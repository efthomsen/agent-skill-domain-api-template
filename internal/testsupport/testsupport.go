// Package testsupport provides the shared PocketBase integration-test
// bootstrap used by every internal package's routes_test.go, so each one
// doesn't re-derive how to boot a throwaway app and drive it over HTTP.
package testsupport

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	_ "github.com/efthomsen/agent-skill-domain-api-template/migrations"
)

// NewMigratedApp boots a throwaway PocketBase app in a temp dir and runs all
// compiled migrations against it.
func NewMigratedApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(),
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}

// NewRouter builds a bare apis router for the app so a single package's
// RegisterRoutes can be exercised in isolation.
func NewRouter(t *testing.T, app core.App) *router.Router[*core.RequestEvent] {
	t.Helper()
	r, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// AddUser creates a "users" auth record with the given id/email.
func AddUser(t *testing.T, app core.App, id, email string) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Id = id
	record.Load(map[string]any{"email": email})
	record.SetPassword("Test-Password-1234!")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	return record
}

// SaveRecord creates or overwrites a record with the given id and field
// values in the named collection, letting tests seed fixtures directly
// without going through a package's HTTP routes.
func SaveRecord(t *testing.T, app core.App, collectionName, id string, values map[string]any) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(collectionName)
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.FindRecordById(collection, id)
	if err != nil {
		record = core.NewRecord(collection)
		record.Id = id
	}
	record.Load(values)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	return record
}

// AuthToken issues a real PocketBase auth token for the given user record.
func AuthToken(t *testing.T, user *core.Record) string {
	t.Helper()
	token, err := user.NewAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// BuildHandler builds rg into an http.Handler once. BuildMux mutates
// internal router state as it walks route groups, so it must be called
// exactly once per router and the resulting handler reused across
// requests — including concurrent ones — rather than rebuilt per call.
func BuildHandler(t *testing.T, rg *router.Router[*core.RequestEvent]) http.Handler {
	t.Helper()
	mux, err := rg.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

// Do executes one request against a handler built by BuildHandler,
// returning the raw response recorder.
func Do(t *testing.T, handler http.Handler, method, path, token string, body map[string]any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// Decode parses a recorded response body as JSON.
func Decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if rec.Body.Len() == 0 {
		return out
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
	}
	return out
}
