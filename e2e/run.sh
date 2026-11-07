#!/usr/bin/env bash
# Runs the phase 1 flows of RENT-FE-001 §4 and the inventory lifecycle of
# RENT-INV-001 (slice 11, e2e/flows/phase2.spec.ts) in a browser against a real tenure
# (RENT-FE-001 §7, tenure-web slice 7b). Around tenure, this script starts:
#   - the services of docker-compose.dev.yml (PostgreSQL 17, the S3 archive,
#     gotenberg, ClamAV) under their own compose project, with fresh volumes;
#   - the test fakes of e2e/fakes (Socrate, an SMTP sink, the INSEE IRL series),
#     built with the e2e tag;
#   - tenure itself, built from this tree with the web bundle in web/dist.
# Then `tenure migrate up`, `tenure serve`, and Playwright on e2e/flows.
#
#   ./scripts/fetch-web.sh          # or E2E_WEB_DIST=<a built tenure-web dist>
#   ./e2e/run.sh                    # E2E_KEEP=1 leaves everything running
#
# It needs Docker, Go and Node. The ports of docker-compose.dev.yml are used,
# so stop a running dev stack first. Every password here is a test-only value.
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(pwd)
work=$(mktemp -d)
project=tenure-e2e
compose=(docker compose -p "$project" -f docker-compose.dev.yml)

restore_dist() {
  if [ -d "$work/dist.orig" ]; then rm -rf "$root/web/dist" && mv "$work/dist.orig" "$root/web/dist"; fi
}
cleanup() {
  status=$?
  restore_dist
  if [ "${E2E_KEEP:-}" = "1" ]; then
    echo "e2e: left running (tenure on http://127.0.0.1:8080, logs in $work)"
    return
  fi
  [ -n "${serve_pid:-}" ] && kill "$serve_pid" 2>/dev/null || true
  [ -n "${fakes_pid:-}" ] && kill "$fakes_pid" 2>/dev/null || true
  if [ "$status" -ne 0 ]; then
    echo "e2e: failed; last lines of tenure's log:" >&2
    tail -n 60 "$work/tenure.log" >&2 || true
  fi
  "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# The web bundle: a built tenure-web, never the placeholder page.
# A given dist replaces web/dist for the build only; the tree gets its own back.
if [ -n "${E2E_WEB_DIST:-}" ]; then
  mv web/dist "$work/dist.orig" && cp -R "$E2E_WEB_DIST" web/dist
fi
if [ ! -d web/dist/assets ]; then
  echo "e2e: web/dist holds no built tenure-web; run ./scripts/fetch-web.sh or set E2E_WEB_DIST" >&2
  exit 1
fi

echo "e2e: services"
# Fresh volumes even after an interrupted run: the flows expect an empty tenure.
"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
"${compose[@]}" up -d --wait postgres archive
"${compose[@]}" --profile init run --rm archive-init >/dev/null
# gotenberg before ClamAV: LibreOffice's start fails on a host busy loading the
# antivirus signatures, and gotenberg then exits. It gets one restart.
gotenberg_up() {
  for _ in $(seq 1 60); do
    curl -fsS http://127.0.0.1:3000/health >/dev/null 2>&1 && return 0
    sleep 3
  done
  return 1
}
"${compose[@]}" up -d gotenberg
gotenberg_up || { "${compose[@]}" up -d --force-recreate gotenberg && gotenberg_up; } || { echo "e2e: gotenberg did not start" >&2; exit 1; }
"${compose[@]}" up -d --wait clamav

echo "e2e: build"
go build -o "$work/tenure" ./cmd/tenure
restore_dist
go build -tags e2e -o "$work/fakes" ./e2e/fakes

"$work/fakes" -env "$work/fakes.env" >"$work/fakes.log" 2>&1 &
fakes_pid=$!
for _ in $(seq 1 50); do [ -s "$work/fakes.env" ] && break; sleep 0.1; done
set -a
# shellcheck disable=SC1091
. "$work/fakes.env"
TENURE_HTTP_ADDR=127.0.0.1:8080
TENURE_PUBLIC_URL=http://127.0.0.1:8080
TENURE_DB_URL=postgres://tenure_api:dev-only-api@localhost:5432/tenure
TENURE_JOBS_DB_URL=postgres://tenure_jobs:dev-only-jobs@localhost:5432/tenure
TENURE_MIGRATE_DB_URL=postgres://tenure_migrate:dev-only-migrate@localhost:5432/tenure
TENURE_SESSION_KEY=$(head -c 32 /dev/urandom | base64)
TENURE_STEP_UP_MAX_AGE=5m
TENURE_S3_ENDPOINT=localhost:9000
TENURE_S3_REGION=us-east-1
TENURE_S3_BUCKET=tenure-dev
TENURE_S3_ACCESS_KEY=dev-only-archive
TENURE_S3_SECRET_KEY=dev-only-archive-secret
TENURE_S3_INSECURE=true
TENURE_PDF_ENGINE_URL=http://127.0.0.1:3000
TENURE_CLAMAV_ADDR=127.0.0.1:3310
TENURE_SMTP_FROM=no-reply@tenure.localhost
TENURE_LOG_LEVEL=info
set +a

echo "e2e: migrate and serve"
"$work/tenure" migrate up
"$work/tenure" serve >"$work/tenure.log" 2>&1 &
serve_pid=$!
ready=""
for _ in $(seq 1 120); do
  if curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; then ready=1; break; fi
  kill -0 "$serve_pid" 2>/dev/null || break
  sleep 1
done
if [ -z "$ready" ]; then
  echo "e2e: tenure is not ready" >&2
  exit 1
fi

echo "e2e: flows"
cd "$root/e2e"
npm ci --no-audit --no-fund --silent
E2E_BASE_URL=http://127.0.0.1:8080 npx playwright test "$@"
