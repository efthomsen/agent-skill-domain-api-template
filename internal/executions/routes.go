package executions

import (
	"net/http"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
)

// RegisterRoutes wires the leased AI-execution loop onto rg under
// httpapi.APIPrefix: claim-next, read context, post a result.
func RegisterRoutes(rg *router.Router[*core.RequestEvent], cfg Config) {
	rg.POST(httpapi.APIPrefix+"/ai/executions/claim-next", claimNextAction(cfg)).Bind(apis.RequireAuth())
	rg.GET(httpapi.APIPrefix+"/ai/executions/{id}/context", contextAction(cfg)).Bind(apis.RequireAuth())
	rg.POST(httpapi.APIPrefix+"/ai/executions/{id}/result", resultAction(cfg)).Bind(apis.RequireAuth())
}

func claimNextAction(cfg Config) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		rec, token, err := ClaimNext(e.App, cfg)
		if err != nil {
			return err
		}
		if rec == nil {
			return e.NoContent(http.StatusNoContent)
		}
		return e.JSON(http.StatusOK, map[string]any{
			"execution":   executionData(rec),
			"lease_token": token,
		})
	}
}

func contextAction(cfg Config) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		id := e.Request.PathValue("id")
		leaseToken := httpapi.Header(e, "X-Lease-Token")
		ctx, err := LoadContext(e.App, cfg, id, leaseToken)
		if err != nil {
			return err
		}
		return e.JSON(http.StatusOK, ctx)
	}
}

func resultAction(cfg Config) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		body, err := httpapi.Body(e)
		if err != nil {
			return err
		}
		id := e.Request.PathValue("id")
		leaseToken, _ := body["lease_token"].(string)
		inputHash, _ := body["input_hash"].(string)
		output, _ := body["output"].(map[string]any)

		result, err := SubmitResult(e.App, cfg, id, leaseToken, inputHash, output)
		if err != nil {
			return err
		}
		return e.JSON(http.StatusOK, map[string]any{"result": result})
	}
}

func executionData(rec *core.Record) map[string]any {
	return map[string]any{
		"id":                rec.Id,
		"task_key":          rec.GetString("task_key"),
		"target_collection": rec.GetString("target_collection"),
		"target_id":         rec.GetString("target_id"),
		"status":            rec.GetString("status"),
		"input_hash":        rec.GetString("input_hash"),
	}
}
