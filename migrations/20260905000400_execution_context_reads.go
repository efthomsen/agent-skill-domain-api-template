package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		reads := core.NewBaseCollection("execution_context_reads")
		reads.Fields.Add(
			&core.TextField{Name: "user_id", Required: true},
			&core.TextField{Name: "execution_id", Required: true},
			&core.TextField{Name: "idempotency_key", Required: true, Max: 180},
			&core.TextField{Name: "lease_token", Max: 64},
			&core.TextField{Name: "request_hash", Max: 64},
			&core.JSONField{Name: "response_json", MaxSize: 2 << 20},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		reads.AddIndex("idx_execution_context_reads_idem", true, "`user_id`, `idempotency_key`", "")
		// Append-only from outside the API: no create/update/delete rule
		// makes it superuser/API-only, matching every other collection here.
		return app.Save(reads)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("execution_context_reads")
		if err != nil {
			return err
		}
		return app.Delete(collection)
	})
}
