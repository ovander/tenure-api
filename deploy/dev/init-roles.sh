#!/bin/sh
# Runs once, on the first start of the dev database container: creates the
# Tenure roles with the dev passwords of docker-compose.dev.yml.
set -eu
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -v db="$POSTGRES_DB" \
  -v migrate_pw="$TENURE_MIGRATE_PW" -v api_pw="$TENURE_API_PW" -v jobs_pw="$TENURE_JOBS_PW" \
  -f /tenure/roles.sql
