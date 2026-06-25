#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ADDR="${MERCATOR_SMOKE_ADDR:-127.0.0.1:$((18080 + RANDOM % 1000))}"
API_URL="http://${ADDR}"
WORKSPACE_ID="${MERCATOR_SMOKE_WORKSPACE_ID:-ws_smoke}"
TOKEN="${MERCATOR_SMOKE_API_TOKEN:-dev-smoke-token}"
FAKE_OFFER="${MERCATOR_SMOKE_FAKE_OFFER:-1}"
TMPDIR_ROOT="${TMPDIR:-/tmp}"
WORK_DIR="$(mktemp -d "${TMPDIR_ROOT%/}/mercator-smoke.XXXXXX")"
BIN="${WORK_DIR}/mercator"
SERVER_LOG="${WORK_DIR}/server.log"
DB_DSN="file:${WORK_DIR}/mercator.db"
SERVER_PID=""

fail() {
  echo "smoke-test-fake: $*" >&2
  if [[ -f "${SERVER_LOG}" ]]; then
    echo "--- server log ---" >&2
    cat "${SERVER_LOG}" >&2
  fi
  exit 1
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "missing required command: $1"
  fi
}

cleanup() {
  if [[ -n "${SERVER_PID}" ]] && kill -0 "${SERVER_PID}" >/dev/null 2>&1; then
    kill "${SERVER_PID}" >/dev/null 2>&1 || true
    wait "${SERVER_PID}" >/dev/null 2>&1 || true
  fi
  if [[ "${MERCATOR_SMOKE_KEEP:-}" == "1" ]]; then
    echo "kept smoke-test artifacts in ${WORK_DIR}" >&2
  else
    rm -rf "${WORK_DIR}"
  fi
}
trap cleanup EXIT

need curl
need go
need jq

cd "${ROOT}"
go build -o "${BIN}" ./cmd/mercator

(
  export MERCATOR_ADDR="${ADDR}"
  export MERCATOR_SQLITE_DSN="${DB_DSN}"
  export MERCATOR_API_TOKEN="${TOKEN}"
  export MERCATOR_AUTH_WORKSPACES="${WORKSPACE_ID}"
  export MERCATOR_FAKE_OFFER="${FAKE_OFFER}"
  exec "${BIN}" serve
) >"${SERVER_LOG}" 2>&1 &
SERVER_PID="$!"

ready=0
for _ in $(seq 1 100); do
  if ! kill -0 "${SERVER_PID}" >/dev/null 2>&1; then
    fail "server exited before becoming ready"
  fi
  if curl -fsS "${API_URL}/health/ready" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.1
done

if [[ "${ready}" != "1" ]]; then
  fail "server did not become ready at ${API_URL}/health/ready"
fi

export MERCATOR_API_URL="${API_URL}"
export MERCATOR_API_TOKEN="${TOKEN}"
export MERCATOR_WORKSPACE_ID="${WORKSPACE_ID}"

create_json="$("${BIN}" run create busybox -- echo hi)"
run_id="$(printf '%s\n' "${create_json}" | jq -er '.run.id')"

run_json="$("${BIN}" run get --run-id "${run_id}")"
printf '%s\n' "${run_json}" | RUN_ID="${run_id}" jq -e '
  .run.id == env.RUN_ID and
  .run.outcome == "succeeded" and
  .run.exit_code == 0 and
  .run.cleanup == "confirmed" and
  .run.closed == true
' >/dev/null || fail "run did not close with the expected fake-adapter result"

events_json="$("${BIN}" run events --run-id "${run_id}")"
printf '%s\n' "${events_json}" | jq -e '(.events | length) > 0' >/dev/null ||
  fail "run events were empty"

decision_json="$("${BIN}" run decision --run-id "${run_id}")"
printf '%s\n' "${decision_json}" | jq -e '(.decision.selected_offer_snapshot_id | type == "string" and length > 0)' >/dev/null ||
  fail "placement decision did not record a selected offer"

printf 'Mercator fake-adapter smoke test passed\n'
printf '%s\n' "${run_json}" |
  jq -r '"run_id=\(.run.id) outcome=\(.run.outcome) exit_code=\(.run.exit_code) cleanup=\(.run.cleanup) closed=\(.run.closed)"'
printf 'console=%s/?workspace_id=%s\n' "${API_URL}" "${WORKSPACE_ID}"
