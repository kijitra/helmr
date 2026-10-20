package localmodel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"
)

func TestEndpointRejectsNonLoopbackAndURLFeatures(t *testing.T) {
	for _, endpoint := range []string{"http://localhost:11434", "https://127.0.0.1:11434", "http://192.168.0.1:11434", "http://u:p@127.0.0.1:11434", "http://127.0.0.1:11434/", "http://127.0.0.1:11434?", "http://127.0.0.1:11434#x", "http://127.0.0.1", "http://127.0.0.1:0"} {
		if _, err := New(endpoint, uuid.NewV7()); err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
}

func reply(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{"model": Model, "done": true, "message": map[string]string{"role": "assistant", "content": "ローカルLLMの応答"}})
}

func TestInferenceBindsRequestAndRechecksAuthority(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.String() != "/api/chat" {
			t.Error("unexpected upstream method/path")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != Model || body["stream"] != false || body["think"] != false || body["tools"] != nil {
			t.Error("unbounded model request")
		}
		reply(w)
	}))
	defer upstream.Close()
	env := uuid.NewV7()
	g, err := New(upstream.URL, env)
	if err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	result, err := g.Infer(t.Context(), env, "hello", func(context.Context) error { checks.Add(1); return nil })
	if err != nil || result.Model != Model || result.Text == "" || checks.Load() < 3 || calls.Load() != 1 {
		t.Fatalf("result %+v %v checks=%d calls=%d", result, err, checks.Load(), calls.Load())
	}
	_, err = g.Infer(t.Context(), uuid.NewV7(), "hello", func(context.Context) error { return nil })
	if !errors.Is(err, ErrDenied) || calls.Load() != 1 {
		t.Fatalf("wrong environment %v", err)
	}
	for _, prompt := range []string{"", " ", strings.Repeat("x", MaxPromptBytes+1), string([]byte{0xff})} {
		if _, err = g.Infer(t.Context(), env, prompt, func(context.Context) error { return nil }); !errors.Is(err, ErrInvalid) {
			t.Fatalf("prompt accepted %v", err)
		}
	}
	_, err = g.Infer(t.Context(), env, "hello", func(context.Context) error { return errors.New("private database details") })
	if !errors.Is(err, ErrDenied) || calls.Load() != 1 {
		t.Fatalf("authority failure %v", err)
	}
}

func TestInferenceRejectsUnboundedOrInvalidResponses(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", maxResponseBytes+1), `{"done":true}`, `{"done":false}`, `{"model":"other","done":true,"message":{"role":"assistant","content":"text"}}`, `{"model":"` + Model + `","done":true,"message":{"role":"assistant","content":"text"}} {}`, `{`} {
		t.Run("response", func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer upstream.Close()
			env := uuid.NewV7()
			g, _ := New(upstream.URL, env)
			if _, err := g.Infer(t.Context(), env, "hello", func(context.Context) error { return nil }); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("accepted response: %v", err)
			}
		})
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); reply(w) }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer upstream.Close()
	env := uuid.NewV7()
	g, _ := New(upstream.URL, env)
	if _, err := g.Infer(t.Context(), env, "hello", func(context.Context) error { return nil }); !errors.Is(err, ErrUnavailable) || redirected.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestInferenceRevocationCancelsHTTPAndBoundsConcurrency(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		// Consume the request before waiting so the server can observe disconnect.
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
			t.Error("HTTP cancellation not observed")
		}
	}))
	defer upstream.Close()
	env := uuid.NewV7()
	g, _ := New(upstream.URL, env)
	var revoked atomic.Bool
	done := make(chan error, 1)
	go func() {
		_, err := g.Infer(t.Context(), env, "hello", func(context.Context) error {
			if revoked.Load() {
				return ErrDenied
			}
			return nil
		})
		done <- err
	}()
	<-started
	if _, err := g.Infer(t.Context(), env, "hello", func(context.Context) error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrency %v", err)
	}
	revoked.Store(true)
	select {
	case err := <-done:
		if !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not finish")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream HTTP still active")
	}
}

func TestInferenceDoesNotReleaseResponseAfterAuthorityLoss(t *testing.T) {
	var lost atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { lost.Store(true); reply(w) }))
	defer upstream.Close()
	env := uuid.NewV7()
	g, _ := New(upstream.URL, env)
	result, err := g.Infer(t.Context(), env, "hello", func(context.Context) error {
		if lost.Load() {
			return ErrDenied
		}
		return nil
	})
	if !errors.Is(err, ErrDenied) || result.Text != "" {
		t.Fatalf("late result leaked %+v %v", result, err)
	}
}
