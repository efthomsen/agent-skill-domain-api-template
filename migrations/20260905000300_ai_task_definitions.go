package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		definitions := core.NewBaseCollection("ai_task_definitions")
		definitions.Fields.Add(
			&core.TextField{Name: "key", Required: true},
			&core.BoolField{Name: "enabled"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		definitions.AddIndex("idx_ai_task_definitions_key", true, "`key`", "")
		if err := app.Save(definitions); err != nil {
			return err
		}

		record := core.NewRecord(definitions)
		record.Set("key", "assess_listing")
		record.Set("enabled", true)
		return app.Save(record)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("ai_task_definitions")
		if err != nil {
			return err
		}
		return app.Delete(collection)
	})
}
