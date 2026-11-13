-- 000027_maintenance — contractors and incidents (RENT-SPEC-015, part 17a,
-- Q-130). An incident of an asset is also its works order: who reported it
-- and how, its urgency and photos, then the contractor, the quote, the day
-- scheduled, the day done and the invoice. The invoice of a repair creates a
-- repairs charge (charge_id); that of an improvement is capitalised on the
-- incident and never becomes a charge. The account of improvements in the
-- owner's journal is seeded in legal_params.

-- +goose Up
CREATE TYPE incident_status AS ENUM ('new', 'quoted', 'scheduled', 'done', 'invoiced', 'cancelled');
CREATE TYPE incident_urgency AS ENUM ('low', 'normal', 'urgent');
CREATE TYPE works_treatment AS ENUM ('repair', 'improvement');

CREATE TABLE contractors (
    id         uuid PRIMARY KEY,
    owner_id   uuid NOT NULL REFERENCES owners (id),
    name       text NOT NULL CHECK (name <> ''),
    trade      text NOT NULL DEFAULT '',
    email      text NOT NULL DEFAULT '',
    phone      text NOT NULL DEFAULT '',
    notes      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version    integer NOT NULL DEFAULT 1,
    UNIQUE (id, owner_id)
);

CREATE TABLE incidents (
    id                  uuid PRIMARY KEY,
    owner_id            uuid NOT NULL REFERENCES owners (id),
    asset_id            uuid NOT NULL,
    lease_id            uuid,
    reported_on         date NOT NULL,
    -- tenant or owner; the channel it came by (email, phone, letter, portal, visit).
    reported_by         text NOT NULL CHECK (reported_by IN ('tenant', 'owner')),
    channel             text NOT NULL CHECK (channel IN ('email', 'phone', 'letter', 'portal', 'visit')),
    reporter_name       text NOT NULL DEFAULT '',
    title               text NOT NULL CHECK (title <> ''),
    description         text NOT NULL DEFAULT '',
    urgency             incident_urgency NOT NULL DEFAULT 'normal',
    status              incident_status NOT NULL DEFAULT 'new',
    photo_document_ids  text[] NOT NULL DEFAULT '{}',
    contractor_id       uuid,
    quote_cents         bigint CHECK (quote_cents > 0),
    quote_document_id   uuid,
    scheduled_on        date,
    done_on             date,
    invoice_cents       bigint CHECK (invoice_cents > 0),
    invoice_document_id uuid,
    treatment           works_treatment,
    paid_on             date,
    charge_id           uuid,
    cancel_reason       text NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL,
    created_by          text NOT NULL,
    updated_at          timestamptz NOT NULL,
    updated_by          text NOT NULL,
    version             integer NOT NULL DEFAULT 1,
    UNIQUE (id, owner_id),
    CHECK (status <> 'invoiced' OR (invoice_cents IS NOT NULL AND treatment IS NOT NULL)),
    CHECK (treatment IS DISTINCT FROM 'repair' OR status <> 'invoiced' OR charge_id IS NOT NULL),
    CHECK (treatment IS DISTINCT FROM 'improvement' OR charge_id IS NULL),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (contractor_id, owner_id) REFERENCES contractors (id, owner_id),
    FOREIGN KEY (quote_document_id, owner_id) REFERENCES documents (id, owner_id),
    FOREIGN KEY (invoice_document_id, owner_id) REFERENCES documents (id, owner_id),
    FOREIGN KEY (charge_id, owner_id) REFERENCES charges (id, owner_id)
);
CREATE INDEX incidents_asset ON incidents (owner_id, asset_id, status);

ALTER TABLE contractors ENABLE ROW LEVEL SECURITY;
ALTER TABLE contractors FORCE ROW LEVEL SECURITY;
ALTER TABLE incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE incidents FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON contractors FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON contractors FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON contractors FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON contractors TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
CREATE POLICY api_read ON incidents FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON incidents FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON incidents FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON incidents TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT, UPDATE ON contractors, incidents TO tenure_api;
GRANT SELECT ON contractors, incidents TO tenure_jobs;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('ledger.capitalised', '2024-01-01', '{"account":{"code":"213500","label":"Installations générales, agencements, aménagements des constructions"}}'::jsonb, 'RENT-SPEC-015 : les travaux d’amélioration sont immobilisés, non déduits ; compte du plan comptable général (règlement ANC n° 2014-03), choix de gestion à valider par l’expert-comptable', 'https://www.anc.gouv.fr/normes-francaises/plan-comptable-general');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'ledger.capitalised';
DROP TABLE incidents;
DROP TABLE contractors;
DROP TYPE works_treatment;
DROP TYPE incident_urgency;
DROP TYPE incident_status;
