package listings_test

import (
	"net/http"
	"testing"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/listings"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/testsupport"
)

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)

	rec := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateListingRequiresIdempotencyKey(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, "usera0000000001", "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	body := map[string]any{"address": "1 Main St", "asking_price": 100000, "description": "cozy"}
	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", token, body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without Idempotency-Key, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIdempotentReplayReturnsSameListingWithoutDuplicating(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, "usera0000000001", "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	body := map[string]any{"address": "1 Main St", "asking_price": 100000, "description": "cozy"}
	headers := map[string]string{"Idempotency-Key": "create-1"}

	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", token, body, headers)
	if first.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", first.Code, first.Body.String())
	}
	firstBody := testsupport.Decode(t, first)
	firstListing, _ := firstBody["listing"].(map[string]any)
	firstID, _ := firstListing["id"].(string)
	if firstID == "" {
		t.Fatalf("expected listing.id in response, got %v", firstBody)
	}

	second := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", token, body, headers)
	if second.Code != http.StatusCreated {
		t.Fatalf("expected replay to also return 201, got %d: %s", second.Code, second.Body.String())
	}
	secondBody := testsupport.Decode(t, second)
	secondListing, _ := secondBody["listing"].(map[string]any)
	if secondListing["id"] != firstID {
		t.Fatalf("expected replay to return the same listing id, got %v vs %v", secondListing["id"], firstID)
	}

	count, err := pbApp.CountRecords("listings")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 listing after idempotent replay, got %d", count)
	}
}

func TestIdempotencyKeyReuseWithDifferentBodyConflicts(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, "usera0000000001", "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	headers := map[string]string{"Idempotency-Key": "create-1"}
	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", token,
		map[string]any{"address": "1 Main St", "asking_price": 100000, "description": "cozy"}, headers)
	if first.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", first.Code, first.Body.String())
	}

	second := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", token,
		map[string]any{"address": "2 Other St", "asking_price": 200000, "description": "different"}, headers)
	if second.Code != http.StatusConflict {
		t.Fatalf("expected 409 for reused key with a different body, got %d: %s", second.Code, second.Body.String())
	}
	decoded := testsupport.Decode(t, second)
	data, _ := decoded["data"].(map[string]any)
	if data["code"] != "idempotency_conflict" {
		t.Fatalf("expected data.code = idempotency_conflict, got %v", decoded)
	}
}

func TestUpdateListingBumpsVersionAndStaleUpdateConflicts(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, "usera0000000001", "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	created := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", token,
		map[string]any{"address": "1 Main St", "asking_price": 100000, "description": "cozy"},
		map[string]string{"Idempotency-Key": "create-1"})
	listingID := testsupport.Decode(t, created)["listing"].(map[string]any)["id"].(string)

	update := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/update-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1, "description": "renovated"},
		map[string]string{"Idempotency-Key": "update-1"})
	if update.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", update.Code, update.Body.String())
	}
	updatedListing := testsupport.Decode(t, update)["listing"].(map[string]any)
	if v, _ := updatedListing["version"].(float64); v != 2 {
		t.Fatalf("expected version 2 after update, got %v", updatedListing["version"])
	}

	stale := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/update-listing", token,
		map[string]any{"listing_id": listingID, "expected_version": 1, "description": "conflicting edit"},
		map[string]string{"Idempotency-Key": "update-2"})
	if stale.Code != http.StatusConflict {
		t.Fatalf("expected 409 for stale expected_version, got %d: %s", stale.Code, stale.Body.String())
	}
	data, _ := testsupport.Decode(t, stale)["data"].(map[string]any)
	if data["code"] != "version_conflict" {
		t.Fatalf("expected data.code = version_conflict, got %v", data)
	}
}

func TestListingsAreOwnerScoped(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	userA := testsupport.AddUser(t, pbApp, "usera0000000001", "a@example.com")
	userB := testsupport.AddUser(t, pbApp, "userb0000000002", "b@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	tokenA := testsupport.AuthToken(t, userA)
	tokenB := testsupport.AuthToken(t, userB)

	created := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", tokenA,
		map[string]any{"address": "1 Main St", "asking_price": 100000, "description": "cozy"},
		map[string]string{"Idempotency-Key": "create-1"})
	listingID := testsupport.Decode(t, created)["listing"].(map[string]any)["id"].(string)

	getAsB := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings/"+listingID, tokenB, nil, nil)
	if getAsB.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for another user's listing, got %d: %s", getAsB.Code, getAsB.Body.String())
	}

	listAsB := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/resources/listings", tokenB, nil, nil)
	listBody := testsupport.Decode(t, listAsB)
	items, _ := listBody["listings"].([]any)
	if len(items) != 0 {
		t.Fatalf("expected user B's listing list to be empty, got %v", items)
	}
}

func TestRequestAssessmentCreatesPendingExecution(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, "usera0000000001", "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	listings.RegisterRoutes(rg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	created := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/create-listing", token,
		map[string]any{"address": "1 Main St", "asking_price": 100000, "description": "cozy"},
		map[string]string{"Idempotency-Key": "create-1"})
	listingID := testsupport.Decode(t, created)["listing"].(map[string]any)["id"].(string)

	resp := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/commands/request-assessment", token,
		map[string]any{"listing_id": listingID, "expected_version": 1},
		map[string]string{"Idempotency-Key": "assess-1"})
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", resp.Code, resp.Body.String())
	}
	body := testsupport.Decode(t, resp)
	executionID, _ := body["execution_id"].(string)
	if executionID == "" {
		t.Fatalf("expected execution_id in response, got %v", body)
	}
	if inputHash, _ := body["input_hash"].(string); inputHash == "" {
		t.Fatalf("expected input_hash in response, got %v", body)
	}

	execution, err := pbApp.FindRecordById("ai_executions", executionID)
	if err != nil {
		t.Fatal(err)
	}
	if execution.GetString("status") != "pending" {
		t.Fatalf("expected execution status pending, got %q", execution.GetString("status"))
	}
	if execution.GetString("target_collection") != "listings" || execution.GetString("target_id") != listingID {
		t.Fatalf("expected execution to target the listing, got %q/%q", execution.GetString("target_collection"), execution.GetString("target_id"))
	}
}
