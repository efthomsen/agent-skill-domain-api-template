---
name: manage-template-service
description: Operate the reference agent-skill-domain-api-template PocketBase service through its domain API. Use when working listings (create, read, update) or when acting as the AI runner that claims and completes queued assessment work via the leased execution loop.
---

# Manage Template Service

This service tracks property listings and lets an agent claim and complete "assess a listing" work through a leased execution loop. It exists to be copied: it is the minimal, tested shape a new service's domain API should take, not a service meant to run in production as-is.

## Start

Set `TEMPLATE_URL`, `TEMPLATE_EMAIL`, `TEMPLATE_PASSWORD`, then get a token:

```bash
TOKEN="$(TEMPLATE_URL=https://template.example.com \
  TEMPLATE_EMAIL=agent@example.com \
  TEMPLATE_PASSWORD=changeme \
  scripts/auth.sh)"
```

The token is short-lived. Re-run `scripts/auth.sh` when a call starts returning 401.

## Non-negotiable operating rules

- Every `commands/*` call requires an `Idempotency-Key` header. Reuse the same key only when retrying the exact same request — a different body with a reused key is rejected as a conflict (`data.code: idempotency_conflict`), not silently applied.
- Every mutating command that targets an existing record takes `expected_version`, the version you last read. A stale value is rejected (`data.code: version_conflict`) rather than silently overwritten — re-read the record and retry with the current version.
- Never persist the PocketBase token to disk; re-authenticate per session.

## Read resources

```bash
curl -s "$TEMPLATE_URL/api/template/v1/resources/listings" \
  -H "Authorization: Bearer $TOKEN"

curl -s "$TEMPLATE_URL/api/template/v1/resources/listings/$LISTING_ID" \
  -H "Authorization: Bearer $TOKEN"
```

## Run commands

```bash
curl -s -X POST "$TEMPLATE_URL/api/template/v1/commands/create-listing" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: create-listing-$(uuidgen)" \
  -d '{"address":"1600 Amphitheatre Pkwy","asking_price":950000,"description":"Great house"}'

curl -s -X POST "$TEMPLATE_URL/api/template/v1/commands/update-listing" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: update-listing-$(uuidgen)" \
  -d '{"listing_id":"'"$LISTING_ID"'","expected_version":1,"description":"Price reduced"}'

curl -s -X POST "$TEMPLATE_URL/api/template/v1/commands/request-assessment" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: request-assessment-$(uuidgen)" \
  -d '{"listing_id":"'"$LISTING_ID"'","expected_version":1}'
```

`request-assessment` returns `{"execution_id": "...", "input_hash": "..."}` and queues a pending unit of AI work — it does not run the assessment itself.

## Claim and execute AI work

```bash
CLAIM="$(curl -s -w '\n%{http_code}' -X POST \
  "$TEMPLATE_URL/api/template/v1/ai/executions/claim-next" \
  -H "Authorization: Bearer $TOKEN")"
CLAIM_STATUS="$(tail -n1 <<<"$CLAIM")"
CLAIM_BODY="$(sed '$d' <<<"$CLAIM")"

if [ "$CLAIM_STATUS" = "204" ]; then
  echo "no work waiting"
else
  EXECUTION_ID="$(jq -r '.execution.id' <<<"$CLAIM_BODY")"
  LEASE_TOKEN="$(jq -r '.lease_token' <<<"$CLAIM_BODY")"

  CONTEXT="$(curl -s "$TEMPLATE_URL/api/template/v1/ai/executions/$EXECUTION_ID/context" \
    -H "Authorization: Bearer $TOKEN" -H "X-Lease-Token: $LEASE_TOKEN")"
  INPUT_HASH="$(jq -r '.input_hash' <<<"$CONTEXT")"

  # ... read $CONTEXT, decide on an assessment ...

  curl -s -X POST "$TEMPLATE_URL/api/template/v1/ai/executions/$EXECUTION_ID/result" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"lease_token":"'"$LEASE_TOKEN"'","input_hash":"'"$INPUT_HASH"'","output":{"assessment":"Fairly priced for the area."}}'
fi
```

A lease is exclusive and expires after 30 minutes if not completed, at which point another `claim-next` call can reclaim it. Posting a result twice with the same execution is safe — the second call replays the original outcome rather than reapplying it.

## Errors and receipts

Every conflict has a stable `data.code` in the response body — branch on that, not on `message` (which is a human-readable sentence and may be reworded):

| `data.code` | status | meaning |
|---|---|---|
| `idempotency_conflict` | 409 | the `Idempotency-Key` was already used with a different request body |
| `version_conflict` | 409 | `expected_version` no longer matches the record; re-read and retry |
| `lease_invalid` | 403 | the lease token is missing, wrong, or the execution isn't in a claimed state |
| `input_changed` | 409 | the submitted `input_hash` no longer matches what was leased |
