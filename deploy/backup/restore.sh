#!/usr/bin/env bash
# Restores a tenure backup into an EMPTY database (deploy/runbooks/restore.md):
#   restore.sh <backup name> <age identity file> <target database URL>
# The target URL connects as a superuser of a cluster where roles.sql and
# backup-role.sql have run, to a fresh database prepared as restore.md says
# (the dump restores owners and grants, the RLS helper's owner included).
# It downloads the backup and its checksum, verifies it, decrypts it with the
# offline age identity and restores it with pg_restore. The target must be a
# fresh database whose roles exist (deploy/sql/roles.sql); it never restores
# over a database that already holds tables.
set -euo pipefail

name=${1:?usage: restore.sh <backup name> <age identity file> <target database URL>}
identity=${2:?}
target=${3:?}
: "${TENURE_BACKUP_BUCKET:?}" "${TENURE_BACKUP_ENDPOINT:?}"

tables=$(psql --no-psqlrc --tuples-only --no-align --dbname="$target" \
  -c "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'")
if [ "$tables" != "0" ]; then
  echo "restore: $target is not empty ($tables tables); restore into a fresh database" >&2
  exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
aws --endpoint-url "$TENURE_BACKUP_ENDPOINT" s3 cp --only-show-errors "$TENURE_BACKUP_BUCKET/$name" "$tmp/$name"
aws --endpoint-url "$TENURE_BACKUP_ENDPOINT" s3 cp --only-show-errors "$TENURE_BACKUP_BUCKET/$name.sha256" "$tmp/$name.sha256"
(cd "$tmp" && sha256sum --check --strict "$name.sha256")

age --decrypt --identity "$identity" "$tmp/$name" |
  pg_restore --exit-on-error --dbname="$target"
echo "restore: $name restored; run the checks of runbooks/restore.md"
