// Package localmodel provides an opt-in, bounded developer-only inference path.
// It never accepts caller-selected endpoints, models, tools or transport options.
package localmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"
)

const Model = "qwen3.8:27b"
const MaxPromptBytes = 16 << 10
const maxResponseBytes = 64 << 10
const operationTimeout = 40 * time.Second
const authorityTimeout = 2 * time.Second
const authorityInterval = 500 * time.Millisecond

var (
	ErrDenied      = errors.New("local model execution is not authorized")
	ErrInvalid     = errors.New("invalid local model prompt")
	ErrBusy        = errors.New("local model is busy")
	ErrUnavailable = errors.New("local model is unavailable")
)

type Gateway struct {
	endpoint    string
	environment uuid.UUID
	client      *http.Client
	slot        chan struct{}
}

type Result struct {
	Text  string `json:"text"`
	Model string `json:"model"`
}

// New allows only a literal loopback HTTP endpoint. A dedicated SSH reverse
// forward provides access to the Mac's existing loopback-only Ollama service.
func New(endpoint string, environment uuid.UUID) (*Gateway, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("local model requires a literal loopback HTTP endpoint")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || environment == uuid.Nil() || endpoint != "http://127.0.0.1:"+strconv.Itoa(port) {
		return nil, errors.New("local model requires an explicit port and environment")
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 16 << 10, ResponseHeaderTimeout: operationTimeout}
	return &Gateway{endpoint: endpoint + "/api/chat", environment: environment, client: &http.Client{
		Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, slot: make(chan struct{}, 1)}, nil
}

// Infer commits each authorization check before doing network I/O. Rechecks
// revoke in-flight requests on authority loss; cancellation is not proof of GPU
// cessation at the upstream model server.
func (g *Gateway) Infer(ctx context.Context, environment uuid.UUID, prompt string, authorize func(context.Context) error) (Result, error) {
	if g == nil || environment != g.environment || authorize == nil {
		return Result{}, ErrDenied
	}
	if len(prompt) < 1 || len(prompt) > MaxPromptBytes || !utf8.ValidString(prompt) || strings.TrimSpace(prompt) == "" {
		return Result{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	check := func() error {
		authCtx, authCancel := context.WithTimeout(ctx, authorityTimeout)
		defer authCancel()
		if err := authorize(authCtx); err != nil {
			return ErrDenied
		}
		return nil
	}
	if err := check(); err != nil {
		return Result{}, err
	}
	select {
	case g.slot <- struct{}{}:
		defer func() { <-g.slot }()
	default:
		return Result{}, ErrBusy
	}
	if err := check(); err != nil {
		return Result{}, err
	}
	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	pollDone := make(chan bool, 1)
	go func() {
		ticker := time.NewTicker(authorityInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				pollDone <- false
				return
			case <-ticker.C:
				if check() != nil {
					pollDone <- true
					cancel()
					return
				}
			}
		}
	}()
	result, err := g.request(ctx, prompt)
	// Wait for the poller before releasing the concurrency slot.
	authorityLost := false
	contextLost := ctx.Err() != nil
	if ctx.Err() == nil {
		if check() != nil {
			authorityLost = true
		}
	}
	stopPoll()
	denied := <-pollDone
	if denied || authorityLost {
		return Result{}, ErrDenied
	}
	if err != nil {
		return Result{}, err
	}
	if contextLost || ctx.Err() != nil {
		return Result{}, ErrUnavailable
	}
	return result, nil
}

func (g *Gateway) request(ctx context.Context, prompt string) (Result, error) {
	body, _ := json.Marshal(map[string]any{
		"model": Model, "messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream": false, "think": false, "keep_alive": "5m",
		"options": map[string]any{"num_ctx": 4096, "num_predict": 256, "temperature": 0},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return Result{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes || !utf8.Valid(raw) {
		return Result{}, ErrUnavailable
	}
	var output struct {
		Model   string `json:"model"`
		Done    bool   `json:"done"`
		Error   string `json:"error"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &output) != nil || !output.Done || output.Error != "" || output.Model != Model || output.Message.Role != "assistant" || strings.TrimSpace(output.Message.Content) == "" || len(output.Message.Content) > 16<<10 {
		return Result{}, ErrUnavailable
	}
	if ctx.Err() != nil {
		return Result{}, ErrUnavailable
	}
	return Result{Text: output.Message.Content, Model: Model}, nil
}
