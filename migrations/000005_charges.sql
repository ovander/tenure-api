-- 000005_charges — charges ledger, syndic import and reconciliation (RENT-SPEC-007;
-- RENT-BE-001 §2, §3, §5; slice 6). Reversible: Down drops everything this file creates.

-- +goose Up
CREATE TYPE charge_category AS ENUM ('copro_provision', 'copro_statement', 'copro_regularisation_prior', 'property_tax',
    'teom', 'insurance', 'repairs', 'management', 'loan_interest', 'loan_insurance', 'recoverable_utilities', 'indemnity',
    'owner_expense_charged_to_tenant', 'unrecovered_at_close');
CREATE TYPE charge_source AS ENUM ('manual', 'syndic', 'invoice');
CREATE TYPE charge_status AS ENUM ('draft', 'confirmed');
CREATE TYPE reconciliation_state AS ENUM ('draft', 'allocated', 'issued', 'settled', 'cancelled');
CREATE TYPE settlement_mode AS ENUM ('fee', 'deposit', 'direct', 'none');

-- The charges ledger. recoverable_cents is the part recoverable from the
-- tenant (décret 87-713); syndic_recoverable_cents keeps the syndic's own
-- split of an imported line, so an override stays visible (RENT-SPEC-007).
-- A charge used by an issued reconciliation is frozen (reconciliation_id).
CREATE TABLE charges (
    id                       uuid PRIMARY KEY,
    owner_id                 uuid NOT NULL REFERENCES owners (id),
    asset_id                 uuid NOT NULL,
    charged_on               date NOT NULL,
    paid_on                  date,
    category                 charge_category NOT NULL,
    label                    text NOT NULL DEFAULT '',
    amount_cents             bigint NOT NULL CHECK (amount_cents >= 0),
    recoverable_cents        bigint NOT NULL CHECK (recoverable_cents >= 0 AND recoverable_cents <= amount_cents),
    syndic_recoverable_cents bigint CHECK (syndic_recoverable_cents IS NULL OR
        (syndic_recoverable_cents >= 0 AND syndic_recoverable_cents <= amount_cents)),
    exercise_year            integer NOT NULL CHECK (exercise_year BETWEEN 1970 AND 9999),
    document_id              uuid,
    source                   charge_source NOT NULL DEFAULT 'manual',
    status                   charge_status NOT NULL DEFAULT 'confirmed',
    reconciliation_id        uuid,
    version                  integer NOT NULL DEFAULT 1,
    created_at               timestamptz NOT NULL,
    created_by               text NOT NULL,
    updated_at               timestamptz NOT NULL,
    updated_by               text NOT NULL,
    UNIQUE (id, owner_id),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE INDEX charges_asset ON charges (asset_id, charged_on);

-- One reconciliation per asset and exercise while not cancelled; a
-- correction cancels the issued one and starts a new one (corrects_id).
CREATE TABLE reconciliations (
    id                       uuid PRIMARY KEY,
    owner_id                 uuid NOT NULL REFERENCES owners (id),
    asset_id                 uuid NOT NULL,
    exercise_year            integer NOT NULL,
    exercise_start           date NOT NULL,
    exercise_end             date NOT NULL CHECK (exercise_end >= exercise_start),
    state                    reconciliation_state NOT NULL DEFAULT 'draft',
    totals                   jsonb NOT NULL DEFAULT '{}',
    charge_ids               jsonb NOT NULL DEFAULT '[]',
    issued_at                timestamptz,
    corrects_id              uuid,
    cancellation_document_id uuid,
    version                  integer NOT NULL DEFAULT 1,
    created_at               timestamptz NOT NULL,
    created_by               text NOT NULL,
    updated_at               timestamptz NOT NULL,
    updated_by               text NOT NULL,
    UNIQUE (id, owner_id),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id),
    FOREIGN KEY (corrects_id, owner_id) REFERENCES reconciliations (id, owner_id),
    FOREIGN KEY (cancellation_document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE UNIQUE INDEX reconciliations_one_live ON reconciliations (asset_id, exercise_start) WHERE state <> 'cancelled';
ALTER TABLE charges ADD CONSTRAINT charges_reconciliation_fk
    FOREIGN KEY (reconciliation_id, owner_id) REFERENCES reconciliations (id, owner_id);

-- The reconciliation of one lease: its share, the provisions collected from
-- matched payments, the balance, its settlement and the provision proposed.
CREATE TABLE reconciliation_lines (
    id                       uuid PRIMARY KEY,
    owner_id                 uuid NOT NULL,
    reconciliation_id        uuid NOT NULL,
    lease_id                 uuid NOT NULL,
    days                     integer NOT NULL,
    allocated_cents          bigint NOT NULL,
    provisions_cents         bigint NOT NULL,
    balance_cents            bigint NOT NULL,
    spread                   boolean NOT NULL DEFAULT false,
    instalments              jsonb NOT NULL DEFAULT '[]',
    proposed_provision_cents bigint NOT NULL DEFAULT 0,
    new_provision_cents      bigint NOT NULL DEFAULT 0 CHECK (new_provision_cents >= 0),
    apply_provision          boolean NOT NULL DEFAULT false,
    settlement               settlement_mode NOT NULL DEFAULT 'fee',
    document_id              uuid,
    number                   text NOT NULL DEFAULT '',
    version                  integer NOT NULL DEFAULT 1,
    created_at               timestamptz NOT NULL,
    created_by               text NOT NULL,
    updated_at               timestamptz NOT NULL,
    updated_by               text NOT NULL,
    UNIQUE (id, owner_id),
    UNIQUE (reconciliation_id, lease_id),
    FOREIGN KEY (reconciliation_id, owner_id) REFERENCES reconciliations (id, owner_id),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);

-- What the next fees carry (RENT-SPEC-007 step 7): one posting per balance, or
-- per instalment of a late statement, or for the carry-over of a credit larger
-- than a fee (parent_id). billing.monthly applies the oldest posting due and
-- records the rent call that carried it; a posting carried by a cancelled rent
-- call is carried again by its reissue (Q-79).
CREATE TABLE regularisation_postings (
    id                  uuid PRIMARY KEY,
    owner_id            uuid NOT NULL,
    lease_id            uuid NOT NULL,
    reconciliation_id   uuid NOT NULL,
    line_id             uuid NOT NULL,
    parent_id           uuid,
    reference           text NOT NULL,
    instalment          integer NOT NULL DEFAULT 0,
    of                  integer NOT NULL DEFAULT 0,
    amount_cents        bigint NOT NULL CHECK (amount_cents <> 0),
    due_from            char(7) NOT NULL CHECK (due_from ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
    billing_document_id uuid,
    voided_at           timestamptz,
    created_at          timestamptz NOT NULL,
    created_by          text NOT NULL,
    UNIQUE (id, owner_id),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (reconciliation_id, owner_id) REFERENCES reconciliations (id, owner_id),
    FOREIGN KEY (line_id, owner_id) REFERENCES reconciliation_lines (id, owner_id),
    FOREIGN KEY (parent_id, owner_id) REFERENCES regularisation_postings (id, owner_id),
    FOREIGN KEY (billing_document_id, owner_id) REFERENCES billing_documents (id, owner_id)
);
CREATE INDEX regularisation_postings_lease ON regularisation_postings (lease_id, due_from) WHERE voided_at IS NULL;

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
SELECT tenure_owner_policies('charges');
SELECT tenure_owner_policies('reconciliations');
SELECT tenure_owner_policies('reconciliation_lines');
SELECT tenure_owner_policies('regularisation_postings');
DROP FUNCTION tenure_owner_policies(regclass);

GRANT SELECT, INSERT, UPDATE ON charges, reconciliations, reconciliation_lines, regularisation_postings TO tenure_api;
GRANT SELECT, INSERT, UPDATE ON charges, reconciliations, reconciliation_lines, regularisation_postings TO tenure_jobs;
-- Allocating again replaces the lines of an allocated reconciliation (never
-- an issued one: the service refuses it).
CREATE POLICY api_delete ON reconciliation_lines FOR DELETE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
GRANT DELETE ON reconciliation_lines TO tenure_api, tenure_jobs;

-- Seed of legal_params added since 000004, generated from testdata/params/*.json
-- by go test ./migrations -update.
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('regularisation_supporting_docs_months', '2025-01-01', '6'::jsonb, 'Loi 89-462 art. 23 : pièces justificatives tenues à la disposition du locataire pendant six mois après l’envoi du décompte', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('regularisation_supporting_docs_months', '2026-01-01', '6'::jsonb, 'Loi 89-462 art. 23 : pièces justificatives tenues à la disposition du locataire pendant six mois après l’envoi du décompte', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key IN ('regularisation_supporting_docs_months');
DROP TABLE regularisation_postings;
DROP TABLE reconciliation_lines;
ALTER TABLE charges DROP CONSTRAINT charges_reconciliation_fk;
DROP TABLE reconciliations;
DROP TABLE charges;
DROP TYPE settlement_mode;
DROP TYPE reconciliation_state;
DROP TYPE charge_status;
DROP TYPE charge_source;
DROP TYPE charge_category;
