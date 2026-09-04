// Package httpapi holds the small set of conventions every resource and
// command handler in this service shares: the API prefix, reading the
// authenticated caller, canonical-JSON hashing for idempotency comparisons,
// and the machine-readable error codes agents branch on.
package httpapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// APIPrefix is the route prefix every handler in this service registers
// under. A real adoption renames this to its own service.
const APIPrefix = "/api/template/v1"

// AuthID returns the id of the authenticated caller, or "" if unauthenticated.
func AuthID(e *core.RequestEvent) string {
	if e.Auth == nil {
		return ""
	}
	return e.Auth.Id
}

// Header reads a request header by name.
func Header(e *core.RequestEvent, key string) string {
	return e.Request.Header.Get(key)
}

// Body returns the parsed JSON request body as a map, matching what
// PocketBase's own filter/rule resolvers see via RequestInfo().
func Body(e *core.RequestEvent) (map[string]any, error) {
	info, err := e.RequestInfo()
	if err != nil {
		return nil, err
	}
	return info.Body, nil
}

// HashCanonical returns the hex SHA-256 of v's canonical JSON encoding.
// encoding/json already sorts map keys on marshal, so two maps with the
// same content in a different key order hash identically.
func HashCanonical(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// codedError builds an ApiError whose Data carries a stable machine-readable
// "code" field. Building it directly (rather than via apis.NewApiError)
// avoids PocketBase's safeErrorsData validation-error normalization, which
// would otherwise flatten a plain map[string]any code into a generic
// "validation_invalid_value" placeholder.
func codedError(status int, code, message string) *router.ApiError {
	return &router.ApiError{
		Status:  status,
		Message: message,
		Data:    map[string]any{"code": code},
	}
}

// VersionConflictError is returned when a command's expected_version no
// longer matches the target record.
func VersionConflictError() *router.ApiError {
	return codedError(http.StatusConflict, "version_conflict", "The record changed since you last read it.")
}

// IdempotencyConflictError is returned when an Idempotency-Key is reused
// with a request body that hashes differently from the original.
func IdempotencyConflictError() *router.ApiError {
	return codedError(http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different request.")
}

// LeaseInvalidError is returned when a lease token is missing, wrong, or
// its execution is not in a claimed state.
func LeaseInvalidError() *router.ApiError {
	return codedError(http.StatusForbidden, "lease_invalid", "The execution lease is invalid or has expired.")
}

// InputChangedError is returned when a submitted result's input_hash no
// longer matches what was leased.
func InputChangedError() *router.ApiError {
	return codedError(http.StatusConflict, "input_changed", "The input changed since the execution was claimed.")
}

// IdentityInvalidError is returned when an OIDC identity token fails
// verification (bad signature, wrong issuer/audience, or expired).
func IdentityInvalidError() *router.ApiError {
	return codedError(http.StatusUnauthorized, "identity_invalid", "The identity token is invalid or expired.")
}

// IdentityUnlinkedError is returned when a verified OIDC identity has no
// linked account on this service yet. Identities are never auto-provisioned
// on exchange; a caller must link one explicitly first via an authenticated
// session.
func IdentityUnlinkedError() *router.ApiError {
	return codedError(http.StatusForbidden, "identity_unlinked", "This identity is not linked to an account. Sign in another way first, then link it.")
}

// IdentityAlreadyLinkedError is returned when linking would conflict with
// an existing link — either this identity already points at a different
// account, or the calling account already has a different identity linked.
func IdentityAlreadyLinkedError() *router.ApiError {
	return codedError(http.StatusConflict, "identity_already_linked", "This identity (or this account) is already linked to a different party.")
}

// ConfirmationDeclinedError is returned when a caller tries to proceed
// past a confirmation a human has already declined.
func ConfirmationDeclinedError() *router.ApiError {
	return codedError(http.StatusConflict, "confirmation_declined", "This action was declined and cannot be resubmitted as-is.")
}

// ConfirmationAlreadyDecidedError is returned when a caller tries to
// decide a confirmation that already has a different, terminal decision.
func ConfirmationAlreadyDecidedError() *router.ApiError {
	return codedError(http.StatusConflict, "confirmation_already_decided", "This confirmation already has a different decision.")
}

// CheckExpectedVersion enforces optimistic concurrency: if body carries an
// expected_version, it must match rec's current version. A missing
// expected_version skips the check.
func CheckExpectedVersion(body map[string]any, rec *core.Record) error {
	raw, ok := body["expected_version"]
	if !ok || raw == nil {
		return nil
	}
	expected, ok := raw.(float64)
	if !ok {
		return nil
	}
	if int(expected) != rec.GetInt("version") {
		return VersionConflictError()
	}
	return nil
}

// JSONField decodes a JSON-typed field's raw stored string into a generic
// value, falling back to the supplied default on any decode failure.
func JSONField(record *core.Record, name string, fallback any) any {
	raw := record.GetString(name)
	if raw == "" {
		return fallback
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return fallback
	}
	return decoded
}

// RunIdempotent enforces the Idempotency-Key contract for a command: the
// header is required; a first-seen key runs fn and stores its response;
// a replayed key with an identical body returns the stored response
// unchanged; a replayed key with a different body is a conflict. fn runs
// inside the same transaction as the bookkeeping row, so any error it
// returns rolls back both — a failed command is not idempotently cached.
// On success it writes the JSON response itself.
func RunIdempotent(e *core.RequestEvent, action string, body map[string]any, fn func(tx core.App) (status int, response map[string]any, err error)) error {
	key := Header(e, "Idempotency-Key")
	if key == "" {
		return apis.NewBadRequestError("Idempotency-Key header is required.", nil)
	}
	userID := AuthID(e)

	requestHash, err := HashCanonical(body)
	if err != nil {
		return err
	}

	var status int
	var response map[string]any

	txErr := e.App.RunInTransaction(func(tx core.App) error {
		existing, ferr := tx.FindFirstRecordByFilter(
			"api_commands",
			"user_id = {:user} && idempotency_key = {:key}",
			dbx.Params{"user": userID, "key": key},
		)
		if ferr != nil && !errors.Is(ferr, sql.ErrNoRows) {
			return ferr
		}
		if existing != nil {
			if existing.GetString("request_hash") != requestHash {
				return IdempotencyConflictError()
			}
			status = existing.GetInt("response_status")
			if decoded, ok := JSONField(existing, "response_json", nil).(map[string]any); ok {
				response = decoded
			}
			return nil
		}

		commandsCollection, cerr := tx.FindCollectionByNameOrId("api_commands")
		if cerr != nil {
			return cerr
		}
		commandRecord := core.NewRecord(commandsCollection)
		commandRecord.Set("user_id", userID)
		commandRecord.Set("idempotency_key", key)
		commandRecord.Set("action", action)
		commandRecord.Set("request_hash", requestHash)
		commandRecord.Set("status", "in_progress")
		if serr := tx.Save(commandRecord); serr != nil {
			return serr
		}

		fnStatus, fnResponse, fnErr := fn(tx)
		if fnErr != nil {
			return fnErr
		}

		commandRecord.Set("status", "completed")
		commandRecord.Set("response_status", fnStatus)
		commandRecord.Set("response_json", fnResponse)
		if serr := tx.Save(commandRecord); serr != nil {
			return serr
		}

		status = fnStatus
		response = fnResponse
		return nil
	})
	if txErr != nil {
		return txErr
	}

	return e.JSON(status, response)
}

// NeedsConfirmation creates a pending agent_confirmations row for action
// and returns the response a RunConfirmable command's fn should return on
// its first, unconfirmed call.
func NeedsConfirmation(tx core.App, e *core.RequestEvent, body map[string]any, action, riskTier, prompt string, proposedChange map[string]any) (int, map[string]any, error) {
	requestHash, err := HashCanonical(body)
	if err != nil {
		return 0, nil, err
	}

	confirmationsCollection, err := tx.FindCollectionByNameOrId("agent_confirmations")
	if err != nil {
		return 0, nil, err
	}
	confirmation := core.NewRecord(confirmationsCollection)
	confirmation.Set("user_id", AuthID(e))
	confirmation.Set("idempotency_key", Header(e, "Idempotency-Key"))
	confirmation.Set("action", action)
	confirmation.Set("request_hash", requestHash)
	confirmation.Set("risk_tier", riskTier)
	confirmation.Set("prompt", prompt)
	confirmation.Set("proposed_change", proposedChange)
	confirmation.Set("status", "pending")
	if err := tx.Save(confirmation); err != nil {
		return 0, nil, err
	}

	return http.StatusAccepted, map[string]any{
		"status":          "confirmation_required",
		"confirmation_id": confirmation.Id,
		"message":         prompt,
	}, nil
}

// RunConfirmable enforces the Idempotency-Key contract for a command that
// requires a human's confirmation before it executes.
//
// The first call with a given key runs fn(tx, false), which is expected to
// call NeedsConfirmation and return that response — it must not perform
// the actual mutation. A later call reusing the SAME key, now with a body
// carrying a "confirmation_id" for an *approved* confirmation matching the
// original request, runs fn(tx, true) — the only place true is ever
// passed, so fn never needs to (and must not) trust body["confirmation_id"]
// itself; trust is decided exactly once, centrally, here.
//
// Unlike RunIdempotent's terminal replay, a stored "confirmation_required"
// response is never treated as final: it is re-evaluated on every call
// until a human decides, and replaying the original key+body while still
// pending returns the stored response unchanged without creating a second
// confirmation row. If fn's confirmed call fails (e.g. a stale
// expected_version), the whole transaction rolls back — including the
// confirmation's consumed flip — so it stays approved and retryable
// without asking the human again.
func RunConfirmable(e *core.RequestEvent, action string, body map[string]any, fn func(tx core.App, confirmed bool) (status int, response map[string]any, err error)) error {
	key := Header(e, "Idempotency-Key")
	if key == "" {
		return apis.NewBadRequestError("Idempotency-Key header is required.", nil)
	}
	userID := AuthID(e)

	requestHash, err := HashCanonical(body)
	if err != nil {
		return err
	}

	var status int
	var response map[string]any

	txErr := e.App.RunInTransaction(func(tx core.App) error {
		existing, ferr := tx.FindFirstRecordByFilter(
			"api_commands",
			"user_id = {:user} && idempotency_key = {:key}",
			dbx.Params{"user": userID, "key": key},
		)
		if ferr != nil && !errors.Is(ferr, sql.ErrNoRows) {
			return ferr
		}

		if existing == nil {
			commandsCollection, cerr := tx.FindCollectionByNameOrId("api_commands")
			if cerr != nil {
				return cerr
			}
			commandRecord := core.NewRecord(commandsCollection)
			commandRecord.Set("user_id", userID)
			commandRecord.Set("idempotency_key", key)
			commandRecord.Set("action", action)
			commandRecord.Set("request_hash", requestHash)

			fnStatus, fnResponse, fnErr := fn(tx, false)
			if fnErr != nil {
				return fnErr
			}

			commandRecord.Set("status", "confirmation_required")
			commandRecord.Set("response_status", fnStatus)
			commandRecord.Set("response_json", fnResponse)
			if serr := tx.Save(commandRecord); serr != nil {
				return serr
			}

			status, response = fnStatus, fnResponse
			return nil
		}

		if existing.GetString("status") == "completed" {
			if existing.GetString("request_hash") != requestHash {
				return IdempotencyConflictError()
			}
			status = existing.GetInt("response_status")
			if decoded, ok := JSONField(existing, "response_json", nil).(map[string]any); ok {
				response = decoded
			}
			return nil
		}

		// status == "confirmation_required": re-evaluate against any
		// confirmation_id in this (possibly different) body, but never
		// create a second confirmation for the same original request.
		if confirmationID, _ := body["confirmation_id"].(string); confirmationID != "" {
			confirmation, cerr := tx.FindRecordById("agent_confirmations", confirmationID)
			if cerr == nil &&
				confirmation.GetString("user_id") == userID &&
				confirmation.GetString("idempotency_key") == key &&
				confirmation.GetString("request_hash") == existing.GetString("request_hash") {
				switch confirmation.GetString("status") {
				case "declined":
					return ConfirmationDeclinedError()
				case "approved":
					confirmation.Set("status", "consumed")
					confirmation.Set("consumed_at", time.Now())
					if serr := tx.Save(confirmation); serr != nil {
						return serr
					}

					fnStatus, fnResponse, fnErr := fn(tx, true)
					if fnErr != nil {
						return fnErr
					}

					existing.Set("status", "completed")
					existing.Set("request_hash", requestHash)
					existing.Set("response_status", fnStatus)
					existing.Set("response_json", fnResponse)
					if serr := tx.Save(existing); serr != nil {
						return serr
					}

					status, response = fnStatus, fnResponse
					return nil
				}
			}
		}

		// Still pending (or an unrecognized/mismatched confirmation_id):
		// replay the original confirmation_required response unchanged.
		status = existing.GetInt("response_status")
		if decoded, ok := JSONField(existing, "response_json", nil).(map[string]any); ok {
			response = decoded
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}

	return e.JSON(status, response)
}
