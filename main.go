// Command agent-skill-domain-api-template runs the reference PocketBase
// service: a tiny "listings" resource, its commands, and the leased
// AI-execution loop an agent uses to work through them.
package main

import (
	"log"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	"github.com/pocketbase/pocketbase/tools/hook"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/confirmations"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/executions"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/finalizers"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/listings"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/oidcauth"
	_ "github.com/efthomsen/agent-skill-domain-api-template/migrations"
)

func main() {
	app := pocketbase.New()

	// Automigrate off: schema changes are reviewed Go migrations, not
	// files auto-written from dashboard edits.
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		TemplateLang: migratecmd.TemplateLangGo,
		Automigrate:  false,
		Dir:          "migrations",
	})

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			// App-defined collections only exist once RunAllMigrations has
			// run, which happens earlier in apis.Serve — but still before
			// this hook — so it's safe to query them here, not earlier.
			registry := finalizers.NewRegistry()
			if err := listings.RegisterFinalizers(registry); err != nil {
				return err
			}
			if err := finalizers.ValidateCoverage(e.App, registry); err != nil {
				return err
			}

			listings.RegisterRoutes(e.Router)
			confirmations.RegisterRoutes(e.Router)
			executions.RegisterRoutes(e.Router, executions.Config{Finalizers: registry})
			oidcauth.RegisterRoutes(e.Router, oidcauth.LoadConfig())
			return e.Next()
		},
		Priority: 999,
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
