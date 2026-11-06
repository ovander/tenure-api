-- 000016_deposit_settlements — the deposit restitution (RENT-INV-001 slice 9,
-- RENT-SPEC-008): the document class of the RD statement, financial for the
-- accountant (INV-OD-11); one settlement row per statement, the first one and
-- the release of a holdback once the building's accounts are approved; the
-- deductions frozen once the first statement is issued.
-- Not reversible for the enum value: Down drops the table and the triggers;
-- 'restitution' stays in document_class (PostgreSQL cannot drop an enum
-- value), unused.

-- +goose Up
ALTER TYPE document_class ADD VALUE IF NOT EXISTS 'restitution';
CREATE TYPE settlement_kind AS ENUM ('initial', 'release');

CREATE TABLE deposit_settlements (
    id          uuid PRIMARY KEY,
    owner_id    uuid NOT NULL REFERENCES owners (id),
    lease_id    uuid NOT NULL,
    kind        settlement_kind NOT NULL,
    settled_on  date NOT NULL,
    deadline    date NOT NULL,
    -- The statement's figures as issued: deposit, each deduction, arrears,
    -- holdback, penalty, balance (the RD snapshot).
    figures     jsonb NOT NULL,
    balance_cents bigint NOT NULL,
    holdback_cents bigint NOT NULL DEFAULT 0 CHECK (holdback_cents >= 0),
    document_id uuid NOT NULL,
    created_at  timestamptz NOT NULL,
    created_by  text NOT NULL,
    UNIQUE (lease_id, kind),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);

-- +goose StatementBegin
CREATE FUNCTION deposit_settlements_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'a deposit settlement is immutable' USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER deposit_settlements_immutable BEFORE UPDATE OR DELETE ON deposit_settlements
    FOR EACH ROW EXECUTE FUNCTION deposit_settlements_immutable();

-- The deductions no longer change once the first statement lists them.
-- +goose StatementBegin
CREATE FUNCTION deposit_deductions_frozen() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    l uuid := CASE WHEN TG_OP = 'DELETE' THEN OLD.lease_id ELSE NEW.lease_id END;
BEGIN
    IF EXISTS (SELECT 1 FROM deposit_settlements WHERE lease_id = l) THEN
        RAISE EXCEPTION 'the deposit of this lease is settled' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER deposit_deductions_frozen BEFORE INSERT OR UPDATE OR DELETE ON deposit_deductions
    FOR EACH ROW EXECUTE FUNCTION deposit_deductions_frozen();

ALTER TABLE deposit_settlements ENABLE ROW LEVEL SECURITY;
ALTER TABLE deposit_settlements FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON deposit_settlements FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_insert ON deposit_settlements FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON deposit_settlements TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT ON deposit_settlements TO tenure_api;
GRANT SELECT ON deposit_settlements TO tenure_jobs;

-- +goose Down
DROP TRIGGER deposit_deductions_frozen ON deposit_deductions;
DROP FUNCTION deposit_deductions_frozen();
DROP TABLE deposit_settlements;
DROP FUNCTION deposit_settlements_immutable();
DROP TYPE settlement_kind;
