package executions_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/executions"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/finalizers"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/listings"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/testsupport"
)

const testUserID = "usera0000000001"

// testConfig builds an executions.Config wired the same way main.go wires
// it, so tests exercise the real finalizer lookup rather than a stub.
func testConfig(t *testing.T) executions.Config {
	t.Helper()
	registry := finalizers.NewRegistry()
	if err := listings.RegisterFinalizers(registry); err != nil {
		t.Fatal(err)
	}
	return executions.Config{Finalizers: registry}
}

func seedListing(t *testing.T, app core.App, id string) {
	t.Helper()
	testsupport.SaveRecord(t, app, "listings", id, map[string]any{
		"user_id":      testUserID,
		"address":      "1 Main St",
		"asking_price": 100000,
		"description":  "cozy",
		"version":      1,
	})
}

func seedExecution(t *testing.T, app core.App, id, targetID, status string) {
	t.Helper()
	testsupport.SaveRecord(t, app, "ai_executions", id, map[string]any{
		"user_id":           testUserID,
		"task_key":          "assess_listing",
		"target_collection": "listings",
		"target_id":         targetID,
		"status":            status,
		"input_json":        map[string]any{"listing": map[string]any{"id": targetID}},
		"input_hash":        "deadbeef",
	})
}

func TestClaimNextReturns204WhenNothingPending(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	executions.RegisterRoutes(rg, testConfig(t))
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/claim-next", token, map[string]any{}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestClaimContextResultRoundTrip(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	executions.RegisterRoutes(rg, testConfig(t))
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	seedListing(t, pbApp, "listing00000001")
	seedExecution(t, pbApp, "exec00000000001", "listing00000001", "pending")

	claim := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/claim-next", token, map[string]any{}, nil)
	if claim.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", claim.Code, claim.Body.String())
	}
	claimBody := testsupport.Decode(t, claim)
	leaseToken, _ := claimBody["lease_token"].(string)
	if leaseToken == "" {
		t.Fatalf("expected a lease_token, got %v", claimBody)
	}
	execution, _ := claimBody["execution"].(map[string]any)
	if execution["id"] != "exec00000000001" {
		t.Fatalf("expected to claim the pending execution, got %v", execution)
	}

	ctxRec := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/ai/executions/exec00000000001/context", token, nil,
		map[string]string{"X-Lease-Token": leaseToken})
	if ctxRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for context, got %d: %s", ctxRec.Code, ctxRec.Body.String())
	}
	ctxBody := testsupport.Decode(t, ctxRec)
	if ctxBody["input_hash"] != "deadbeef" {
		t.Fatalf("expected input_hash deadbeef, got %v", ctxBody["input_hash"])
	}

	resultRec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/exec00000000001/result", token,
		map[string]any{"lease_token": leaseToken, "input_hash": "deadbeef", "output": map[string]any{"assessment": "Fairly priced."}}, nil)
	if resultRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for result, got %d: %s", resultRec.Code, resultRec.Body.String())
	}

	listing, err := pbApp.FindRecordById("listings", "listing00000001")
	if err != nil {
		t.Fatal(err)
	}
	if listing.GetString("assessment") != "Fairly priced." {
		t.Fatalf("expected assessment to be applied, got %q", listing.GetString("assessment"))
	}
	if listing.GetInt("version") != 2 {
		t.Fatalf("expected listing version 2, got %d", listing.GetInt("version"))
	}

	execRec, err := pbApp.FindRecordById("ai_executions", "exec00000000001")
	if err != nil {
		t.Fatal(err)
	}
	if execRec.GetString("status") != "completed" {
		t.Fatalf("expected execution status completed, got %q", execRec.GetString("status"))
	}
	if execRec.GetString("lease_token") != "" {
		t.Fatal("expected the lease to be burned after completion")
	}
}

func TestResultReplayIsIdempotent(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	executions.RegisterRoutes(rg, testConfig(t))
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	seedListing(t, pbApp, "listing00000001")
	seedExecution(t, pbApp, "exec00000000001", "listing00000001", "pending")

	claim := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/claim-next", token, map[string]any{}, nil)
	leaseToken := testsupport.Decode(t, claim)["lease_token"].(string)

	submit := func() *httptest.ResponseRecorder {
		return testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/exec00000000001/result", token,
			map[string]any{"lease_token": leaseToken, "input_hash": "deadbeef", "output": map[string]any{"assessment": "Fairly priced."}}, nil)
	}

	first := submit()
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", first.Code, first.Body.String())
	}
	second := submit()
	if second.Code != http.StatusOK {
		t.Fatalf("expected replay to also return 200, got %d: %s", second.Code, second.Body.String())
	}
	if testsupport.Decode(t, first)["result"].(map[string]any)["version"] != testsupport.Decode(t, second)["result"].(map[string]any)["version"] {
		t.Fatal("expected the replayed result to match the original")
	}

	listing, err := pbApp.FindRecordById("listings", "listing00000001")
	if err != nil {
		t.Fatal(err)
	}
	if listing.GetInt("version") != 2 {
		t.Fatalf("expected the listing to be bumped only once, got version %d", listing.GetInt("version"))
	}
}

func TestResultWrongLeaseTokenIsForbidden(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	executions.RegisterRoutes(rg, testConfig(t))
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	seedListing(t, pbApp, "listing00000001")
	seedExecution(t, pbApp, "exec00000000001", "listing00000001", "pending")
	testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/claim-next", token, map[string]any{}, nil)

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/exec00000000001/result", token,
		map[string]any{"lease_token": "wrong-token", "input_hash": "deadbeef", "output": map[string]any{"assessment": "x"}}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a wrong lease token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestContextWithoutLeaseTokenIsForbidden(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	executions.RegisterRoutes(rg, testConfig(t))
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	seedListing(t, pbApp, "listing00000001")
	seedExecution(t, pbApp, "exec00000000001", "listing00000001", "pending")
	testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/claim-next", token, map[string]any{}, nil)

	rec := testsupport.Do(t, handler, http.MethodGet, httpapi.APIPrefix+"/ai/executions/exec00000000001/context", token, nil, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a lease token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestResultInputHashMismatchConflicts(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	executions.RegisterRoutes(rg, testConfig(t))
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	seedListing(t, pbApp, "listing00000001")
	seedExecution(t, pbApp, "exec00000000001", "listing00000001", "pending")
	claim := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/claim-next", token, map[string]any{}, nil)
	leaseToken := testsupport.Decode(t, claim)["lease_token"].(string)

	rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/exec00000000001/result", token,
		map[string]any{"lease_token": leaseToken, "input_hash": "not-the-right-hash", "output": map[string]any{"assessment": "x"}}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for an input_hash mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
	data, _ := testsupport.Decode(t, rec)["data"].(map[string]any)
	if data["code"] != "input_changed" {
		t.Fatalf("expected data.code = input_changed, got %v", data)
	}
}

func TestConcurrentClaimNextLeasesExactlyOnce(t *testing.T) {
	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	executions.RegisterRoutes(rg, testConfig(t))
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	seedListing(t, pbApp, "listing00000001")
	seedExecution(t, pbApp, "exec00000000001", "listing00000001", "pending")

	const goroutines = 16
	var ready, start sync.WaitGroup
	ready.Add(goroutines)
	start.Add(1)

	var ok200, ok204 int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ready.Done()
			start.Wait()
			rec := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/claim-next", token, map[string]any{}, nil)
			switch rec.Code {
			case http.StatusOK:
				atomic.AddInt64(&ok200, 1)
				body := testsupport.Decode(t, rec)
				execution, _ := body["execution"].(map[string]any)
				if execution["id"] != "exec00000000001" {
					t.Errorf("expected the winning claim to be the seeded execution, got %v", execution)
				}
			case http.StatusNoContent:
				atomic.AddInt64(&ok204, 1)
			default:
				t.Errorf("unexpected status %d: %s", rec.Code, rec.Body.String())
			}
		}()
	}
	ready.Wait()
	start.Done()
	wg.Wait()

	if ok200 != 1 {
		t.Fatalf("expected exactly 1 successful claim, got %d", ok200)
	}
	if ok204 != goroutines-1 {
		t.Fatalf("expected %d 204s, got %d", goroutines-1, ok204)
	}
}

func TestExpiredLeaseIsReclaimable(t *testing.T) {
	now := time.Now()
	clock := &fakeClock{t: now}
	cfg := testConfig(t)
	cfg.LeaseTTL = time.Second
	cfg.Now = clock.Now

	pbApp := testsupport.NewMigratedApp(t)
	user := testsupport.AddUser(t, pbApp, testUserID, "a@example.com")
	rg := testsupport.NewRouter(t, pbApp)
	executions.RegisterRoutes(rg, cfg)
	handler := testsupport.BuildHandler(t, rg)
	token := testsupport.AuthToken(t, user)

	seedListing(t, pbApp, "listing00000001")
	seedExecution(t, pbApp, "exec00000000001", "listing00000001", "pending")

	first := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/claim-next", token, map[string]any{}, nil)
	firstToken := testsupport.Decode(t, first)["lease_token"].(string)

	clock.advance(2 * time.Second)

	second := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/claim-next", token, map[string]any{}, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("expected the expired lease to be reclaimable, got %d: %s", second.Code, second.Body.String())
	}
	secondToken := testsupport.Decode(t, second)["lease_token"].(string)
	if secondToken == firstToken {
		t.Fatal("expected a new lease_token after reclaiming an expired lease")
	}

	stale := testsupport.Do(t, handler, http.MethodPost, httpapi.APIPrefix+"/ai/executions/exec00000000001/result", token,
		map[string]any{"lease_token": firstToken, "input_hash": "deadbeef", "output": map[string]any{"assessment": "x"}}, nil)
	if stale.Code != http.StatusForbidden {
		t.Fatalf("expected the old lease token to be rejected, got %d: %s", stale.Code, stale.Body.String())
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
