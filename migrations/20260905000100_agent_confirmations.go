package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		confirmations := core.NewBaseCollection("agent_confirmations")
		confirmations.Fields.Add(
			&core.TextField{Name: "user_id", Required: true},
			&core.TextField{Name: "idempotency_key", Required: true, Max: 180},
			&core.TextField{Name: "action", Required: true},
			&core.TextField{Name: "request_hash", Max: 64},
			&core.TextField{Name: "risk_tier", Required: true},
			&core.TextField{Name: "prompt", Required: true, Max: 2000},
			&core.JSONField{Name: "proposed_change", MaxSize: 2 << 20},
			&core.TextField{Name: "status", Required: true},
			&core.DateField{Name: "decided_at"},
			&core.DateField{Name: "consumed_at"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		confirmations.AddIndex("idx_agent_confirmations_idem", true, "`user_id`, `idempotency_key`", "")
		return app.Save(confirmations)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("agent_confirmations")
		if err != nil {
			return err
		}
		return app.Delete(collection)
	})
}
