# Restore the database

The backup is nightly at 01:30 Europe/Paris: an encrypted `pg_dump` in the fr-par backup
bucket, kept 35 days (`backup/backup.sh`). The archive bucket is not restored: it is versioned
and locked, and every document row points at its object version.

**The restore is tested twice a year** (RENT-SPEC-022). Each test is logged in a GitHub issue
labelled `restore-test`, with the backup used, the duration and the result of the checks below.

## Steps

1. Get the age identity from the safe (`break-glass.md`). Never copy it to the host's disk:
   use a tmpfs such as `/dev/shm`, and delete it at the end.
2. Choose the backup: `aws --endpoint-url "$TENURE_BACKUP_ENDPOINT" s3 ls "$TENURE_BACKUP_BUCKET/"`.
3. Create a fresh database on a cluster where `sql/roles.sql` and `sql/backup-role.sql` have run,
   as a superuser:
   ```sql
   CREATE DATABASE tenure_restore;
   \c tenure_restore
   GRANT CONNECT ON DATABASE tenure_restore TO tenure_api, tenure_jobs, tenure_migrate, tenure_backup;
   GRANT CREATE ON DATABASE tenure_restore TO tenure_migrate;
   ALTER SCHEMA public OWNER TO tenure_migrate;
   REVOKE CREATE ON SCHEMA public FROM PUBLIC;
   ```
4. Restore. The script refuses a database that already has tables:
   `backup/restore.sh <name> /dev/shm/age.key postgres://postgres@127.0.0.1/tenure_restore`.
   The client tools must be PostgreSQL 17.
5. Check:
   - `tenure migrate status` shows every migration applied;
   - `tenure_scope_role()` is owned by `tenure_rls`;
   - the audit chain verifies: `tenure job audit.verify` against the restored database;
   - the latest chain head in the bucket matches or precedes the restored chain;
   - row counts of `owners`, `leases`, `billing_documents` and `documents` are plausible against
     the day before.
6. For a real recovery, swap the database:
   1. stop tenure;
   2. rename `tenure` to `tenure_broken` and `tenure_restore` to `tenure`;
   3. start tenure;
   4. check `/readyz`.

   Writes made after the backup are lost. List them from the audit chain head in the bucket and
   from the e-mails sent, and tell the owners concerned.
7. Delete the identity from `/dev/shm`. After a test restore, drop `tenure_restore`.
