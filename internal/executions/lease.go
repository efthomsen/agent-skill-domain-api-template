// Package executions implements the leased AI-execution loop: an agent
// claims the oldest pending (or expired-lease) unit of work, reads its
// context bundle, and posts a result back. Claiming is a compare-and-swap
// inside a PocketBase transaction, which runs on the single-connection
// nonconcurrent pool — so two concurrent claimants can never win the same
// row.
package executions

import (
	"fmt"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/efthomsen/agent-skill-domain-api-template/internal/finalizers"
	"github.com/efthomsen/agent-skill-domain-api-template/internal/httpapi"
)

// Config controls lease behavior and how completed executions are
// finalized. Now is injectable so tests can simulate lease expiry without
// sleeping.
type Config struct {
	LeaseTTL   time.Duration
	Now        func() time.Time
	Finalizers *finalizers.Registry
}

func (c Config) leaseTTL() time.Duration {
	if c.LeaseTTL <= 0 {
		return 30 * time.Minute
	}
	return c.LeaseTTL
}

func (c Config) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

// ClaimNext leases the oldest execution that is pending, or whose previous
// lease has expired, to the caller. It returns (nil, "", nil) when nothing
// is available.
func ClaimNext(app core.App, cfg Config) (*core.Record, string, error) {
	var claimed *core.Record
	var token string

	err := app.RunInTransaction(func(tx core.App) error {
		nowStr := cfg.now().UTC().Format(types.DefaultDateLayout)

		candidates, err := tx.FindRecordsByFilter(
			"ai_executions",
			"status = 'pending' || (status = 'claimed' && lease_expires_at < {:now})",
			"created",
			1, 0,
			dbx.Params{"now": nowStr},
		)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}

		rec := candidates[0]
		token = security.RandomString(64)
		rec.Set("status", "claimed")
		rec.Set("lease_token", token)
		rec.Set("lease_expires_at", cfg.now().Add(cfg.leaseTTL()))
		if err := tx.Save(rec); err != nil {
			return err
		}
		claimed = rec
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return claimed, token, nil
}

// LoadContext returns the input bundle for a claimed execution, provided
// leaseToken matches its live, unexpired lease.
func LoadContext(app core.App, cfg Config, id, leaseToken string) (map[string]any, error) {
	rec, err := app.FindRecordById("ai_executions", id)
	if err != nil {
		return nil, err
	}
	if err := verifyLease(rec, leaseToken, cfg); err != nil {
		return nil, err
	}
	return map[string]any{
		"execution_id": rec.Id,
		"input":        httpapi.JSONField(rec, "input_json", map[string]any{}),
		"input_hash":   rec.GetString("input_hash"),
	}, nil
}

// SubmitResult applies an agent's output to the execution's target record
// and marks the execution completed. It is a compare-and-swap: the lease
// must still be live and the input_hash must match what was leased.
// Re-submitting an already-completed execution replays its stored result
// rather than re-applying the output.
func SubmitResult(app core.App, cfg Config, id, leaseToken, inputHash string, output map[string]any) (map[string]any, error) {
	var result map[string]any

	err := app.RunInTransaction(func(tx core.App) error {
		rec, err := tx.FindRecordById("ai_executions", id)
		if err != nil {
			return err
		}

		if rec.GetString("status") == "completed" {
			result, _ = httpapi.JSONField(rec, "result_json", map[string]any{}).(map[string]any)
			return nil
		}

		if err := verifyLease(rec, leaseToken, cfg); err != nil {
			return err
		}
		if rec.GetString("input_hash") != inputHash {
			return httpapi.InputChangedError()
		}

		finalize, ok := cfg.Finalizers.Lookup(rec.GetString("task_key"))
		if !ok {
			return fmt.Errorf("no registered finalizer for task key %q", rec.GetString("task_key"))
		}
		finalizeResult, err := finalize(tx, rec, output)
		if err != nil {
			return err
		}

		result = finalizeResult
		rec.Set("output_json", output)
		rec.Set("result_json", result)
		rec.Set("status", "completed")
		rec.Set("lease_token", "")
		rec.Set("completed_at", cfg.now())
		if err := tx.Save(rec); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func verifyLease(rec *core.Record, leaseToken string, cfg Config) error {
	if leaseToken == "" || rec.GetString("status") != "claimed" || rec.GetString("lease_token") != leaseToken {
		return httpapi.LeaseInvalidError()
	}
	if rec.GetDateTime("lease_expires_at").Time().Before(cfg.now()) {
		return httpapi.LeaseInvalidError()
	}
	return nil
}
