-- 000003_payments — payments, matching, reminders (RENT-BE-001 §2, §3, §5; slice 4).
-- Reversible except for the enum value partial_receipt: PostgreSQL cannot drop
-- an enum value, so Down leaves it in billing_kind, unused (explicitly marked).

-- +goose Up
ALTER TYPE billing_kind ADD VALUE IF NOT EXISTS 'partial_receipt';
CREATE TYPE payment_method AS ENUM ('transfer', 'direct_debit', 'cheque', 'cash', 'card', 'caf', 'other');

CREATE TABLE payments (
    id           uuid PRIMARY KEY,
    owner_id     uuid NOT NULL REFERENCES owners (id),
    lease_id     uuid NOT NULL,
    paid_on      date NOT NULL,
    amount_cents bigint NOT NULL CHECK (amount_cents > 0),
    method       payment_method NOT NULL,
    reference    text NOT NULL DEFAULT '',
    bank_line_id text,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL,
    created_by   text NOT NULL,
    updated_at   timestamptz NOT NULL,
    updated_by   text NOT NULL,
    UNIQUE (id, owner_id),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id)
);
CREATE INDEX payments_lease ON payments (lease_id, paid_on);

-- The part of a payment applied to one rent call (partial matching). A
-- cancel-and-reissue moves the matches to the new rent call and removes only
-- the part the new rent call cannot take (it becomes unapplied again).
CREATE TABLE payment_matches (
    id                  uuid PRIMARY KEY,
    owner_id            uuid NOT NULL,
    payment_id          uuid NOT NULL,
    billing_document_id uuid NOT NULL,
    amount_cents        bigint NOT NULL CHECK (amount_cents > 0),
    created_at          timestamptz NOT NULL,
    created_by          text NOT NULL,
    FOREIGN KEY (payment_id, owner_id) REFERENCES payments (id, owner_id),
    FOREIGN KEY (billing_document_id, owner_id) REFERENCES billing_documents (id, owner_id)
);
CREATE INDEX payment_matches_payment ON payment_matches (payment_id);
CREATE INDEX payment_matches_document ON payment_matches (billing_document_id);

-- One reminder per rent call (billing.reminders, Q-62).
CREATE TABLE billing_reminders (
    id                  uuid PRIMARY KEY,
    owner_id            uuid NOT NULL,
    billing_document_id uuid NOT NULL UNIQUE,
    sent_on             date NOT NULL,
    outstanding_cents   bigint NOT NULL,
    recipients          integer NOT NULL,
    failures            integer NOT NULL,
    created_at          timestamptz NOT NULL,
    FOREIGN KEY (billing_document_id, owner_id) REFERENCES billing_documents (id, owner_id)
);

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
SELECT tenure_owner_policies('payments');
SELECT tenure_owner_policies('payment_matches');
SELECT tenure_owner_policies('billing_reminders');
DROP FUNCTION tenure_owner_policies(regclass);
CREATE POLICY api_delete ON payment_matches FOR DELETE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');

GRANT SELECT, INSERT, UPDATE ON payments, payment_matches TO tenure_api;
GRANT DELETE ON payment_matches TO tenure_api;
GRANT SELECT ON billing_reminders TO tenure_api;
GRANT SELECT ON payments, payment_matches TO tenure_jobs;
GRANT SELECT, INSERT ON billing_reminders TO tenure_jobs;

-- seed:legal_params:begin
-- seed:legal_params:end

-- +goose Down
DROP TABLE billing_reminders;
DROP TABLE payment_matches;
DROP TABLE payments;
DROP TYPE payment_method;
-- billing_kind keeps the value partial_receipt (see the header).
