package confirmations_test

import (
	"net/http"
	"testing"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/confirmations"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/testsupport"
)

const testUserID = "usera0000000001"
const otherUserID = "userb0000000002"

func TestDecideConfirmationRequiresIdempotencyKey(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	testsupport.SaveRecord(t, pbApp, "agent_confirmations", "conf00000000001", map[string]any{
		"user_id": testUserID, "idempotency_key": "delete-1", "action": "delete-listing",
		"request_hash": "abc", "risk_tier": "destructive", "prompt": "Delete this listing?",
		"status": "pending",
	})

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/decide-confirmation", token,
		map[string]any{"confirmation_id": "conf00000000001", "decision": "approve"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without Idempotency-Key, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDecideConfirmationApprovesAPendingConfirmation(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	testsupport.SaveRecord(t, pbApp, "agent_confirmations", "conf00000000001", map[string]any{
		"user_id": testUserID, "idempotency_key": "delete-1", "action": "delete-listing",
		"request_hash": "abc", "risk_tier": "destructive", "prompt": "Delete this listing?",
		"status": "pending",
	})

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/decide-confirmation", token,
		map[string]any{"confirmation_id": "conf00000000001", "decision": "approve"},
		map[string]string{"Idempotency-Key": "decide-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	confirmation, err := pbApp.FindRecordById("agent_confirmations", "conf00000000001")
	if err != nil {
		t.Fatal(err)
	}
	if confirmation.GetString("status") != "approved" {
		t.Fatalf("expected status approved, got %q", confirmation.GetString("status"))
	}
}

func TestDecideConfirmationApprovingTwiceIsANoOp(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	testsupport.SaveRecord(t, pbApp, "agent_confirmations", "conf00000000001", map[string]any{
		"user_id": testUserID, "idempotency_key": "delete-1", "action": "delete-listing",
		"request_hash": "abc", "risk_tier": "destructive", "prompt": "Delete this listing?",
		"status": "approved",
	})

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/decide-confirmation", token,
		map[string]any{"confirmation_id": "conf00000000001", "decision": "approve"},
		map[string]string{"Idempotency-Key": "decide-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected re-approving an already-approved confirmation to be a no-op 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDecideConfirmationRejectsFlippingAnAlreadyDecidedConfirmation(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	testsupport.SaveRecord(t, pbApp, "agent_confirmations", "conf00000000001", map[string]any{
		"user_id": testUserID, "idempotency_key": "delete-1", "action": "delete-listing",
		"request_hash": "abc", "risk_tier": "destructive", "prompt": "Delete this listing?",
		"status": "approved",
	})

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/decide-confirmation", token,
		map[string]any{"confirmation_id": "conf00000000001", "decision": "decline"},
		map[string]string{"Idempotency-Key": "decide-1"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 declining an already-approved confirmation, got %d: %s", rec.Code, rec.Body.String())
	}
	data, _ := testsupport.Decode(t, rec)["data"].(map[string]any)
	if data["code"] != "confirmation_already_decided" {
		t.Fatalf("expected data.code = confirmation_already_decided, got %v", data)
	}
}

func TestConfirmationsAreOwnerScoped(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	otherUser := testsupport.AddUser(t, pbApp, otherUserID, "b@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	otherToken := testsupport.AuthToken(t, otherUser)

	testsupport.SaveRecord(t, pbApp, "agent_confirmations", "conf00000000001", map[string]any{
		"user_id": testUserID, "idempotency_key": "delete-1", "action": "delete-listing",
		"request_hash": "abc", "risk_tier": "destructive", "prompt": "Delete this listing?",
		"status": "pending",
	})

	rec := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/confirmations/conf00000000001", otherToken, nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for another user's confirmation, got %d: %s", rec.Code, rec.Body.String())
	}
}
