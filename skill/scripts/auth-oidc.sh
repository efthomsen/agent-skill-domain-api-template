#!/bin/sh
# Authenticates via the identity provider named by this service's own
# /auth/oidc/config, then exchanges the resulting id_token for a
# PocketBase token. The device-authorization/token dance runs directly
# against the provider, never against this service — this service only
# ever sees the id_token at the very end, via /auth/oidc/exchange.
#
# Only the refresh token is ever cached to disk; the id/access tokens and
# the final PocketBase token are never persisted. All human-facing output
# goes to stderr so the token can be captured with TOKEN="$(auth-oidc.sh)".
set -eu

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 1; }

service_url="${TEMPLATE_URL:?TEMPLATE_URL is required, e.g. https://template.example.com}"
config_root="${XDG_CONFIG_HOME:-${HOME:?HOME is required}/.config}"
token_file="${TEMPLATE_OIDC_TOKEN_FILE:-$config_root/agent-skill-domain-api-template/oidc.json}"
token_dir="$(dirname "$token_file")"

oidc_config="$(
  curl --silent --show-error --fail-with-body \
    "$service_url/api/template/v1/auth/oidc/config" \
    --header 'Accept: application/json'
)"
if [ "$(printf '%s' "$oidc_config" | jq -r '.enabled // false')" != "true" ]; then
  echo "SSO is not enabled for this deployment; use scripts/auth.sh instead." >&2
  exit 1
fi

issuer="$(printf '%s' "$oidc_config" | jq -er '.issuer')"
client_id="$(printf '%s' "$oidc_config" | jq -er '.cli_client_id')"
token_endpoint="$(printf '%s' "$oidc_config" | jq -er '.token_endpoint')"
device_endpoint="$(printf '%s' "$oidc_config" | jq -er '.device_authorization_endpoint')"
scope="$(printf '%s' "$oidc_config" | jq -r '(.scopes // ["openid","profile","email"]) | join(" ")')"

refresh_token=''
if [ -r "$token_file" ] && jq -e --arg issuer "$issuer" --arg client "$client_id" \
  '.issuer == $issuer and .client_id == $client and (.refresh_token | type == "string" and length > 0)' \
  "$token_file" >/dev/null 2>&1; then
  refresh_token="$(jq -r '.refresh_token' "$token_file")"
fi

token_response=''
if [ -n "$refresh_token" ]; then
  token_response="$(
    curl --silent --show-error \
      --request POST "$token_endpoint" \
      --header 'Accept: application/json' \
      --header 'Content-Type: application/x-www-form-urlencoded' \
      --data-urlencode 'grant_type=refresh_token' \
      --data-urlencode "client_id=$client_id" \
      --data-urlencode "refresh_token=$refresh_token"
  )"
  if [ -n "$(printf '%s' "$token_response" | jq -r '.error // empty')" ] || \
     [ -z "$(printf '%s' "$token_response" | jq -r '.id_token // empty')" ]; then
    token_response=''
  fi
fi

if [ -z "$token_response" ]; then
  device_response="$(
    curl --silent --show-error --fail-with-body \
      --request POST "$device_endpoint" \
      --header 'Accept: application/json' \
      --header 'Content-Type: application/x-www-form-urlencoded' \
      --data-urlencode "client_id=$client_id" \
      --data-urlencode "scope=$scope"
  )"
  device_code="$(printf '%s' "$device_response" | jq -er '.device_code')"
  verification_uri="$(printf '%s' "$device_response" | jq -er '.verification_uri_complete // .verification_uri')"
  user_code="$(printf '%s' "$device_response" | jq -r '.user_code // empty')"
  interval="$(printf '%s' "$device_response" | jq -r '.interval // 5')"
  expires_in="$(printf '%s' "$device_response" | jq -r '.expires_in // 600')"
  started_at="$(date +%s)"
  echo "Open $verification_uri" >&2
  if [ -n "$user_code" ]; then echo "Code: $user_code" >&2; fi

  while :; do
    sleep "$interval"
    token_response="$(
      curl --silent --show-error \
        --request POST "$token_endpoint" \
        --header 'Accept: application/json' \
        --header 'Content-Type: application/x-www-form-urlencoded' \
        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:device_code' \
        --data-urlencode "client_id=$client_id" \
        --data-urlencode "device_code=$device_code"
    )"
    token_error="$(printf '%s' "$token_response" | jq -r '.error // empty')"
    case "$token_error" in
      '') break ;;
      authorization_pending) ;;
      slow_down) interval=$((interval + 5)) ;;
      *)
        detail="$(printf '%s' "$token_response" | jq -r '.error_description // .error')"
        echo "Device sign-in failed: $detail" >&2
        exit 1
        ;;
    esac
    if [ "$(($(date +%s) - started_at))" -ge "$expires_in" ]; then
      echo "Device sign-in expired. Run the command again." >&2
      exit 1
    fi
  done
fi

id_token="$(printf '%s' "$token_response" | jq -er '.id_token')"
new_refresh_token="$(printf '%s' "$token_response" | jq -r '.refresh_token // empty')"
if [ -z "$new_refresh_token" ]; then new_refresh_token="$refresh_token"; fi
if [ -n "$new_refresh_token" ]; then
  mkdir -p "$token_dir"
  chmod 700 "$token_dir"
  token_tmp="$(mktemp "$token_dir/.oidc.XXXXXX")"
  trap 'rm -f "$token_tmp"' EXIT HUP INT TERM
  jq -n --arg refresh_token "$new_refresh_token" --arg issuer "$issuer" --arg client_id "$client_id" \
    '{refresh_token:$refresh_token,issuer:$issuer,client_id:$client_id}' > "$token_tmp"
  chmod 600 "$token_tmp"
  mv "$token_tmp" "$token_file"
  trap - EXIT HUP INT TERM
fi

jq -n --arg id_token "$id_token" '{id_token:$id_token}' |
curl --silent --show-error --fail-with-body \
  --request POST "$service_url/api/template/v1/auth/oidc/exchange" \
  --header 'Accept: application/json' \
  --header 'Content-Type: application/json' \
  --data-binary @- |
jq -er '.token'
