-- 000021_bank_lines — bank accounts and imported bank statement lines
-- (RENT-SPEC-013, part 14a, Q-123). An account keeps a label and the last
-- four digits of its IBAN, never the whole IBAN. A line is a credit
-- (positive) or a debit (negative) booked on a day, keyed in its account by
-- the bank's own id or a hash, so a statement imported twice adds nothing.
-- Its status is unmatched, matched (part 14b) or ignored. The reconciliation
-- parameter is seeded in legal_params.

-- +goose Up
CREATE TYPE bank_line_status AS ENUM ('unmatched', 'matched', 'ignored');

CREATE TABLE bank_accounts (
    id         uuid PRIMARY KEY,
    owner_id   uuid NOT NULL REFERENCES owners (id),
    label      text NOT NULL CHECK (label <> ''),
    iban_last4 text NOT NULL DEFAULT '' CHECK (iban_last4 ~ '^([0-9A-Z]{4})?$'),
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    version    integer NOT NULL DEFAULT 1,
    UNIQUE (id, owner_id)
);

CREATE TABLE bank_lines (
    id                 uuid PRIMARY KEY,
    owner_id           uuid NOT NULL,
    account_id         uuid NOT NULL,
    booked_on          date NOT NULL,
    amount_cents       bigint NOT NULL CHECK (amount_cents <> 0),
    label              text NOT NULL DEFAULT '',
    reference          text NOT NULL DEFAULT '',
    line_key           text NOT NULL,
    -- The uploaded statement the line came from.
    import_document_id uuid NOT NULL,
    status             bank_line_status NOT NULL DEFAULT 'unmatched',
    note               text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL,
    created_by         text NOT NULL,
    updated_at         timestamptz NOT NULL,
    updated_by         text NOT NULL,
    version            integer NOT NULL DEFAULT 1,
    UNIQUE (account_id, line_key),
    FOREIGN KEY (account_id, owner_id) REFERENCES bank_accounts (id, owner_id),
    FOREIGN KEY (import_document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE INDEX bank_lines_status ON bank_lines (owner_id, status, booked_on);

ALTER TABLE bank_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank_accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE bank_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank_lines FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON bank_accounts FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON bank_accounts FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON bank_accounts FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON bank_accounts TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
CREATE POLICY api_read ON bank_lines FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON bank_lines FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON bank_lines FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON bank_lines TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT, UPDATE ON bank_accounts, bank_lines TO tenure_api;
GRANT SELECT ON bank_accounts, bank_lines TO tenure_jobs;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('bank.reconciliation', '2024-01-01', '{"unmatched_alert_days":10}'::jsonb, 'RENT-SPEC-013 : une ligne bancaire non rapprochée après dix jours apparaît au tableau de bord ; règle de gestion, sans base légale', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'bank.reconciliation';
DROP TABLE bank_lines;
DROP TABLE bank_accounts;
DROP TYPE bank_line_status;
