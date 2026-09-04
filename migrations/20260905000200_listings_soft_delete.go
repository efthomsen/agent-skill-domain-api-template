package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("listings")
		if err != nil {
			return err
		}
		if collection.Fields.GetByName("deleted_at") == nil {
			collection.Fields.Add(&core.DateField{Name: "deleted_at"})
		}
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("listings")
		if err != nil {
			return err
		}
		collection.Fields.RemoveByName("deleted_at")
		return app.Save(collection)
	})
}
