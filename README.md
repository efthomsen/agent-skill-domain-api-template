# Agent Skill + Domain API Template

A small, tested reference implementation of a pattern for building agent-operated services: an agent reads a `SKILL.md`, calls a custom **domain API** (not raw database access), backed by **PocketBase used as a Go library**.

## Why this exists

Several internal services follow a similar convention (a skill file, a versioned domain API, a PocketBase backend), but each grew it independently by hand: some on stock PocketBase with JS hook scripts, some as a compiled Go binary, at least one with no domain API layer at all (agents just talk to raw collections). The convention never existed as shared, reusable, *tested* code: only as something each service reinvented and got slightly differently.

That gap is why this repository exists. It's meant to do double duty: a **guiding star** for what the pattern should look like when done properly, and **working scaffolding** a new service can actually be bootstrapped from: not a toy that only covers the easy 80%. Concretely, that means it doesn't stop at the mechanics every tutorial covers (idempotency, optimistic concurrency); it also covers the parts that only show up once a service has been in production for a while: a mutation a human has to approve first, a task registry that fails loudly at boot instead of silently at runtime, a real SSO flow, and an audit trail for reads, not just writes.

The rationale, stated generically:
- Hand-written JS hooks against a live database are hard to test before merge; a compiled Go service can run its request handlers against a real (if throwaway) database in CI.
- A "read the record, decide, write it back" flow without a transaction races under concurrent callers: an agent and a human editing the same record at once can silently clobber each other.
- Agents need a machine-readable way to tell "you retried safely" apart from "you conflicted with someone else" apart from "your session token is no longer valid": a sentence in an error message doesn't cut it.
- An agent completing a unit of work should hold a narrow, time-boxed lease on that one task, not a superuser credential that can touch everything.
- Not every mutation should execute unattended: some need a human to see exactly what's about to happen and say yes first.
- A service that adds a new AI task type should fail to *start* if nothing implements it yet, not fail the first time an agent tries to use it.
- Signing in with a corporate identity provider shouldn't silently create an account: linking an identity to an account is a deliberate, separate, authenticated step.

## The mechanics

### 1. Idempotent commands

Every `commands/*` endpoint requires an `Idempotency-Key` header. The first request with a given key runs and its result is stored; replaying the same key with the same body returns the stored result again rather than repeating the side effect. Reusing the key with a *different* body is rejected as a conflict:

```bash
curl -X POST "$URL/api/template/v1/commands/create-listing" \
  -H "Idempotency-Key: create-listing-$(uuidgen)" -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"address":"1600 Amphitheatre Pkwy","asking_price":950000,"description":"Great house"}'
# -> 201 {"listing": {"id": "...", "version": 1, ...}}
# replaying the exact same request returns the same listing.id, not a duplicate
```

See [`internal/httpapi/httpapi.go`](internal/httpapi/httpapi.go)'s `RunIdempotent`, and the replay/conflict tests in [`internal/listings/routes_test.go`](internal/listings/routes_test.go).

### 2. Optimistic concurrency

Mutating a listing requires `expected_version`: the version you last read. If the record changed since then, the write is rejected with `409 {"data": {"code": "version_conflict"}}` instead of silently overwriting someone else's change. This template uses a simple integer counter bumped on every write; deriving the version from a content hash instead is a reasonable alternative if a service already needs deterministic hashing elsewhere.

### 3. Confirmation-gated mutations

Not every command should execute the moment it's called. `commands/delete-listing` is the worked example of a mutation that requires a human's explicit approval first:

```bash
curl -X POST "$URL/api/template/v1/commands/delete-listing" \
  -H "Idempotency-Key: delete-1" -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -d '{"listing_id":"'"$ID"'","expected_version":1}'
# -> 202 {"status":"confirmation_required","confirmation_id":"...","message":"Delete the listing at ...?"}

curl -X POST "$URL/api/template/v1/commands/decide-confirmation" \
  -H "Idempotency-Key: decide-1" -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -d '{"confirmation_id":"...","decision":"approve"}'

# resubmit the ORIGINAL request, same Idempotency-Key, now with confirmation_id added:
curl -X POST "$URL/api/template/v1/commands/delete-listing" \
  -H "Idempotency-Key: delete-1" -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -d '{"listing_id":"'"$ID"'","expected_version":1,"confirmation_id":"..."}'
# -> 200, actually deleted
```

The interesting property isn't the happy path: it's what happens off it. Replaying the original request while still pending returns the same confirmation, never a duplicate. A declined confirmation can't be bypassed by resubmitting. A consumed confirmation can't be reused for a different mutation. And if the resubmit itself fails (say, the record moved on and `expected_version` is now stale), the approval survives: the whole attempt rolls back together, so the human isn't asked to approve the same thing twice just because a retry failed. See `httpapi.RunConfirmable` in [`internal/httpapi/httpapi.go`](internal/httpapi/httpapi.go) for exactly how that's enforced, and [`internal/listings/delete_confirmation_test.go`](internal/listings/delete_confirmation_test.go) for each of those cases as a real test.

### 4. The leased AI-execution loop, with a finalizer registry behind it

A command like `request-assessment` doesn't run an assessment itself: it queues a pending unit of work and returns immediately. An agent then:

1. `POST /ai/executions/claim-next`: leases the oldest pending (or lease-expired) unit of work to itself, or `204` if nothing is waiting.
2. `POST /ai/executions/{id}/context` (with its lease token and its own `Idempotency-Key`): reads the input bundle it needs to do the work.
3. `POST /ai/executions/{id}/result` (with its lease token and the `input_hash` it read): applies its output and completes the execution.

The lease is exclusive (verified with tests: [`TestConcurrentClaimNextLeasesExactlyOnce`](internal/executions/routes_test.go) fires 16 concurrent claims at one pending execution and asserts exactly one wins), time-boxed (an unfinished lease expires and becomes reclaimable; see [`TestExpiredLeaseIsReclaimable`](internal/executions/routes_test.go)), and posting a result is itself idempotent (a repeated submit replays the original outcome rather than re-applying it).

Claiming is a compare-and-swap inside a PocketBase transaction. PocketBase's SQLite writes run on a single-connection pool, so a transaction's read-then-write is fully serialized against every other transaction in the process: no extra locking is needed for this to be race-free within one instance. See [`internal/executions/lease.go`](internal/executions/lease.go).

Completing an execution doesn't hardcode what "completing" means. [`internal/finalizers`](internal/finalizers/registry.go) is a small `task_key -> Finalizer` registry; a resource package (like `listings`) registers what it knows how to finalize, and `main.go` calls `finalizers.ValidateCoverage` at boot (after migrations run, before routes register), which queries every `enabled` row in `ai_task_definitions` and fails startup outright if any of them has no registered finalizer. A live data/code mismatch (someone adds a task type in the database without shipping the Go code for it) is a startup failure, not a runtime gap discovered the first time an agent tries to use it.

### 5. Auditing reads, not just writes

`.../context` above is a `POST`, not a `GET`, and requires its own `Idempotency-Key`, separate from the execution's creation and lease keys. Every read is logged to `execution_context_reads`: replaying the same key returns the exact original bundle without touching the lease again; a *new* key always logs a fresh row, even if the underlying data happens to be byte-identical to a prior read, because the read event itself is what's being audited, not just the data it returned. Reusing a key against a different execution or lease token is a conflict, the same way a mutating command's key is.

### 6. Real SSO, alongside the simple password fallback

[`internal/oidcauth`](internal/oidcauth/routes.go) verifies an OIDC device-flow identity token: it doesn't broker the device flow itself. A client discovers the provider via `GET /auth/oidc/config`, does the actual device-authorization/token dance directly against that provider, and hands the resulting `id_token` to this service:

- `POST /auth/oidc/exchange` verifies the token against the provider's JWKS and, if the identity is already linked to an account, returns a normal PocketBase auth token.
- `POST /auth/oidc/link` (already authenticated, e.g. via the password flow) links a verified identity to the calling account.

**Identities are never auto-provisioned on exchange.** An unlinked identity gets a `403`, not a new account: linking is a deliberate, separate, authenticated step. This is the one property worth preserving faithfully rather than simplifying away; everything else about the flow is intentionally minimal. Leave `TEMPLATE_OIDC_ENABLED` unset and the stock `auth-with-password` flow (via `skill/scripts/auth.sh`) is the only auth path, with zero code changes needed.

## Layout

```
main.go                    boots pocketbase.New(), builds the finalizer registry, registers routes on OnServe
migrations/                compiled-in Go migrations (no hand-run JS against a live dashboard)
internal/httpapi/          shared conventions: API prefix, idempotency, confirmations, version checks, error codes
internal/listings/         the example resource + its commands, including the confirmable delete
internal/confirmations/    inspecting and deciding on pending human-approval gates
internal/executions/       the leased AI-execution loop and the audited context-read ledger
internal/finalizers/       the task_key -> completion-logic registry and its boot-time coverage check
internal/oidcauth/         OIDC identity-token verification and account linking
internal/testsupport/      shared integration-test bootstrap
skill/                     an example SKILL.md + auth.sh for an agent operating this service
```

## Run it locally

```bash
go run . serve
```

Create a user through PocketBase's stock public signup endpoint, then:

```bash
export TEMPLATE_URL=http://127.0.0.1:8090
export TEMPLATE_EMAIL=you@example.com
export TEMPLATE_PASSWORD=your-password
TOKEN="$(skill/scripts/auth.sh)"
```

From there, follow the command, confirmation, and claim/context/result examples in [`skill/SKILL.md`](skill/SKILL.md).

## Adopting this in a new service

1. Copy `internal/httpapi`, `internal/testsupport`, and the migration/main.go bootstrap as-is; rename `httpapi.APIPrefix`.
2. Replace `internal/listings` with your own resource package(s), following the same `resources/{resource}` + `commands/{action}` shape. Use `httpapi.RunIdempotent` for ordinary commands, `httpapi.RunConfirmable` for ones a human should approve first.
3. Replace the `assess_listing` task with your own `task_key`(s): register a `finalizers.Finalizer` for each in `main.go`, and add a matching row to `ai_task_definitions` in a migration.
4. Set the `TEMPLATE_OIDC_*` env vars once the service has a real identity provider to point at; until then the password flow just works.

## What this is not

A reference implementation for learning and copying from, not a production service: it isn't deployed anywhere, and no performance numbers are claimed for it. Two things are deliberately **not** built, and documented here instead as extension points rather than left unmentioned: a discovery/OpenAPI-style metadata endpoint describing the live contract, and a skill-package zip+checksum download endpoint for self-verifying a local skill copy against the deployed one. Both are deployment/distribution tooling, not the domain-API pattern itself.

## Verify it yourself

```bash
go build ./...
go vet ./...
go test -race ./...
```
