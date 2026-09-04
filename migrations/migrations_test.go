package migrations_test

import (
	"testing"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/testsupport"
)

func TestMigrationsCreateExpectedCollections(t *testing.T) {
	app := testsupport.NewMigratedApp(t)

	for _, name := range []string{"listings", "api_commands", "ai_executions", "agent_confirmations", "ai_task_definitions"} {
		if _, err := app.FindCollectionByNameOrId(name); err != nil {
			t.Errorf("expected collection %q to exist after migrations, got error: %v", name, err)
		}
	}
}
