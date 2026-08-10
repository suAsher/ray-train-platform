#!/usr/bin/env bash
set -Eeuo pipefail

API_URL="${API_URL:?set API_URL}"
ACCESS_TOKEN="${ACCESS_TOKEN:-}"
ALLOW_ANONYMOUS="${ALLOW_ANONYMOUS:-false}"
if [[ -z "$ACCESS_TOKEN" && "$ALLOW_ANONYMOUS" != "true" ]]; then
  printf 'set ACCESS_TOKEN, or explicitly set ALLOW_ANONYMOUS=true for the isolated demo profile\n' >&2
  exit 1
fi
AUTH_ARGS=()
if [[ -n "$ACCESS_TOKEN" ]]; then
  AUTH_ARGS=(-H "Authorization: Bearer $ACCESS_TOKEN")
fi
need() { command -v "$1" >/dev/null 2>&1 || { printf 'missing command: %s\n' "$1" >&2; exit 1; }; }
need curl
need jq

curl --fail --silent --show-error "$API_URL/readyz" >/dev/null
workspace="$(curl --fail --silent --show-error -X POST "$API_URL/api/v1/dev-workspaces" "${AUTH_ARGS[@]}" -H 'Content-Type: application/json' -d '{}')"
workspace_id="$(jq -r '.data.id' <<<"$workspace")"
[[ -n "$workspace_id" && "$workspace_id" != null ]] || { printf '%s\n' "$workspace" >&2; exit 1; }
curl --fail --silent --show-error "$API_URL/api/v1/dev-workspaces/me" "${AUTH_ARGS[@]}" | jq -e --arg id "$workspace_id" '.data.id == $id' >/dev/null
printf 'workspace accepted: %s\n' "$workspace_id"
curl --fail --silent --show-error -X DELETE "$API_URL/api/v1/dev-workspaces/me" "${AUTH_ARGS[@]}" >/dev/null
printf 'workspace stop accepted\n'
