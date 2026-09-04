#!/bin/sh
# Authenticates against this service's PocketBase "users" auth collection
# with an email/password on file and prints a short-lived token to stdout.
# All human-facing output goes to stderr so the token can be captured with
# TOKEN="$(scripts/auth.sh)".
set -eu

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 1; }

service_url="${TEMPLATE_URL:?TEMPLATE_URL is required, e.g. https://template.example.com}"
email="${TEMPLATE_EMAIL:?TEMPLATE_EMAIL is required}"
password="${TEMPLATE_PASSWORD:?TEMPLATE_PASSWORD is required}"

response="$(
  curl --silent --show-error --fail-with-body \
    --request POST "$service_url/api/collections/users/auth-with-password" \
    --header 'Content-Type: application/json' \
    --header 'Accept: application/json' \
    --data "$(jq -n --arg identity "$email" --arg password "$password" '{identity:$identity,password:$password}')"
)"

printf '%s\n' "$response" | jq -er '.token'
