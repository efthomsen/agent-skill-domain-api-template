// Package confirmations lets a caller inspect and decide on pending
// human-approval gates created by httpapi.RunConfirmable elsewhere in the
// service.
package confirmations

import (
	"errors"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
)

// RegisterRoutes wires the confirmations resource and its decide command
// onto rg.
func RegisterRoutes(rg *router.Router[*core.RequestEvent]) {
	rg.GET(httpapi.APIPrefix+"/resources/confirmations", listAction).Bind(apis.RequireAuth())
	rg.GET(httpapi.APIPrefix+"/resources/confirmations/{id}", getAction).Bind(apis.RequireAuth())
	rg.POST(httpapi.APIPrefix+"/commands/decide-confirmation", decideAction).Bind(apis.RequireAuth())
}

func listAction(e *core.RequestEvent) error {
	records, err := e.App.FindRecordsByFilter("agent_confirmations", "user_id = {:user}", "-created", 0, 0,
		map[string]any{"user": httpapi.AuthID(e)})
	if err != nil {
		return err
	}
	items := make([]any, len(records))
	for i, rec := range records {
		items[i] = confirmationData(rec)
	}
	return e.JSON(http.StatusOK, map[string]any{"confirmations": items})
}

func getAction(e *core.RequestEvent) error {
	rec, err := findOwnedConfirmation(e.App, e.Request.PathValue("id"), httpapi.AuthID(e))
	if err != nil {
		return apis.NewNotFoundError("", err)
	}
	return e.JSON(http.StatusOK, map[string]any{"confirmation": confirmationData(rec)})
}

func decideAction(e *core.RequestEvent) error {
	if httpapi.Header(e, "Idempotency-Key") == "" {
		return apis.NewBadRequestError("Idempotency-Key header is required.", nil)
	}
	body, err := httpapi.Body(e)
	if err != nil {
		return err
	}
	confirmationID, _ := body["confirmation_id"].(string)
	decision, _ := body["decision"].(string)
	var wantStatus string
	switch decision {
	case "approve":
		wantStatus = "approved"
	case "decline":
		wantStatus = "declined"
	default:
		return apis.NewBadRequestError(`decision must be "approve" or "decline".`, nil)
	}

	userID := httpapi.AuthID(e)
	var response map[string]any

	txErr := e.App.RunInTransaction(func(tx core.App) error {
		confirmation, err := findOwnedConfirmation(tx, confirmationID, userID)
		if err != nil {
			return apis.NewNotFoundError("", err)
		}

		switch confirmation.GetString("status") {
		case wantStatus:
			// already decided the same way: idempotent no-op
		case "pending":
			confirmation.Set("status", wantStatus)
			confirmation.Set("decided_at", time.Now())
			if err := tx.Save(confirmation); err != nil {
				return err
			}
		default:
			return httpapi.ConfirmationAlreadyDecidedError()
		}

		response = map[string]any{"confirmation": confirmationData(confirmation)}
		return nil
	})
	if txErr != nil {
		return txErr
	}

	return e.JSON(http.StatusOK, response)
}

func findOwnedConfirmation(app core.App, id, userID string) (*core.Record, error) {
	rec, err := app.FindRecordById("agent_confirmations", id)
	if err != nil {
		return nil, err
	}
	if rec.GetString("user_id") != userID {
		return nil, errors.New("confirmation not owned by caller")
	}
	return rec, nil
}

func confirmationData(rec *core.Record) map[string]any {
	return map[string]any{
		"id":         rec.Id,
		"action":     rec.GetString("action"),
		"risk_tier":  rec.GetString("risk_tier"),
		"prompt":     rec.GetString("prompt"),
		"status":     rec.GetString("status"),
		"proposed_change": httpapi.JSONField(rec, "proposed_change", map[string]any{}),
	}
}
