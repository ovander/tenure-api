#!/usr/bin/env bash
# Nightly encrypted backup of the tenure database (RENT-BE-001 §8b, Q-97):
# pg_dump in custom format, encrypted on the host with age to the backup
# public key (its private key is kept offline, runbooks/break-glass.md), then
# uploaded to the EU backup bucket, which encrypts at rest (SSE) and expires
# objects by its lifecycle rule. Nothing unencrypted leaves the host or stays
# on disk. Settings come from /etc/tenure/backup.env:
#   TENURE_BACKUP_DB_URL      read-only dump role (pg_dump), TLS
#   TENURE_BACKUP_AGE_RECIPIENT  age public key (age1…)
#   TENURE_BACKUP_BUCKET      s3://bucket/prefix in fr-par
#   TENURE_BACKUP_ENDPOINT    https://s3.fr-par.scw.cloud
#   AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY  write-only key of the backup bucket
set -euo pipefail

: "${TENURE_BACKUP_DB_URL:?}" "${TENURE_BACKUP_AGE_RECIPIENT:?}" "${TENURE_BACKUP_BUCKET:?}" "${TENURE_BACKUP_ENDPOINT:?}"
case "$TENURE_BACKUP_AGE_RECIPIENT" in age1*) ;; *) echo "backup: TENURE_BACKUP_AGE_RECIPIENT is not an age public key" >&2; exit 1 ;; esac

stamp=$(date -u +%Y%m%dT%H%M%SZ)
name="tenure-${stamp}.dump.age"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The dump keeps owners and grants (the RLS helper must stay owned by
# tenure_rls) and is streamed straight into age: no plaintext file on disk.
pg_dump --format=custom --dbname="$TENURE_BACKUP_DB_URL" |
  age --encrypt --recipient "$TENURE_BACKUP_AGE_RECIPIENT" --output "$tmp/$name"
sha256sum "$tmp/$name" | sed "s|$tmp/||" >"$tmp/$name.sha256"

aws --endpoint-url "$TENURE_BACKUP_ENDPOINT" s3 cp --only-show-errors "$tmp/$name" "$TENURE_BACKUP_BUCKET/$name"
aws --endpoint-url "$TENURE_BACKUP_ENDPOINT" s3 cp --only-show-errors "$tmp/$name.sha256" "$TENURE_BACKUP_BUCKET/$name.sha256"
echo "backup: $name ($(stat -c %s "$tmp/$name") bytes)"
