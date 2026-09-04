// Package migrations defines the collections this template's domain API
// runs against. Each migration is compiled into the binary (not run as
// hand-edited JS against a live dashboard), so the schema is versioned and
// reviewable like any other Go change.
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		listings := core.NewBaseCollection("listings")
		listings.Fields.Add(
			&core.TextField{Name: "user_id", Required: true},
			&core.TextField{Name: "address", Required: true, Max: 500},
			&core.NumberField{Name: "asking_price", OnlyInt: true},
			&core.TextField{Name: "description", Max: 50000},
			&core.TextField{Name: "assessment", Max: 50000},
			&core.NumberField{Name: "version", OnlyInt: true, Required: true},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		listings.AddIndex("idx_listings_user", false, "`user_id`", "")
		if err := app.Save(listings); err != nil {
			return err
		}

		apiCommands := core.NewBaseCollection("api_commands")
		apiCommands.Fields.Add(
			&core.TextField{Name: "user_id", Required: true},
			&core.TextField{Name: "idempotency_key", Required: true, Max: 180},
			&core.TextField{Name: "action", Required: true},
			&core.TextField{Name: "request_hash", Max: 64},
			&core.TextField{Name: "status", Required: true},
			&core.NumberField{Name: "response_status", OnlyInt: true},
			&core.JSONField{Name: "response_json", MaxSize: 2 << 20},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		apiCommands.AddIndex("idx_api_commands_idem", true, "`user_id`, `idempotency_key`", "")
		if err := app.Save(apiCommands); err != nil {
			return err
		}

		aiExecutions := core.NewBaseCollection("ai_executions")
		aiExecutions.Fields.Add(
			&core.TextField{Name: "user_id", Required: true},
			&core.TextField{Name: "task_key", Required: true},
			&core.TextField{Name: "target_collection", Required: true},
			&core.TextField{Name: "target_id", Required: true},
			&core.TextField{Name: "status", Required: true},
			&core.JSONField{Name: "input_json", MaxSize: 2 << 20},
			&core.TextField{Name: "input_hash", Max: 64},
			&core.JSONField{Name: "output_json", MaxSize: 2 << 20},
			&core.JSONField{Name: "result_json", MaxSize: 2 << 20},
			&core.TextField{Name: "lease_token", Max: 64},
			&core.DateField{Name: "lease_expires_at"},
			&core.DateField{Name: "completed_at"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		aiExecutions.AddIndex("idx_ai_executions_status_created", false, "`status`, `created`", "")
		aiExecutions.AddIndex("idx_ai_executions_lease", true, "`lease_token`", "`lease_token` != ''")
		if err := app.Save(aiExecutions); err != nil {
			return err
		}

		return nil
	}, func(app core.App) error {
		for _, name := range []string{"ai_executions", "api_commands", "listings"} {
			collection, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if err := app.Delete(collection); err != nil {
				return err
			}
		}
		return nil
	})
}
