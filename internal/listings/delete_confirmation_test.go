package listings_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/confirmations"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/listings"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/testsupport"
)

const testUserID = "usera0000000001"

func createListingForDeleteTests(t *testing.T, handler http.Handler, token string) string {
	t.Helper()
	created := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", token,
		map[string]any{"address": "1 Main St", "asking_price": 100000, "description": "cozy"},
		map[string]string{"Idempotency-Key": "create-" + t.Name()})
	if created.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating a listing, got %d: %s", created.Code, created.Body.String())
	}
	return testsupport.Decode(t, created)["listing"].(map[string]any)["id"].(string)
}

func TestDeleteListingRequiresConfirmation(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	listingID := createListingForDeleteTests(t, handler, token)

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1},
		map[string]string{"Idempotency-Key": "delete-1"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 confirmation_required, got %d: %s", rec.Code, rec.Body.String())
	}
	body := testsupport.Decode(t, rec)
	if body["status"] != "confirmation_required" || body["confirmation_id"] == "" {
		t.Fatalf("expected a confirmation_required response with a confirmation_id, got %v", body)
	}

	get := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings/"+listingID, token, nil, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("expected the listing to still exist before confirmation, got %d: %s", get.Code, get.Body.String())
	}
}

func TestReplayingDeleteWhilePendingDoesNotDuplicateConfirmation(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	listingID := createListingForDeleteTests(t, handler, token)
	body := map[string]any{"listing_id": listingID, "expected_version": 1}
	headers := map[string]string{"Idempotency-Key": "delete-1"}

	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token, body, headers)
	second := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token, body, headers)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("expected both replays to return 202, got %d and %d", first.Code, second.Code)
	}
	firstID := testsupport.Decode(t, first)["confirmation_id"]
	secondID := testsupport.Decode(t, second)["confirmation_id"]
	if firstID != secondID {
		t.Fatalf("expected replaying the same pending request to return the same confirmation_id, got %v vs %v", firstID, secondID)
	}

	count, err := pbApp.CountRecords("agent_confirmations")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 confirmation after replaying while pending, got %d", count)
	}
}

func TestApprovedConfirmationThenResubmitDeletesListing(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	listingID := createListingForDeleteTests(t, handler, token)
	deleteKey := map[string]string{"Idempotency-Key": "delete-1"}

	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1}, deleteKey)
	confirmationID := testsupport.Decode(t, first)["confirmation_id"].(string)

	decide := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/decide-confirmation", token,
		map[string]any{"confirmation_id": confirmationID, "decision": "approve"},
		map[string]string{"Idempotency-Key": "decide-1"})
	if decide.Code != http.StatusOK {
		t.Fatalf("expected 200 approving the confirmation, got %d: %s", decide.Code, decide.Body.String())
	}

	resubmit := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1, "confirmation_id": confirmationID}, deleteKey)
	if resubmit.Code != http.StatusOK {
		t.Fatalf("expected 200 executing the approved delete, got %d: %s", resubmit.Code, resubmit.Body.String())
	}

	get := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings/"+listingID, token, nil, nil)
	if get.Code != http.StatusNotFound {
		t.Fatalf("expected the soft-deleted listing to 404, got %d: %s", get.Code, get.Body.String())
	}

	confirmation, err := pbApp.FindRecordById("agent_confirmations", confirmationID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmation.GetString("status") != "consumed" {
		t.Fatalf("expected the confirmation to be consumed, got %q", confirmation.GetString("status"))
	}
}

func TestDeclinedConfirmationCannotBeBypassedByResubmit(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	listingID := createListingForDeleteTests(t, handler, token)
	deleteKey := map[string]string{"Idempotency-Key": "delete-1"}

	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1}, deleteKey)
	confirmationID := testsupport.Decode(t, first)["confirmation_id"].(string)

	testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/decide-confirmation", token,
		map[string]any{"confirmation_id": confirmationID, "decision": "decline"},
		map[string]string{"Idempotency-Key": "decide-1"})

	resubmit := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1, "confirmation_id": confirmationID}, deleteKey)
	if resubmit.Code != http.StatusConflict {
		t.Fatalf("expected 409 resubmitting a declined delete, got %d: %s", resubmit.Code, resubmit.Body.String())
	}
	data, _ := testsupport.Decode(t, resubmit)["data"].(map[string]any)
	if data["code"] != "confirmation_declined" {
		t.Fatalf("expected data.code = confirmation_declined, got %v", data)
	}

	get := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings/"+listingID, token, nil, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("expected the declined listing to still exist, got %d: %s", get.Code, get.Body.String())
	}
}

func TestConsumedConfirmationCannotBeReusedForADifferentMutation(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	listingOneID := createListingForDeleteTests(t, handler, token)

	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingOneID, "expected_version": 1},
		map[string]string{"Idempotency-Key": "delete-1"})
	confirmationID := testsupport.Decode(t, first)["confirmation_id"].(string)
	testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/decide-confirmation", token,
		map[string]any{"confirmation_id": confirmationID, "decision": "approve"},
		map[string]string{"Idempotency-Key": "decide-1"})
	testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingOneID, "expected_version": 1, "confirmation_id": confirmationID},
		map[string]string{"Idempotency-Key": "delete-1"})

	// A second, unrelated listing's delete tries to reuse the now-consumed
	// confirmation id under a brand new Idempotency-Key.
	created := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", token,
		map[string]any{"address": "2 Other St", "asking_price": 50000, "description": "other"},
		map[string]string{"Idempotency-Key": "create-2"})
	listingTwoID := testsupport.Decode(t, created)["listing"].(map[string]any)["id"].(string)

	reuse := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingTwoID, "expected_version": 1, "confirmation_id": confirmationID},
		map[string]string{"Idempotency-Key": "delete-2"})
	if reuse.Code != http.StatusAccepted {
		t.Fatalf("expected the reused confirmation_id to be ignored and get its own fresh confirmation_required, got %d: %s", reuse.Code, reuse.Body.String())
	}
	newConfirmationID := testsupport.Decode(t, reuse)["confirmation_id"]
	if newConfirmationID == confirmationID {
		t.Fatal("expected a fresh confirmation_id, not the reused/consumed one")
	}

	get := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings/"+listingTwoID, token, nil, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("expected the second listing to remain undeleted, got %d: %s", get.Code, get.Body.String())
	}
}

func TestApprovedConfirmationSurvivesAFailedRetry(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	confirmations.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	listingID := createListingForDeleteTests(t, handler, token)
	deleteKey := map[string]string{"Idempotency-Key": "delete-1"}

	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1}, deleteKey)
	confirmationID := testsupport.Decode(t, first)["confirmation_id"].(string)

	testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/decide-confirmation", token,
		map[string]any{"confirmation_id": confirmationID, "decision": "approve"},
		map[string]string{"Idempotency-Key": "decide-1"})

	// The listing moves on (version 1 -> 2) before the approved delete is
	// actually resubmitted.
	testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/update-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1, "description": "updated"},
		map[string]string{"Idempotency-Key": "update-1"})

	staleResubmit := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1, "confirmation_id": confirmationID}, deleteKey)
	if staleResubmit.Code != http.StatusConflict {
		t.Fatalf("expected 409 version_conflict on the stale resubmit, got %d: %s", staleResubmit.Code, staleResubmit.Body.String())
	}

	confirmation, err := pbApp.FindRecordById("agent_confirmations", confirmationID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmation.GetString("status") != "approved" {
		t.Fatalf("expected the confirmation to remain approved after a failed retry, got %q", confirmation.GetString("status"))
	}

	corrected := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/delete-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 2, "confirmation_id": confirmationID}, deleteKey)
	if corrected.Code != http.StatusOK {
		t.Fatalf("expected 200 on the corrected retry, got %d: %s", corrected.Code, corrected.Body.String())
	}
}

func TestDeletedListingIsExcludedFromListAndGet(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	listingID := createListingForDeleteTests(t, handler, token)
	testsupport.SaveRecord(t, pbApp, "listings", listingID, map[string]any{"deleted_at": time.Now()})

	get := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings/"+listingID, token, nil, nil)
	if get.Code != http.StatusNotFound {
		t.Fatalf("expected a deleted listing to 404 on GET, got %d: %s", get.Code, get.Body.String())
	}

	list := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings", token, nil, nil)
	items, _ := testsupport.Decode(t, list)["listings"].([]any)
	if len(items) != 0 {
		t.Fatalf("expected a deleted listing to be excluded from the list, got %v", items)
	}
}
