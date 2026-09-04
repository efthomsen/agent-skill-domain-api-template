// Package listings is the example resource + command pair: property
// listings a caller owns, plus a command that queues an AI assessment of
// one via the leased execution loop in internal/executions.
package listings

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
)

// RegisterRoutes wires the listings resource and command routes onto rg
// under httpapi.APIPrefix.
func RegisterRoutes(rg *router.Router[*core.RequestEvent]) {
	rg.GET(httpapi.APIPrefix+"/resources/listings", listAction).Bind(apis.RequireAuth())
	rg.GET(httpapi.APIPrefix+"/resources/listings/{id}", getAction).Bind(apis.RequireAuth())
	rg.POST(httpapi.APIPrefix+"/commands/create-listing", createAction).Bind(apis.RequireAuth())
	rg.POST(httpapi.APIPrefix+"/commands/update-listing", updateAction).Bind(apis.RequireAuth())
	rg.POST(httpapi.APIPrefix+"/commands/request-assessment", requestAssessmentAction).Bind(apis.RequireAuth())
}

func listAction(e *core.RequestEvent) error {
	records, err := e.App.FindRecordsByFilter("listings", "user_id = {:user}", "-created", 0, 0,
		map[string]any{"user": httpapi.AuthID(e)})
	if err != nil {
		return err
	}
	items := make([]any, len(records))
	for i, rec := range records {
		items[i] = listingData(rec)
	}
	return e.JSON(http.StatusOK, map[string]any{"listings": items})
}

func getAction(e *core.RequestEvent) error {
	rec, err := findOwnedListing(e.App, e.Request.PathValue("id"), httpapi.AuthID(e))
	if err != nil {
		return apis.NewNotFoundError("", err)
	}
	return e.JSON(http.StatusOK, map[string]any{"listing": listingData(rec)})
}

func createAction(e *core.RequestEvent) error {
	body, err := httpapi.Body(e)
	if err != nil {
		return err
	}
	userID := httpapi.AuthID(e)

	return httpapi.RunIdempotent(e, "create-listing", body, func(tx core.App) (int, map[string]any, error) {
		collection, err := tx.FindCollectionByNameOrId("listings")
		if err != nil {
			return 0, nil, err
		}
		rec := core.NewRecord(collection)
		rec.Set("user_id", userID)
		rec.Set("address", body["address"])
		rec.Set("asking_price", body["asking_price"])
		rec.Set("description", body["description"])
		rec.Set("version", 1)
		if err := tx.Save(rec); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, map[string]any{"listing": listingData(rec)}, nil
	})
}

func updateAction(e *core.RequestEvent) error {
	body, err := httpapi.Body(e)
	if err != nil {
		return err
	}
	userID := httpapi.AuthID(e)
	listingID, _ := body["listing_id"].(string)

	return httpapi.RunIdempotent(e, "update-listing", body, func(tx core.App) (int, map[string]any, error) {
		rec, err := findOwnedListing(tx, listingID, userID)
		if err != nil {
			return 0, nil, apis.NewNotFoundError("", err)
		}
		if err := httpapi.CheckExpectedVersion(body, rec); err != nil {
			return 0, nil, err
		}
		if v, ok := body["address"]; ok {
			rec.Set("address", v)
		}
		if v, ok := body["asking_price"]; ok {
			rec.Set("asking_price", v)
		}
		if v, ok := body["description"]; ok {
			rec.Set("description", v)
		}
		rec.Set("version", rec.GetInt("version")+1)
		if err := tx.Save(rec); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"listing": listingData(rec)}, nil
	})
}

func requestAssessmentAction(e *core.RequestEvent) error {
	body, err := httpapi.Body(e)
	if err != nil {
		return err
	}
	userID := httpapi.AuthID(e)
	listingID, _ := body["listing_id"].(string)

	return httpapi.RunIdempotent(e, "request-assessment", body, func(tx core.App) (int, map[string]any, error) {
		rec, err := findOwnedListing(tx, listingID, userID)
		if err != nil {
			return 0, nil, apis.NewNotFoundError("", err)
		}
		if err := httpapi.CheckExpectedVersion(body, rec); err != nil {
			return 0, nil, err
		}

		input := map[string]any{"listing": listingData(rec)}
		inputHash, err := httpapi.HashCanonical(input)
		if err != nil {
			return 0, nil, err
		}

		executionsCollection, err := tx.FindCollectionByNameOrId("ai_executions")
		if err != nil {
			return 0, nil, err
		}
		execution := core.NewRecord(executionsCollection)
		execution.Set("user_id", userID)
		execution.Set("task_key", "assess_listing")
		execution.Set("target_collection", "listings")
		execution.Set("target_id", rec.Id)
		execution.Set("status", "pending")
		execution.Set("input_json", input)
		execution.Set("input_hash", inputHash)
		if err := tx.Save(execution); err != nil {
			return 0, nil, err
		}

		return http.StatusAccepted, map[string]any{
			"execution_id": execution.Id,
			"input_hash":   inputHash,
		}, nil
	})
}

func findOwnedListing(app core.App, id, userID string) (*core.Record, error) {
	if id == "" {
		return nil, sql.ErrNoRows
	}
	rec, err := app.FindRecordById("listings", id)
	if err != nil {
		return nil, err
	}
	if rec.GetString("user_id") != userID {
		return nil, errors.New("listing not owned by caller")
	}
	return rec, nil
}

func listingData(rec *core.Record) map[string]any {
	return map[string]any{
		"id":           rec.Id,
		"address":      rec.GetString("address"),
		"asking_price": rec.GetInt("asking_price"),
		"description":  rec.GetString("description"),
		"assessment":   rec.GetString("assessment"),
		"version":      rec.GetInt("version"),
	}
}
