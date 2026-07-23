#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 3 ]]; then
  echo "usage: $0 IMAGE@sha256:DIGEST PREVIOUS_VERSION CANDIDATE_SHA" >&2
  exit 2
fi

candidate_image="$1"
previous_version="$2"
candidate_sha="$3"
fixture_dir="internal/daemon/testdata/release-upgrades"
manifest="$fixture_dir/manifest.json"

for command in curl docker jq sqlite3; do
  if ! command -v "$command" >/dev/null; then
    echo "release upgrade qualification requires $command" >&2
    exit 1
  fi
done

if [[ ! "$candidate_image" =~ @sha256:[0-9a-f]{64}$ ]]; then
  echo "candidate image must be immutable by digest, got $candidate_image" >&2
  exit 1
fi
if [[ ! "$candidate_sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "candidate SHA must be a full 40-character Git SHA, got $candidate_sha" >&2
  exit 1
fi

fixture_version="$(jq -er '.release_gate_version' "$manifest")"
final_fixture_version="$(jq -er '.lineage[-1].version' "$manifest")"
if [[ "$fixture_version" != "$final_fixture_version" ]]; then
  echo "release gate version $fixture_version does not match final fixture $final_fixture_version" >&2
  exit 1
fi
if [[ "$fixture_version" != "$previous_version" ]]; then
  echo "release upgrade fixture is $fixture_version, but the previous release is $previous_version" >&2
  echo "capture and sanitize $previous_version state before releasing" >&2
  exit 1
fi
if [[ "$(jq -er '.sanitized' "$manifest")" != "true" ]]; then
  echo "release upgrade fixture manifest is not marked sanitized" >&2
  exit 1
fi

workspace_id="$(jq -er '.workspace_id' "$manifest")"
run_id="$(jq -er '.run_id' "$manifest")"
qualification_root="$(mktemp -d)"
database="$qualification_root/mercator.db"
response="$qualification_root/runs.json"
container_name="mercator-release-upgrade-${candidate_sha:0:12}-${GITHUB_RUN_ID:-local}"
container_running=false

cleanup() {
  status="$?"
  if [[ "$status" -ne 0 ]] && docker inspect "$container_name" >/dev/null 2>&1; then
    docker logs "$container_name" >&2 || true
  fi
  if [[ "$container_running" == "true" ]]; then
    docker stop --time 10 "$container_name" >/dev/null 2>&1 || true
  fi
  rm -rf "$qualification_root"
  exit "$status"
}
trap cleanup EXIT

while IFS= read -r fixture; do
  if [[ ! "$fixture" =~ ^v[0-9]+\.[0-9]+\.[0-9]+\.sql$ ]]; then
    echo "invalid release upgrade fixture name: $fixture" >&2
    exit 1
  fi
  sqlite3 -bail "$database" < "$fixture_dir/$fixture"
done < <(jq -er '.lineage[].fixture' "$manifest")

if [[ "$(sqlite3 "$database" 'SELECT COUNT(*) FROM connection_secret;')" != "0" ]]; then
  echo "release upgrade fixture contains stored connection secrets" >&2
  exit 1
fi
if [[ "$(sqlite3 "$database" 'SELECT COUNT(*) FROM events WHERE private_data IS NOT NULL;')" != "0" ]]; then
  echo "release upgrade fixture contains private event payloads" >&2
  exit 1
fi

boot_and_replay() {
  boot_number="$1"
  docker run \
    --detach \
    --rm \
    --name "$container_name" \
    --publish 127.0.0.1::8080 \
    --env MERCATOR_ADDR=0.0.0.0:8080 \
    --env MERCATOR_API_TOKEN=release-upgrade-token \
    --volume "$qualification_root:/data" \
    "$candidate_image" serve >/dev/null
  container_running=true

  revision="$(docker inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$container_name")"
  if [[ "$revision" != "$candidate_sha" ]]; then
    echo "candidate image revision is $revision, want $candidate_sha" >&2
    exit 1
  fi

  endpoint="$(docker port "$container_name" 8080/tcp)"
  port="${endpoint##*:}"
  base_url="http://127.0.0.1:$port"
  ready=false
  for _ in {1..60}; do
    if ! docker inspect "$container_name" >/dev/null 2>&1; then
      echo "candidate daemon exited before readiness on boot $boot_number" >&2
      exit 1
    fi
    if curl --fail --silent --show-error "$base_url/health/ready" >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 0.5
  done
  if [[ "$ready" != "true" ]]; then
    echo "candidate daemon did not become ready on boot $boot_number" >&2
    exit 1
  fi

  unauthenticated_status="$(
    curl --silent --show-error \
      --output "$qualification_root/unauthenticated.json" \
      --write-out '%{http_code}' \
      "$base_url/v1/runs?workspace_id=$workspace_id"
  )"
  if [[ "$unauthenticated_status" != "401" ]]; then
    echo "unauthenticated run list returned $unauthenticated_status, want 401" >&2
    exit 1
  fi

  curl --fail-with-body --silent --show-error \
    --header "Authorization: Bearer release-upgrade-token" \
    --output "$response" \
    "$base_url/v1/runs?workspace_id=$workspace_id"
  jq -e --arg run_id "$run_id" '
    .runs
    | length == 1
      and .[0].id == $run_id
      and .[0].closed == true
      and .[0].outcome == "succeeded"
  ' "$response" >/dev/null

  docker stop --time 10 "$container_name" >/dev/null
  container_running=false
}

boot_and_replay 1
boot_and_replay 2

echo "qualified $candidate_image from $candidate_sha against $previous_version persisted state"
