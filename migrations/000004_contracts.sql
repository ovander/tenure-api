-- 000004_contracts — contracts, guarantees, diagnostics, inventories and uploads
-- (RENT-SPEC-008, RENT-SPEC-022; RENT-BE-001 §2, §3, §6, §8b; slice 5).
-- Reversible except for the enum value surety of document_class: PostgreSQL
-- cannot drop an enum value, so Down leaves it, unused (explicitly marked).

-- +goose Up
ALTER TYPE document_class ADD VALUE IF NOT EXISTS 'surety';
CREATE TYPE document_source AS ENUM ('generated', 'upload');
CREATE TYPE diagnostic_kind AS ENUM ('dpe', 'erp', 'electricity', 'gas', 'lead', 'asbestos');
CREATE TYPE inventory_kind AS ENUM ('entry', 'exit');
CREATE TYPE lease_document_kind AS ENUM ('contract', 'amendment', 'deposit_receipt', 'surety_act');
CREATE TYPE tenant_occupation AS ENUM ('student', 'apprentice', 'other');
-- Loi 89-462 art. 25-12: the tenant's reason for a bail mobilité.
CREATE TYPE mobility_reason AS ENUM ('vocational_training', 'higher_education', 'apprenticeship', 'internship',
    'civic_service', 'job_transfer', 'temporary_assignment');

-- Uploaded files share the documents table with generated ones (Q-67): an
-- upload has no number, keeps its sniffed content type and original name, and
-- a signed copy points at the generated document it signs.
ALTER TABLE documents ADD COLUMN source document_source NOT NULL DEFAULT 'generated';
ALTER TABLE documents ADD COLUMN original_name text NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN content_type text NOT NULL DEFAULT 'application/pdf';
ALTER TABLE documents ADD COLUMN signs_document_id uuid;
ALTER TABLE documents ADD CONSTRAINT documents_signs_fk
    FOREIGN KEY (signs_document_id, owner_id) REFERENCES documents (id, owner_id);
CREATE INDEX documents_signs ON documents (signs_document_id) WHERE signs_document_id IS NOT NULL;
CREATE INDEX documents_lease ON documents (lease_id, class) WHERE lease_id IS NOT NULL;

-- A student or apprentice tenant may have a surety alongside a rent insurance
-- (loi 89-462 art. 22-1, RENT-SPEC-008).
ALTER TABLE tenants ADD COLUMN occupation tenant_occupation;
ALTER TABLE leases ADD COLUMN mobility_reason mobility_reason;

-- The diagnostics of an asset (Q-23): one current record per kind. A kind the
-- asset does not need (a building after 1949 for lead) is declared so by the
-- owner with a reason, never assumed.
CREATE TABLE asset_diagnostics (
    id                  uuid PRIMARY KEY,
    owner_id            uuid NOT NULL REFERENCES owners (id),
    asset_id            uuid NOT NULL,
    kind                diagnostic_kind NOT NULL,
    done_on             date,
    valid_until         date CHECK (valid_until IS NULL OR done_on IS NULL OR valid_until >= done_on),
    result              text NOT NULL DEFAULT '',
    not_required_reason text NOT NULL DEFAULT '',
    document_id         uuid,
    version             integer NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL,
    created_by          text NOT NULL,
    updated_at          timestamptz NOT NULL,
    updated_by          text NOT NULL,
    UNIQUE (asset_id, kind),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id),
    CHECK (not_required_reason <> '' OR done_on IS NOT NULL)
);

-- États des lieux: one entry and one exit per lease, each with its uploaded
-- document; items are structured from phase 2. Recorded once, never changed.
CREATE TABLE inventories (
    id          uuid PRIMARY KEY,
    owner_id    uuid NOT NULL REFERENCES owners (id),
    lease_id    uuid NOT NULL,
    kind        inventory_kind NOT NULL,
    done_on     date NOT NULL,
    items       jsonb NOT NULL DEFAULT '[]',
    document_id uuid NOT NULL,
    created_at  timestamptz NOT NULL,
    created_by  text NOT NULL,
    UNIQUE (lease_id, kind),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);

-- Documents generated from a lease (RENT-SPEC-008): contracts, amendments,
-- deposit receipts and surety acts, created issued and never changed. A
-- contract records the lease version it was generated from (Q-69).
CREATE TABLE lease_documents (
    id             uuid PRIMARY KEY,
    owner_id       uuid NOT NULL REFERENCES owners (id),
    lease_id       uuid NOT NULL,
    kind           lease_document_kind NOT NULL,
    number         text NOT NULL,
    lease_version  integer NOT NULL,
    guarantee_id   uuid,
    document_id    uuid NOT NULL,
    input_snapshot jsonb NOT NULL,
    issued_at      timestamptz NOT NULL,
    created_by     text NOT NULL,
    UNIQUE (owner_id, number),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id),
    CHECK ((kind IN ('deposit_receipt', 'surety_act')) = (guarantee_id IS NOT NULL))
);
CREATE INDEX lease_documents_lease ON lease_documents (lease_id, kind, issued_at);

-- +goose StatementBegin
CREATE FUNCTION lease_documents_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'lease documents are immutable once issued' USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER lease_documents_immutable BEFORE UPDATE OR DELETE ON lease_documents
    FOR EACH ROW EXECUTE FUNCTION lease_documents_immutable();

-- +goose StatementBegin
CREATE FUNCTION tenure_owner_policies(t regclass) RETURNS void LANGUAGE plpgsql AS $$
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
    EXECUTE format('CREATE POLICY jobs_scope ON %s TO tenure_jobs
        USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id())', t);
END
$$;
-- +goose StatementEnd
SELECT tenure_owner_policies('asset_diagnostics');
SELECT tenure_owner_policies('inventories');
SELECT tenure_owner_policies('lease_documents');
DROP FUNCTION tenure_owner_policies(regclass);

GRANT SELECT, INSERT, UPDATE ON asset_diagnostics TO tenure_api;
GRANT SELECT, INSERT ON inventories, lease_documents TO tenure_api;
GRANT SELECT ON asset_diagnostics, inventories, lease_documents TO tenure_jobs;

-- Seed of legal_params added since 000003, generated from testdata/params/*.json
-- by go test ./migrations -update.
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_warning_months.box', '2025-01-01', '2'::jsonb, 'RENT-SPEC-008 et Q-26 (règle produit : un box loué seul n’a pas de plafond légal ; avertissement au-delà)', '');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('diagnostics', '2025-01-01', '{"dpe_min_done_on":"2021-07-01","required":{"box":["erp"],"dwelling":["dpe","erp","electricity","gas","lead","asbestos"]},"validity_months":{"asbestos":0,"dpe":120,"electricity":72,"erp":6,"gas":72,"lead":72}}'::jsonb, 'Loi 89-462 art. 3-3 (dossier de diagnostic technique); CCH L126-26 et décret 2020-1610 (DPE 10 ans, DPE antérieurs au 1er juillet 2021 caducs au 1er janvier 2025); C. env. L125-5 (état des risques de moins de 6 mois); décrets 2016-1104 et 2016-1105 (électricité, gaz : 6 ans); CSP L1334-7 (CREP : 6 ans); CSP R1334-29-5 (amiante, sans durée). Un diagnostic non requis est déclaré comme tel par le propriétaire (Q-23)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('mobility_term_months', '2025-01-01', '{"max":10,"min":1}'::jsonb, 'Loi 89-462 art. 25-14 (bail mobilité : 1 à 10 mois)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('rental_min_dpe_class', '2025-01-01', '"F"'::jsonb, 'Loi 2021-1104 art. 160 et décret 2002-120 art. 3 bis : un logement classé G n’est plus décent pour un nouveau bail depuis le 1er janvier 2025', 'https://www.legifrance.gouv.fr/jorf/id/JORFTEXT000043956924');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_warning_months.box', '2026-01-01', '2'::jsonb, 'RENT-SPEC-008 et Q-26 (règle produit : un box loué seul n’a pas de plafond légal ; avertissement au-delà)', '');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('diagnostics', '2026-01-01', '{"dpe_min_done_on":"2021-07-01","required":{"box":["erp"],"dwelling":["dpe","erp","electricity","gas","lead","asbestos"]},"validity_months":{"asbestos":0,"dpe":120,"electricity":72,"erp":6,"gas":72,"lead":72}}'::jsonb, 'Loi 89-462 art. 3-3 (dossier de diagnostic technique); CCH L126-26 et décret 2020-1610 (DPE 10 ans, DPE antérieurs au 1er juillet 2021 caducs au 1er janvier 2025); C. env. L125-5 (état des risques de moins de 6 mois); décrets 2016-1104 et 2016-1105 (électricité, gaz : 6 ans); CSP L1334-7 (CREP : 6 ans); CSP R1334-29-5 (amiante, sans durée). Un diagnostic non requis est déclaré comme tel par le propriétaire (Q-23)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('mobility_term_months', '2026-01-01', '{"max":10,"min":1}'::jsonb, 'Loi 89-462 art. 25-14 (bail mobilité : 1 à 10 mois)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('rental_min_dpe_class', '2026-01-01', '"F"'::jsonb, 'Loi 2021-1104 art. 160 et décret 2002-120 art. 3 bis : un logement classé G n’est plus décent pour un nouveau bail depuis le 1er janvier 2025', 'https://www.legifrance.gouv.fr/jorf/id/JORFTEXT000043956924');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_max_months.furnished', '2024-01-01', '2'::jsonb, 'Loi 89-462 art. 25-6', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_max_months.mobility', '2024-01-01', '0'::jsonb, 'Loi 89-462 art. 25-13', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_max_months.unfurnished', '2024-01-01', '1'::jsonb, 'Loi 89-462 art. 22', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit_warning_months.box', '2024-01-01', '2'::jsonb, 'RENT-SPEC-008 et Q-26 (règle produit : un box loué seul n’a pas de plafond légal ; avertissement au-delà)', '');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('mobility_term_months', '2024-01-01', '{"max":10,"min":1}'::jsonb, 'Loi 89-462 art. 25-14 (bail mobilité : 1 à 10 mois)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key IN ('diagnostics', 'rental_min_dpe_class', 'mobility_term_months',
    'deposit_warning_months.box');
DELETE FROM legal_params WHERE effective_from = '2024-01-01' AND key IN ('deposit_max_months.unfurnished',
    'deposit_max_months.furnished', 'deposit_max_months.mobility');
DROP TABLE lease_documents;
DROP FUNCTION lease_documents_immutable();
DROP TABLE inventories;
DROP TABLE asset_diagnostics;
ALTER TABLE leases DROP COLUMN mobility_reason;
ALTER TABLE tenants DROP COLUMN occupation;
DROP INDEX documents_lease;
DROP INDEX documents_signs;
ALTER TABLE documents DROP CONSTRAINT documents_signs_fk;
ALTER TABLE documents DROP COLUMN signs_document_id;
ALTER TABLE documents DROP COLUMN content_type;
ALTER TABLE documents DROP COLUMN original_name;
ALTER TABLE documents DROP COLUMN source;
DROP TYPE mobility_reason;
DROP TYPE tenant_occupation;
DROP TYPE lease_document_kind;
DROP TYPE inventory_kind;
DROP TYPE diagnostic_kind;
DROP TYPE document_source;
-- document_class keeps the value surety (see the header).
