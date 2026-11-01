-- The backup role (deploy/backup/backup.sh, Q-97). Run once per cluster as a
-- superuser, after roles.sql:
--
--   psql "$SUPERUSER_URL" -v db=tenure -v backup_pw="$TENURE_BACKUP_PW" \
--        -f deploy/sql/backup-role.sql
--
-- tenure_backup reads every row for pg_dump: pg_read_all_data and BYPASSRLS,
-- since pg_dump refuses a table whose policies would filter it. It cannot
-- write, run DDL or log in from anywhere but the host (pg_hba.conf).

\set ON_ERROR_STOP on

CREATE ROLE tenure_backup LOGIN BYPASSRLS PASSWORD :'backup_pw';
GRANT CONNECT ON DATABASE :"db" TO tenure_backup;
GRANT pg_read_all_data TO tenure_backup;
ALTER ROLE tenure_backup SET default_transaction_read_only = on;
