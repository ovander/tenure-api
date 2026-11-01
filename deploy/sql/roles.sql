-- Bootstrap of the Tenure database roles (RENT-BE-001 §8b). Run once per
-- cluster as a superuser, before the first `tenure migrate`:
--
--   psql "$SUPERUSER_URL" -v db=tenure \
--        -v migrate_pw="$TENURE_MIGRATE_PW" -v api_pw="$TENURE_API_PW" \
--        -v jobs_pw="$TENURE_JOBS_PW" -f deploy/sql/roles.sql
--
-- Passwords come from the environment, never from this file.
--
-- tenure_migrate  DDL; owns every object; used only by `tenure migrate`.
-- tenure_api      the API: no BYPASSRLS, no DDL.
-- tenure_jobs     the scheduler: sets app.owner_id per owner, no DDL.
-- tenure_rls      NOLOGIN, BYPASSRLS: owns only the SECURITY DEFINER function
--                 tenure_scope_role(), which reads owners and owner_grants for
--                 the row-level-security policies without recursing into them.

\set ON_ERROR_STOP on

CREATE ROLE tenure_migrate LOGIN NOBYPASSRLS PASSWORD :'migrate_pw';
CREATE ROLE tenure_api     LOGIN NOBYPASSRLS PASSWORD :'api_pw';
CREATE ROLE tenure_jobs    LOGIN NOBYPASSRLS PASSWORD :'jobs_pw';
CREATE ROLE tenure_rls     NOLOGIN BYPASSRLS;

-- tenure_migrate creates the function, then hands it to tenure_rls.
GRANT tenure_rls TO tenure_migrate;

GRANT CONNECT ON DATABASE :"db" TO tenure_migrate, tenure_api, tenure_jobs;
GRANT CREATE ON DATABASE :"db" TO tenure_migrate;
ALTER SCHEMA public OWNER TO tenure_migrate;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
