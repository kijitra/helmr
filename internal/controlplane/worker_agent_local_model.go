package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"uuid"

	"github.com/helmrdotdev/helmr/internal/db"
	"github.com/helmrdotdev/helmr/internal/localmodel"
	"github.com/helmrdotdev/helmr/internal/session"
	"github.com/helmrdotdev/helmr/internal/workerapi"
	"github.com/jackc/pgx/v5"
)

func (server *Server) workerAgentLocalModel(w http.ResponseWriter, r *http.Request, requestID string, caller session.Caller, payload json.RawMessage) {
	fail := func(code, message string) {
		writeJSON(w, http.StatusOK, agentOperationFailure(requestID, code, message))
	}
	if server.localModel == nil {
		fail("unsupported_operation", "Local model is not enabled")
		return
	}
	var input struct {
		TurnID string `json:"turnId"`
		Prompt string `json:"prompt"`
	}
	if decodeAgentPayload(payload, &input) != nil {
		fail("invalid_arguments", "Invalid local model arguments")
		return
	}
	turnID, err := uuid.Parse(input.TurnID)
	if err != nil || turnID == uuid.Nil() || turnID.String() != input.TurnID {
		fail("invalid_arguments", "Exact running Turn is required")
		return
	}
	scopedCaller := caller
	scopedCaller.TurnID = turnID
	result, err := server.localModel.Infer(r.Context(), caller.Execution.EnvironmentID, input.Prompt, func(ctx context.Context) error {
		return db.RunTx(ctx, server.tx, func(tx pgx.Tx) error { return session.LockRuntimeCaller(ctx, tx, scopedCaller) })
	})
	if err != nil {
		code := "local_model_unavailable"
		switch err {
		case localmodel.ErrDenied:
			code = "denied"
		case localmodel.ErrInvalid:
			code = "invalid_arguments"
		case localmodel.ErrBusy:
			code = "local_model_busy"
		}
		fail(code, err.Error())
		return
	}
	body, _ := json.Marshal(result)
	writeJSON(w, http.StatusOK, workerapi.AgentOperationResponse{RequestID: requestID, Value: body})
}
