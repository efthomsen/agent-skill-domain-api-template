package finalizers_test

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/finalizers"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/testsupport"
)

func noopFinalizer(tx core.App, execution *core.Record, output map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func TestRegisterRejectsDuplicateTaskKey(t *testing.T) {
	registry := finalizers.NewRegistry()
	if err := registry.Register("assess_listing", noopFinalizer); err != nil {
		t.Fatalf("expected the first registration to succeed, got %v", err)
	}
	if err := registry.Register("assess_listing", noopFinalizer); err == nil {
		t.Fatal("expected registering the same task key twice to error")
	}
}

func TestLookupFindsARegisteredFinalizer(t *testing.T) {
	registry := finalizers.NewRegistry()
	_ = registry.Register("assess_listing", noopFinalizer)

	if _, ok := registry.Lookup("assess_listing"); !ok {
		t.Fatal("expected to find the registered finalizer")
	}
	if _, ok := registry.Lookup("unregistered_task"); ok {
		t.Fatal("expected no finalizer for an unregistered task key")
	}
}

func TestValidateCoveragePassesWhenEveryEnabledTaskIsRegistered(t *testing.T) {
	app := testsupport.NewMigratedApp(t) // seeds an enabled "assess_listing" definition
	registry := finalizers.NewRegistry()
	_ = registry.Register("assess_listing", noopFinalizer)

	if err := finalizers.ValidateCoverage(app, registry); err != nil {
		t.Fatalf("expected coverage to pass, got %v", err)
	}
}

func TestValidateCoverageFailsWhenAnEnabledTaskHasNoFinalizer(t *testing.T) {
	app := testsupport.NewMigratedApp(t)
	registry := finalizers.NewRegistry() // deliberately nothing registered

	err := finalizers.ValidateCoverage(app, registry)
	if err == nil {
		t.Fatal("expected coverage to fail when assess_listing has no registered finalizer")
	}
	if !strings.Contains(err.Error(), "assess_listing") {
		t.Fatalf("expected the error to name the uncovered task key, got %v", err)
	}
}

func TestValidateCoverageIgnoresDisabledDefinitions(t *testing.T) {
	app := testsupport.NewMigratedApp(t)
	testsupport.SaveRecord(t, app, "ai_task_definitions", "task00000000001", map[string]any{
		"key": "retired_task", "enabled": false,
	})
	registry := finalizers.NewRegistry()
	_ = registry.Register("assess_listing", noopFinalizer) // retired_task deliberately left unregistered

	if err := finalizers.ValidateCoverage(app, registry); err != nil {
		t.Fatalf("expected a disabled definition to be ignored, got %v", err)
	}
}
