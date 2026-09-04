# Agent Skill + Domain API Template

A small, tested reference implementation of a pattern for building agent-operated services: an agent reads a `SKILL.md`, calls a custom **domain API** (not raw database access), backed by **PocketBase used as a Go library**.

## Why this exists

Several internal services follow a similar convention — a skill file, a versioned domain API, a PocketBase backend — but each grew it independently by hand: some on stock PocketBase with JS hook scripts, some as a compiled Go binary, at least one with no domain API layer at all (agents just talk to raw collections). The convention never existed as shared, reusable, *tested* code — only as something each service reinvented and got slightly differently.

That gap is why this repository exists. It extracts the shape those services converge on and gives it real tests, so the mechanics — not just the idea — can be copied into a new service.

The rationale, stated generically:
- Hand-written JS hooks against a live database are hard to test before merge; a compiled Go service can run its request handlers against a real (if throwaway) database in CI.
- A "read the record, decide, write it back" flow without a transaction races under concurrent callers — an agent and a human editing the same record at once can silently clobber each other.
- Agents need a machine-readable way to tell "you retried safely" apart from "you conflicted with someone else" apart from "your session token you were given is no longer valid" — a sentence in an error message doesn't cut it.
- An agent completing a unit of work should hold a narrow, time-boxed lease on that one task, not a superuser credential that can touch everything.

## The three mechanics

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

Mutating a listing requires `expected_version` — the version you last read. If the record changed since then, the write is rejected with `409 {"data": {"code": "version_conflict"}}` instead of silently overwriting someone else's change:

```bash
curl -X POST "$URL/api/template/v1/commands/update-listing" \
  -H "Idempotency-Key: update-listing-$(uuidgen)" -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"listing_id":"'"$ID"'","expected_version":1,"description":"Price reduced"}'
```

This template uses a simple integer counter bumped on every write. A content-hash variant (derive the "version" from a SHA-256 of the record's own data, so two writers who happen to produce identical output don't spuriously conflict) is a reasonable alternative if a service already needs deterministic hashing elsewhere — swap `httpapi.CheckExpectedVersion`'s comparison for one.

### 3. The leased AI-execution loop

A command like `request-assessment` doesn't run an assessment itself — it queues a pending unit of work and returns immediately. An agent then:

1. `POST /ai/executions/claim-next` — leases the oldest pending (or lease-expired) unit of work to itself, or `204` if nothing is waiting.
2. `GET /ai/executions/{id}/context` (with its lease token) — reads the input bundle it needs to do the work.
3. `POST /ai/executions/{id}/result` (with its lease token and the `input_hash` it read) — applies its output and completes the execution.

The lease is exclusive (verified with tests: [`TestConcurrentClaimNextLeasesExactlyOnce`](internal/executions/routes_test.go) fires 16 concurrent claims at one pending execution and asserts exactly one wins), time-boxed (an unfinished lease expires and becomes reclaimable — [`TestExpiredLeaseIsReclaimable`](internal/executions/routes_test.go)), and posting a result is itself idempotent (a repeated submit replays the original outcome rather than re-applying it).

Claiming is a compare-and-swap inside a PocketBase transaction. PocketBase's SQLite writes run on a single-connection pool, so a transaction's read-then-write is fully serialized against every other transaction in the process — no extra locking is needed for this to be race-free within one instance. See [`internal/executions/lease.go`](internal/executions/lease.go).

## Layout

```
main.go                    boots pocketbase.New(), registers routes on OnServe
migrations/                compiled-in Go migrations (no hand-run JS against a live dashboard)
internal/httpapi/          shared conventions: API prefix, idempotency, version checks, error codes
internal/listings/         the example resource + its commands
internal/executions/       the leased AI-execution loop
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

From there, follow the command and claim/context/result examples in [`skill/SKILL.md`](skill/SKILL.md).

## Adopting this in a new service

1. Copy `internal/httpapi`, `internal/testsupport`, and the migration/main.go bootstrap as-is; rename `httpapi.APIPrefix`.
2. Replace `internal/listings` with your own resource package(s), following the same `resources/{resource}` + `commands/{action}` shape.
3. Replace the `assess_listing` task in `internal/executions` with your own `task_key`s if a resource needs AI-driven work; a real adoption with more than one task type will want a small per-`task_key` finalizer registry rather than the one hardcoded branch here.
4. Swap `skill/scripts/auth.sh`'s password flow for an OIDC device-flow + refresh-token-cache script if the service sits behind SSO — the domain API itself doesn't care how the caller authenticated, only that `apis.RequireAuth()` accepted it.

## What this is not

A reference implementation for learning and copying from, not a production service — it isn't deployed anywhere, and no performance numbers are claimed for it.

## Verify it yourself

```bash
go build ./...
go test -race ./...
```
