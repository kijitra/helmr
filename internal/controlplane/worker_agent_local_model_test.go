package controlplane

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

	"github.com/helmrdotdev/helmr/internal/db/dbtest"
	"github.com/helmrdotdev/helmr/internal/localmodel"
	"github.com/helmrdotdev/helmr/internal/runtimemcp"
	"github.com/helmrdotdev/helmr/internal/session"
	"github.com/helmrdotdev/helmr/internal/session/sessiontest"
	"github.com/helmrdotdev/helmr/internal/workerapi"
)

func TestWorkerAgentLocalModelFencesRuntimeAuthority(t *testing.T) {
	for _, scenario := range []string{"valid", "disabled", "wrong environment", "unknown arguments", "foreign turn", "closed processing", "expired turn", "held", "stale generation", "stale lease", "stale process"} {
		t.Run(scenario, func(t *testing.T) {
			f := sessiontest.New(t)
			admission, err := session.Enqueue(t.Context(), f.Pool, nil, session.Caller{Kind: "user", ID: f.User}, session.EnqueueRequest{EnvironmentID: f.Environment, SessionID: f.Session, IdempotencyKey: "model-origin", Input: json.RawMessage(`[]`)})
			if err != nil {
				t.Fatal(err)
			}
			execution := session.Execution{EnvironmentID: f.Environment, SessionID: f.Session, WorkerHostID: f.Worker, WorkerEpoch: 1, ProcessEpoch: 1, LeaseEpoch: 1, AuthorityGeneration: 1}
			if _, err := dispatchRuntimeTurn(t, f.Pool, execution); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"model": localmodel.Model, "done": true, "message": map[string]string{"role": "assistant", "content": "test reply"}})
			}))
			defer model.Close()
			env := f.Environment
			if scenario == "wrong environment" {
				env = uuid.NewV7()
			}
			gateway, err := localmodel.New(model.URL, env)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "disabled" {
				gateway = nil
			}
			server := httptest.NewServer(newPostgresServer(t, f.Pool, func(cfg *ServerConfig) { cfg.LocalModel = gateway }))
			defer server.Close()
			secret := seedHostSecret(t, f.Pool, f.Worker)
			client := secret.client(t, server.URL)
			args := map[string]any{"turnId": admission.TurnID.String(), "prompt": "hello"}
			switch scenario {
			case "unknown arguments":
				args["endpoint"] = "http://127.0.0.1:9999"
			case "foreign turn":
				args["turnId"] = uuid.NewV7().String()
			case "closed processing":
				if err := closeRuntimeProcessing(t, f.Pool, execution, admission.TurnID); err != nil {
					t.Fatal(err)
				}
			case "expired turn":
				dbtest.MustExec(t, t.Context(), f.Pool, `UPDATE turns SET deadline_at=clock_timestamp()-interval '1 second' WHERE id=$1`, admission.TurnID)
			case "held":
				dbtest.MustExec(t, t.Context(), f.Pool, `INSERT INTO session_holds(environment_id,id,session_id,scope,reason) VALUES($1,$2,$3,'local','explicit hold')`, f.Environment, uuid.NewV7(), f.Session)
			}
			body, _ := json.Marshal(map[string]any{"tool": "infer_local_model", "arguments": args})
			request := workerapi.AgentOperationRequest{Session: runtimeTestSession(f), RequestID: uuid.NewV7().String(), AuthorityGeneration: 1, Method: workerapi.AgentMethodRuntimeMCP, Payload: body}
			switch scenario {
			case "stale generation":
				request.AuthorityGeneration = 2
			case "stale lease":
				request.Session.ComputerLeaseEpoch = 2
			case "stale process":
				request.Session.ProcessEpoch = 2
			}
			response, err := client.AgentOperation(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "valid" {
				if response.Error != nil || calls.Load() != 1 {
					t.Fatalf("valid %+v calls=%d", response, calls.Load())
				}

				mcpHandler := runtimemcp.NewHandler(func(ctx context.Context, tool string, arguments json.RawMessage) (json.RawMessage, error) {
					call := request
					call.Payload, _ = json.Marshal(map[string]any{"tool": tool, "arguments": arguments})
					call.RequestID = uuid.NewV7().String()
					result, err := client.AgentOperation(ctx, call)
					if err != nil {
						return nil, err
					}
					if result.Error != nil {
						return nil, errors.New(result.Error.Code)
					}
					return result.Value, nil
				})
				rpcBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "infer_local_model", "arguments": args}})
				req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(string(rpcBody)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("MCP-Protocol-Version", "2025-11-25")
				recorder := httptest.NewRecorder()
				mcpHandler.ServeHTTP(recorder, req)
				if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "test reply") || strings.Contains(recorder.Body.String(), `"isError":true`) || calls.Load() != 2 {
					t.Fatalf("MCP integration %d %s", recorder.Code, recorder.Body.String())
				}
			} else if response.Error == nil || calls.Load() != 0 {
				t.Fatalf("authority accepted %+v calls=%d", response, calls.Load())
			}
		})
	}
}

func TestWorkerAgentLocalModelCancelsOnLiveHold(t *testing.T) {
	f := sessiontest.New(t)
	admission, err := session.Enqueue(t.Context(), f.Pool, nil, session.Caller{Kind: "user", ID: f.User}, session.EnqueueRequest{EnvironmentID: f.Environment, SessionID: f.Session, IdempotencyKey: "inflight-model", Input: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	execution := session.Execution{EnvironmentID: f.Environment, SessionID: f.Session, WorkerHostID: f.Worker, WorkerEpoch: 1, ProcessEpoch: 1, LeaseEpoch: 1, AuthorityGeneration: 1}
	if _, err := dispatchRuntimeTurn(t, f.Pool, execution); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input any
		_ = json.NewDecoder(r.Body).Decode(&input)
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(6 * time.Second):
			t.Error("inference HTTP was not cancelled")
		}
	}))
	defer upstream.Close()
	gateway, err := localmodel.New(upstream.URL, f.Environment)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newPostgresServer(t, f.Pool, func(cfg *ServerConfig) { cfg.LocalModel = gateway }))
	defer server.Close()
	secret := seedHostSecret(t, f.Pool, f.Worker)
	client := secret.client(t, server.URL)
	payload, _ := json.Marshal(map[string]any{"tool": "infer_local_model", "arguments": map[string]string{"turnId": admission.TurnID.String(), "prompt": "hello"}})
	done := make(chan error, 1)
	go func() {
		response, err := client.AgentOperation(t.Context(), workerapi.AgentOperationRequest{Session: runtimeTestSession(f), RequestID: uuid.NewV7().String(), AuthorityGeneration: 1, Method: workerapi.AgentMethodRuntimeMCP, Payload: payload})
		if err == nil && (response.Error == nil || len(response.Value) > 0) {
			err = errors.New("revoked inference returned a result")
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("inference not started")
	}
	// A real lock-taking mutation must commit while HTTP inference is blocked.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = f.Pool.Exec(ctx, `UPDATE sessions SET authority_generation=authority_generation+1 WHERE environment_id=$1 AND id=$2`, f.Environment, f.Session)
	if err != nil {
		t.Fatalf("network operation retained authority locks: %v", err)
	}
	dbtest.MustExec(t, t.Context(), f.Pool, `INSERT INTO session_holds(environment_id,id,session_id,scope,reason) VALUES($1,$2,$3,'local','explicit hold')`, f.Environment, uuid.NewV7(), f.Session)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("live authority loss did not fence inference")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream HTTP was not cancelled")
	}
}
