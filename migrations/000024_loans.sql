-- 000024_loans — the loans that finance an asset and their amortisation
-- table (RENT-SPEC-014, part 15a, Q-126). A loan is computed from its terms
-- (fixed rate, constant monthly instalments) or typed in from the bank's
-- table; either way its instalments are stored, each split between
-- principal, interest and insurance, so the tax summary reads the interest
-- and insurance due in the income year for line 250. Editing a loan replaces
-- its instalments. No legal parameter: a schedule is the contract's own.

-- +goose Up
CREATE TYPE loan_source AS ENUM ('computed', 'imported');

CREATE TABLE loans (
    id              uuid PRIMARY KEY,
    owner_id        uuid NOT NULL REFERENCES owners (id),
    asset_id        uuid NOT NULL,
    label           text NOT NULL CHECK (label <> ''),
    lender          text NOT NULL DEFAULT '',
    principal_cents bigint NOT NULL CHECK (principal_cents > 0),
    -- The nominal annual rate in parts per million (3.45 % = 34500); null
    -- for a table typed in without it.
    annual_rate_ppm bigint CHECK (annual_rate_ppm >= 0 AND annual_rate_ppm < 1000000),
    term_months     integer NOT NULL CHECK (term_months > 0),
    first_due_on    date NOT NULL,
    insurance_cents bigint NOT NULL DEFAULT 0 CHECK (insurance_cents >= 0),
    source          loan_source NOT NULL,
    -- The bank's offer or table, uploaded through POST /documents.
    document_id     uuid,
    created_at      timestamptz NOT NULL,
    created_by      text NOT NULL,
    updated_at      timestamptz NOT NULL,
    updated_by      text NOT NULL,
    version         integer NOT NULL DEFAULT 1,
    UNIQUE (id, owner_id),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE INDEX loans_asset ON loans (owner_id, asset_id);

CREATE TABLE loan_instalments (
    id              uuid PRIMARY KEY,
    owner_id        uuid NOT NULL,
    loan_id         uuid NOT NULL,
    seq             integer NOT NULL CHECK (seq > 0),
    due_on          date NOT NULL,
    principal_cents bigint NOT NULL CHECK (principal_cents >= 0),
    interest_cents  bigint NOT NULL CHECK (interest_cents >= 0),
    insurance_cents bigint NOT NULL CHECK (insurance_cents >= 0),
    remaining_cents bigint NOT NULL CHECK (remaining_cents >= 0),
    UNIQUE (loan_id, seq),
    FOREIGN KEY (loan_id, owner_id) REFERENCES loans (id, owner_id)
);
CREATE INDEX loan_instalments_due ON loan_instalments (owner_id, due_on);

ALTER TABLE loans ENABLE ROW LEVEL SECURITY;
ALTER TABLE loans FORCE ROW LEVEL SECURITY;
ALTER TABLE loan_instalments ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_instalments FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON loans FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON loans FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON loans FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON loans TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
CREATE POLICY api_read ON loan_instalments FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON loan_instalments FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_delete ON loan_instalments FOR DELETE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON loan_instalments TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT, UPDATE ON loans TO tenure_api;
GRANT SELECT, INSERT, DELETE ON loan_instalments TO tenure_api;
GRANT SELECT ON loans, loan_instalments TO tenure_jobs;

-- +goose Down
DROP TABLE loan_instalments;
DROP TABLE loans;
DROP TYPE loan_source;
