-- 000001_init — phase 1 slice 2 schema (RENT-BE-001 §2, §8b; OPEN-QUESTIONS Q-03, Q-05, Q-21, Q-28, Q-32, Q-33).
-- Reversible: the Down section drops everything this file creates.
-- Requires the roles of deploy/sql/roles.sql; runs as tenure_migrate.

-- +goose Up

-- tenure_rls owns the SECURITY DEFINER functions below (deploy/sql/roles.sql).
GRANT CREATE, USAGE ON SCHEMA public TO tenure_rls;

-- Session context set by internal/db in every transaction ----------------------
-- +goose StatementBegin
CREATE FUNCTION app_owner_id() RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.owner_id', true), '')::uuid
$$;
CREATE FUNCTION app_actor_sub() RETURNS text LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.actor_sub', true), '')
$$;
-- +goose StatementEnd

-- Enums ---------------------------------------------------------------------------
CREATE TYPE lang AS ENUM ('fr', 'en');
CREATE TYPE grant_role AS ENUM ('co_owner', 'accountant');
CREATE TYPE asset_kind AS ENUM ('house', 'apartment', 'box');
CREATE TYPE lease_kind AS ENUM ('unfurnished', 'furnished', 'mobility', 'box');
CREATE TYPE lease_state AS ENUM ('draft', 'active', 'notice', 'ended', 'closed');
CREATE TYPE rent_exemption AS ENUM ('works', 'under_valued');
CREATE TYPE guarantee_kind AS ENUM ('deposit', 'surety', 'visale', 'insurance');
CREATE TYPE guarantee_status AS ENUM ('held', 'valid', 'expired', 'settled');
CREATE TYPE document_class AS ENUM ('rent_call', 'receipt', 'regularisation', 'revision_notice', 'contract',
    'amendment', 'deposit', 'tax_summary', 'inventory', 'supporting', 'other');
CREATE TYPE job_status AS ENUM ('running', 'succeeded', 'failed', 'partial');

-- Owners and grants ---------------------------------------------------------------
CREATE TABLE owners (
    id          uuid PRIMARY KEY,
    socrate_sub text NOT NULL UNIQUE,
    legal_name  text NOT NULL CHECK (legal_name <> ''),
    address     text NOT NULL DEFAULT '',
    tax_id      text NOT NULL DEFAULT '',
    language    lang NOT NULL DEFAULT 'fr',
    version     integer NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL,
    created_by  text NOT NULL,
    updated_at  timestamptz NOT NULL,
    updated_by  text NOT NULL
);

CREATE TABLE owner_grants (
    id          uuid PRIMARY KEY,
    owner_id    uuid NOT NULL REFERENCES owners (id),
    grantee_sub text NOT NULL CHECK (grantee_sub <> ''),
    role        grant_role NOT NULL,
    granted_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    created_by  text NOT NULL,
    updated_at  timestamptz NOT NULL,
    updated_by  text NOT NULL
);
CREATE UNIQUE INDEX owner_grants_live ON owner_grants (owner_id, grantee_sub) WHERE revoked_at IS NULL;
CREATE INDEX owner_grants_grantee ON owner_grants (grantee_sub) WHERE revoked_at IS NULL;

-- tenure_scope_role(owner) is the caller's role in that owner's scope: 'owner'
-- when app.actor_sub is the owner's Socrate subject, the live grant's role
-- otherwise, NULL when none. Owned by tenure_rls (BYPASSRLS) so the policies of
-- owners and owner_grants can use it without recursing into each other.
-- +goose StatementBegin
CREATE FUNCTION tenure_scope_role(p_owner uuid) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT CASE
        WHEN app_actor_sub() IS NULL OR p_owner IS NULL THEN NULL
        WHEN EXISTS (SELECT 1 FROM owners WHERE id = p_owner AND socrate_sub = app_actor_sub()) THEN 'owner'
        ELSE (SELECT role::text FROM owner_grants
              WHERE owner_id = p_owner AND grantee_sub = app_actor_sub() AND revoked_at IS NULL
              LIMIT 1)
    END
$$;
-- +goose StatementEnd
GRANT SELECT ON owners, owner_grants TO tenure_rls;
ALTER FUNCTION tenure_scope_role(uuid) OWNER TO tenure_rls;
REVOKE ALL ON FUNCTION tenure_scope_role(uuid) FROM PUBLIC;

-- Portfolio -----------------------------------------------------------------------
CREATE TABLE assets (
    id                    uuid PRIMARY KEY,
    owner_id              uuid NOT NULL REFERENCES owners (id),
    kind                  asset_kind NOT NULL,
    address               text NOT NULL CHECK (address <> ''),
    lot                   text NOT NULL DEFAULT '',
    surface_m2            numeric(8, 2) CHECK (surface_m2 > 0),
    furnished             boolean NOT NULL DEFAULT false,
    dpe_class             char(1) CHECK (dpe_class IN ('A', 'B', 'C', 'D', 'E', 'F', 'G')),
    controlled_zone       boolean NOT NULL DEFAULT false,
    rent_control_zone     text NOT NULL DEFAULT '',
    rent_control_category text NOT NULL DEFAULT '',
    acquired_on           date,
    acquisition_cents     bigint CHECK (acquisition_cents >= 0),
    parent_asset_id       uuid,
    version               integer NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    created_by            text NOT NULL,
    updated_at            timestamptz NOT NULL,
    updated_by            text NOT NULL,
    UNIQUE (id, owner_id),
    FOREIGN KEY (parent_asset_id, owner_id) REFERENCES assets (id, owner_id),
    CHECK (parent_asset_id IS NULL OR kind = 'box'),
    CHECK (NOT controlled_zone OR kind = 'box' OR (rent_control_zone <> '' AND rent_control_category <> ''))
);

-- asset_shares.owner_id is the co-owner holding the share; access follows the asset.
CREATE TABLE asset_shares (
    asset_id  uuid NOT NULL REFERENCES assets (id),
    owner_id  uuid NOT NULL REFERENCES owners (id),
    share_pct numeric(5, 2) NOT NULL CHECK (share_pct > 0 AND share_pct <= 100),
    PRIMARY KEY (asset_id, owner_id)
);

CREATE TABLE tenants (
    id         uuid PRIMARY KEY,
    owner_id   uuid NOT NULL REFERENCES owners (id),
    names      text[] NOT NULL CHECK (cardinality(names) > 0),
    email      text NOT NULL DEFAULT '',
    phone      text NOT NULL DEFAULT '',
    language   lang NOT NULL DEFAULT 'fr',
    version    integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    UNIQUE (id, owner_id)
);

-- Leases --------------------------------------------------------------------------
CREATE TABLE leases (
    id              uuid PRIMARY KEY,
    owner_id        uuid NOT NULL REFERENCES owners (id),
    kind            lease_kind NOT NULL,
    state           lease_state NOT NULL DEFAULT 'draft',
    start_on        date NOT NULL,
    end_on          date CHECK (end_on IS NULL OR end_on >= start_on),
    term_months     integer NOT NULL CHECK (term_months > 0),
    rent_cents      bigint NOT NULL CHECK (rent_cents >= 0),
    provision_cents bigint NOT NULL DEFAULT 0 CHECK (provision_cents >= 0),
    deposit_cents   bigint NOT NULL DEFAULT 0 CHECK (deposit_cents >= 0),
    due_day         integer NOT NULL CHECK (due_day BETWEEN 1 AND 28),
    irl_ref_quarter char(7) NOT NULL CHECK (irl_ref_quarter ~ '^[0-9]{4}-Q[1-4]$'),
    irl_ref_value   integer NOT NULL CHECK (irl_ref_value > 0),
    revision_month  integer NOT NULL CHECK (revision_month BETWEEN 1 AND 12),
    rent_exemption  rent_exemption,
    rent_breakdown  jsonb,
    clauses         jsonb NOT NULL DEFAULT '[]',
    version         integer NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL,
    created_by      text NOT NULL,
    updated_at      timestamptz NOT NULL,
    updated_by      text NOT NULL,
    UNIQUE (id, owner_id)
);

CREATE TABLE lease_assets (
    lease_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    owner_id uuid NOT NULL,
    PRIMARY KEY (lease_id, asset_id),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id)
);
CREATE INDEX lease_assets_asset ON lease_assets (asset_id);

CREATE TABLE lease_tenants (
    lease_id  uuid NOT NULL,
    tenant_id uuid NOT NULL,
    owner_id  uuid NOT NULL,
    solidary  boolean NOT NULL DEFAULT true,
    PRIMARY KEY (lease_id, tenant_id),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (tenant_id, owner_id) REFERENCES tenants (id, owner_id)
);

-- Documents (metadata only in slice 2; the pipeline comes in slice 3) ------------
CREATE TABLE documents (
    id               uuid PRIMARY KEY,
    owner_id         uuid NOT NULL REFERENCES owners (id),
    class            document_class NOT NULL,
    asset_id         uuid,
    lease_id         uuid,
    period           char(7) CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
    object_key       text NOT NULL,
    sha256           char(64) NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes       bigint NOT NULL CHECK (size_bytes >= 0),
    template_version text NOT NULL DEFAULT '',
    issued_at        timestamptz,
    retain_until     date,
    created_at       timestamptz NOT NULL,
    created_by       text NOT NULL,
    UNIQUE (id, owner_id),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id)
);

-- +goose StatementBegin
CREATE FUNCTION documents_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'documents are never deleted' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF OLD.issued_at IS NOT NULL THEN
        RAISE EXCEPTION 'document % is issued and immutable', OLD.id USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER documents_immutable BEFORE UPDATE OR DELETE ON documents
    FOR EACH ROW EXECUTE FUNCTION documents_immutable();

CREATE TABLE guarantees (
    id           uuid PRIMARY KEY,
    owner_id     uuid NOT NULL,
    lease_id     uuid NOT NULL,
    kind         guarantee_kind NOT NULL,
    guarantor    jsonb NOT NULL DEFAULT '{}',
    amount_cents bigint NOT NULL DEFAULT 0 CHECK (amount_cents >= 0),
    valid_from   date,
    valid_to     date CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from),
    status       guarantee_status NOT NULL DEFAULT 'held',
    document_id  uuid,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL,
    created_by   text NOT NULL,
    updated_at   timestamptz NOT NULL,
    updated_by   text NOT NULL,
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE INDEX guarantees_lease ON guarantees (lease_id);

-- Audit trail (RENT-SPEC-022) -----------------------------------------------------
-- owner_id NULL is the platform chain (admin parameter changes, Q-36).
CREATE TABLE audit_events (
    id          bigserial PRIMARY KEY,
    owner_id    uuid REFERENCES owners (id),
    actor_sub   text NOT NULL,
    scope       text NOT NULL,
    action      text NOT NULL,
    entity_type text NOT NULL,
    entity_id   text NOT NULL,
    before      jsonb,
    after       jsonb,
    request_id  text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL,
    prev_hash   char(64) NOT NULL,
    hash        char(64) NOT NULL UNIQUE
);
CREATE INDEX audit_events_chain ON audit_events (owner_id, id);
CREATE INDEX audit_events_entity ON audit_events (owner_id, entity_type, entity_id);

-- +goose StatementBegin
CREATE FUNCTION audit_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only' USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();
CREATE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_events_append_only();

-- audit_last_hash() returns the head of the chain of the current scope
-- (app.owner_id, or the platform chain when unset), so a caller allowed to append
-- an event (a co-owner downloading a document) need not be allowed to read the
-- chain. Owned by tenure_rls.
-- +goose StatementBegin
CREATE FUNCTION audit_last_hash() RETURNS char(64)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT hash FROM audit_events
    WHERE owner_id IS NOT DISTINCT FROM app_owner_id()
    ORDER BY id DESC LIMIT 1
$$;
-- +goose StatementEnd
GRANT SELECT ON audit_events TO tenure_rls;
ALTER FUNCTION audit_last_hash() OWNER TO tenure_rls;
REVOKE ALL ON FUNCTION audit_last_hash() FROM PUBLIC;

CREATE TABLE audit_chain_heads (
    owner_id      uuid REFERENCES owners (id),
    as_of         date NOT NULL,
    last_event_id bigint NOT NULL,
    head_hash     char(64) NOT NULL,
    object_key    text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX audit_chain_heads_key
    ON audit_chain_heads (COALESCE(owner_id, '00000000-0000-0000-0000-000000000000'::uuid), as_of);

-- Legal parameters and IRL --------------------------------------------------------
CREATE TABLE legal_params (
    key            text NOT NULL,
    effective_from date NOT NULL,
    value          jsonb NOT NULL,
    -- form_2044_line_map entries of different income years may share a date.
    variant        text GENERATED ALWAYS AS (COALESCE(value ->> 'tax_year', '')) STORED,
    source         text NOT NULL CHECK (source <> ''),
    source_url     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    created_by     text NOT NULL DEFAULT 'migration',
    PRIMARY KEY (key, effective_from, variant)
);

CREATE TABLE irl_index (
    quarter      char(7) PRIMARY KEY CHECK (quarter ~ '^[0-9]{4}-Q[1-4]$'),
    value        integer NOT NULL CHECK (value > 0),
    published_on date NOT NULL,
    source       text NOT NULL DEFAULT 'insee',
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Jobs ----------------------------------------------------------------------------
CREATE TABLE job_runs (
    id          uuid PRIMARY KEY,
    job         text NOT NULL,
    period      text NOT NULL,
    owner_id    uuid REFERENCES owners (id),
    dry_run     boolean NOT NULL DEFAULT false,
    started_at  timestamptz NOT NULL,
    finished_at timestamptz,
    status      job_status NOT NULL DEFAULT 'running',
    summary     jsonb NOT NULL DEFAULT '{}'
);
CREATE UNIQUE INDEX job_runs_key
    ON job_runs (job, period, COALESCE(owner_id, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE NOT dry_run;

-- Row-level security --------------------------------------------------------------
-- Standard owner-table policies: read for any role in the scope, write for the
-- owner only (co-owners and accountants are read-only in the database too),
-- jobs limited to the owner they iterate on.
-- +goose StatementBegin
CREATE FUNCTION tenure_owner_policies(t regclass, with_delete boolean) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY api_read ON %s FOR SELECT TO tenure_api
        USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL)', t);
    EXECUTE format('CREATE POLICY api_insert ON %s FOR INSERT TO tenure_api
        WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = ''owner'')', t);
    EXECUTE format('CREATE POLICY api_update ON %s FOR UPDATE TO tenure_api
        USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = ''owner'')
        WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = ''owner'')', t);
    IF with_delete THEN
        EXECUTE format('CREATE POLICY api_delete ON %s FOR DELETE TO tenure_api
            USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = ''owner'')', t);
    END IF;
    EXECUTE format('CREATE POLICY jobs_scope ON %s TO tenure_jobs
        USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id())', t);
END
$$;
-- +goose StatementEnd
SELECT tenure_owner_policies('assets', false);
SELECT tenure_owner_policies('tenants', false);
SELECT tenure_owner_policies('leases', false);
SELECT tenure_owner_policies('lease_assets', true);
SELECT tenure_owner_policies('lease_tenants', true);
SELECT tenure_owner_policies('guarantees', false);
SELECT tenure_owner_policies('documents', false);
DROP FUNCTION tenure_owner_policies(regclass, boolean);

ALTER TABLE owners ENABLE ROW LEVEL SECURITY;
ALTER TABLE owners FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON owners FOR SELECT TO tenure_api USING (tenure_scope_role(id) IS NOT NULL);
CREATE POLICY api_insert ON owners FOR INSERT TO tenure_api
    WITH CHECK (socrate_sub = app_actor_sub() AND id = app_owner_id());
CREATE POLICY api_update ON owners FOR UPDATE TO tenure_api
    USING (socrate_sub = app_actor_sub()) WITH CHECK (socrate_sub = app_actor_sub());
CREATE POLICY jobs_read ON owners FOR SELECT TO tenure_jobs USING (true);

ALTER TABLE owner_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE owner_grants FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON owner_grants FOR SELECT TO tenure_api
    USING (grantee_sub = app_actor_sub() OR tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_insert ON owner_grants FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON owner_grants FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_read ON owner_grants FOR SELECT TO tenure_jobs USING (true);

ALTER TABLE asset_shares ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_shares FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON asset_shares FOR SELECT TO tenure_api
    USING (EXISTS (SELECT 1 FROM assets a WHERE a.id = asset_shares.asset_id));
CREATE POLICY api_insert ON asset_shares FOR INSERT TO tenure_api
    WITH CHECK (EXISTS (SELECT 1 FROM assets a WHERE a.id = asset_shares.asset_id
                        AND tenure_scope_role(a.owner_id) = 'owner'));
CREATE POLICY api_delete ON asset_shares FOR DELETE TO tenure_api
    USING (EXISTS (SELECT 1 FROM assets a WHERE a.id = asset_shares.asset_id
                   AND tenure_scope_role(a.owner_id) = 'owner'));
CREATE POLICY jobs_scope ON asset_shares TO tenure_jobs
    USING (EXISTS (SELECT 1 FROM assets a WHERE a.id = asset_shares.asset_id));

ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE ROW LEVEL SECURITY;
-- Q-37: the audit trail is read by the owner and the accountant (RENT-BE-001 §3).
CREATE POLICY api_read ON audit_events FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IN ('owner', 'accountant'));
CREATE POLICY api_insert ON audit_events FOR INSERT TO tenure_api
    WITH CHECK (owner_id IS NOT DISTINCT FROM app_owner_id()
                AND (owner_id IS NULL OR tenure_scope_role(owner_id) IS NOT NULL)
                AND actor_sub = app_actor_sub());
CREATE POLICY jobs_read ON audit_events FOR SELECT TO tenure_jobs
    USING (owner_id IS NOT DISTINCT FROM app_owner_id());
CREATE POLICY jobs_insert ON audit_events FOR INSERT TO tenure_jobs
    WITH CHECK (owner_id IS NOT DISTINCT FROM app_owner_id());

ALTER TABLE audit_chain_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_chain_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON audit_chain_heads FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IN ('owner', 'accountant'));
CREATE POLICY jobs_scope ON audit_chain_heads TO tenure_jobs
    USING (owner_id IS NOT DISTINCT FROM app_owner_id()) WITH CHECK (owner_id IS NOT DISTINCT FROM app_owner_id());

ALTER TABLE job_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON job_runs FOR SELECT TO tenure_api
    USING (owner_id IS NULL OR (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL));
CREATE POLICY jobs_all ON job_runs TO tenure_jobs USING (true) WITH CHECK (true);

-- Grants ----------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO tenure_api, tenure_jobs;
GRANT EXECUTE ON FUNCTION app_owner_id(), app_actor_sub(), tenure_scope_role(uuid), audit_last_hash()
    TO tenure_api, tenure_jobs;

GRANT SELECT, INSERT, UPDATE ON owners, owner_grants, assets, tenants, leases, guarantees, documents TO tenure_api;
GRANT SELECT, INSERT, DELETE ON asset_shares, lease_assets, lease_tenants TO tenure_api;
GRANT SELECT, INSERT ON audit_events TO tenure_api;
GRANT USAGE ON SEQUENCE audit_events_id_seq TO tenure_api, tenure_jobs;
GRANT SELECT ON audit_chain_heads, irl_index, job_runs TO tenure_api;
GRANT SELECT, INSERT ON legal_params TO tenure_api;

GRANT SELECT ON ALL TABLES IN SCHEMA public TO tenure_jobs;
GRANT INSERT, UPDATE ON job_runs, irl_index TO tenure_jobs;
GRANT INSERT ON audit_events, audit_chain_heads TO tenure_jobs;

-- Seed of legal_params, generated from testdata/params/*.json by
-- `go test ./migrations -update`; a test fails when it drifts from the fixtures.
-- seed:legal_params:begin
-- 2025.json
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deficit_carryforward_years', '2025-01-01', '10'::jsonb, 'CGI art. 156, I-3°', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deficit_global_cap_cents', '2025-01-01', '1070000'::jsonb, 'CGI art. 156, I-3°', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_max_months.furnished', '2025-01-01', '2'::jsonb, 'Loi 89-462 art. 25-6', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_max_months.mobility', '2025-01-01', '0'::jsonb, 'Loi 89-462 art. 25-13', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_max_months.unfurnished', '2025-01-01', '1'::jsonb, 'Loi 89-462 art. 22', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('forfait_222_cents', '2025-01-01', '2000'::jsonb, 'CGI art. 31, I-1° e', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('form_2044_line_map', '2025-01-01', '{"income_lines":["211","212","213"],"interest_lines":["250"],"lines":{"copro_provision":"229","copro_regularisation_prior":"230","copro_statement":"","forfait_222":"222","indemnity":"213","insurance":"223","loan_insurance":"250","loan_interest":"250","management":"221","owner_expense_charged_to_tenant":"212","property_tax":"227","recoverable_utilities":"","rent":"211","repairs":"224","teom":"","unrecovered_at_close":"225"},"millesime":2026,"subtracted_lines":["230"],"tax_year":2025}'::jsonb, 'Notice 2044, income year 2025 (millésime 2026)', 'https://www.impots.gouv.fr/formulaire/2044/declaration-des-revenus-fonciers');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('irl_cap', '2025-01-01', '{"applies":false,"rate_bp":0}'::jsonb, 'Loi 2022-1158 art. 12: cap applied to indices Q3 2022–Q1 2024 only', 'https://www.legifrance.gouv.fr/jorf/id/JORFTEXT000046186723');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('micro_foncier_allowance_pct', '2025-01-01', '30'::jsonb, 'CGI art. 32, 1', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('micro_foncier_threshold_cents', '2025-01-01', '1500000'::jsonb, 'CGI art. 32, 1', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('receipt_due_days_before_reminder', '2025-01-01', '5'::jsonb, 'RENT-SPEC-003 (product rule, not statutory)', '');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('regularisation_spread_after_months', '2025-01-01', '12'::jsonb, 'Loi 89-462 art. 23; RENT-SPEC-007', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('rent_control.relocation_rule', '2025-01-01', '{"applies":true}'::jsonb, 'Loi 89-462 art. 18 and the annual relocation decree (décret 2017-1198, renewed each year)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('rent_control.zone_ceiling', '2025-01-01', '{"zones":{}}'::jsonb, 'Loi 89-462 art. 17 and loi ELAN art. 140; per-zone values come from each prefectoral arrêté and are added when an asset is in that zone (none yet)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- 2026.json
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deficit_carryforward_years', '2026-01-01', '10'::jsonb, 'CGI art. 156, I-3°', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deficit_global_cap_cents', '2026-01-01', '1070000'::jsonb, 'CGI art. 156, I-3°', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_max_months.furnished', '2026-01-01', '2'::jsonb, 'Loi 89-462 art. 25-6', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_max_months.mobility', '2026-01-01', '0'::jsonb, 'Loi 89-462 art. 25-13', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_max_months.unfurnished', '2026-01-01', '1'::jsonb, 'Loi 89-462 art. 22', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('forfait_222_cents', '2026-01-01', '2000'::jsonb, 'CGI art. 31, I-1° e', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('irl_cap', '2026-01-01', '{"applies":false,"rate_bp":0}'::jsonb, 'Loi 2022-1158 art. 12: cap applied to indices Q3 2022–Q1 2024 only', 'https://www.legifrance.gouv.fr/jorf/id/JORFTEXT000046186723');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('micro_foncier_allowance_pct', '2026-01-01', '30'::jsonb, 'CGI art. 32, 1', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('micro_foncier_threshold_cents', '2026-01-01', '1500000'::jsonb, 'CGI art. 32, 1', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('receipt_due_days_before_reminder', '2026-01-01', '5'::jsonb, 'RENT-SPEC-003 (product rule, not statutory)', '');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('regularisation_spread_after_months', '2026-01-01', '12'::jsonb, 'Loi 89-462 art. 23; RENT-SPEC-007', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('rent_control.relocation_rule', '2026-01-01', '{"applies":true}'::jsonb, 'Loi 89-462 art. 18 and the annual relocation decree (décret 2017-1198, renewed each year)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('rent_control.zone_ceiling', '2026-01-01', '{"zones":{}}'::jsonb, 'Loi 89-462 art. 17 and loi ELAN art. 140; per-zone values come from each prefectoral arrêté and are added when an asset is in that zone (none yet)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DROP TABLE job_runs;
DROP TABLE irl_index;
DROP TABLE legal_params;
DROP TABLE audit_chain_heads;
DROP FUNCTION audit_last_hash();
DROP TABLE audit_events;
DROP FUNCTION audit_events_append_only();
DROP TABLE guarantees;
DROP TABLE documents;
DROP FUNCTION documents_immutable();
DROP TABLE lease_tenants;
DROP TABLE lease_assets;
DROP TABLE leases;
DROP TABLE tenants;
DROP TABLE asset_shares;
DROP TABLE assets;
DROP TABLE owner_grants;
DROP TABLE owners;
DROP FUNCTION tenure_scope_role(uuid);
DROP TYPE job_status;
DROP TYPE document_class;
DROP TYPE guarantee_status;
DROP TYPE guarantee_kind;
DROP TYPE rent_exemption;
DROP TYPE lease_state;
DROP TYPE lease_kind;
DROP TYPE asset_kind;
DROP TYPE grant_role;
DROP TYPE lang;
DROP FUNCTION app_actor_sub();
DROP FUNCTION app_owner_id();
REVOKE CREATE, USAGE ON SCHEMA public FROM tenure_rls;
REVOKE USAGE ON SCHEMA public FROM tenure_api, tenure_jobs;
