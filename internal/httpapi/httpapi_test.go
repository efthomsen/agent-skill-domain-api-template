package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
	"github.com/pocketbase/pocketbase/core"
)

func TestHashCanonicalIsKeyOrderIndependent(t *testing.T) {
	a := map[string]any{"b": 2, "a": 1, "nested": map[string]any{"y": 2, "x": 1}}
	b := map[string]any{"a": 1, "nested": map[string]any{"x": 1, "y": 2}, "b": 2}

	hashA, err := httpapi.HashCanonical(a)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := httpapi.HashCanonical(b)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB {
		t.Errorf("expected equal hashes for equivalent maps regardless of key order, got %q vs %q", hashA, hashB)
	}

	hashC, err := httpapi.HashCanonical(map[string]any{"a": 1, "b": 3})
	if err != nil {
		t.Fatal(err)
	}
	if hashA == hashC {
		t.Error("expected different hashes for different content")
	}
}

func TestVersionConflictErrorBodyShape(t *testing.T) {
	err := httpapi.VersionConflictError()
	if err.Status != http.StatusConflict {
		t.Errorf("expected status 409, got %d", err.Status)
	}
	if code, _ := err.Data["code"].(string); code != "version_conflict" {
		t.Errorf("expected data.code = version_conflict, got %v", err.Data["code"])
	}
}

func TestCheckExpectedVersionSkipsWhenAbsent(t *testing.T) {
	rec := core.NewRecord(core.NewBaseCollection("scratch"))
	rec.Set("version", 3)

	if err := httpapi.CheckExpectedVersion(map[string]any{}, rec); err != nil {
		t.Errorf("expected no error when expected_version is absent, got %v", err)
	}
}

func TestCheckExpectedVersionRejectsMismatch(t *testing.T) {
	rec := core.NewRecord(core.NewBaseCollection("scratch"))
	rec.Set("version", 3)

	err := httpapi.CheckExpectedVersion(map[string]any{"expected_version": float64(2)}, rec)
	if err == nil {
		t.Fatal("expected an error for a mismatched expected_version")
	}
}

func TestCheckExpectedVersionAcceptsMatch(t *testing.T) {
	rec := core.NewRecord(core.NewBaseCollection("scratch"))
	rec.Set("version", 3)

	if err := httpapi.CheckExpectedVersion(map[string]any{"expected_version": float64(3)}, rec); err != nil {
		t.Errorf("expected no error for a matching expected_version, got %v", err)
	}
}
