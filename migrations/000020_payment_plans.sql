-- 000020_payment_plans — payment plans of unpaid rent (RENT-SPEC-011, part
-- 13b, Q-122): the rent calls overdue on the day a plan is agreed, their
-- debt and the instalments. Where a plan stands (respected, broken,
-- completed) is computed on read; a respected plan suspends the recovery.
-- Only the owner's cancellation changes a plan. The plan parameters are
-- seeded in legal_params.

-- +goose Up
CREATE TABLE payment_plans (
    id           uuid PRIMARY KEY,
    owner_id     uuid NOT NULL REFERENCES owners (id),
    lease_id     uuid NOT NULL,
    agreed_on    date NOT NULL,
    debt_cents   bigint NOT NULL CHECK (debt_cents > 0),
    -- The billing documents (rent calls) the plan covers.
    calls        jsonb NOT NULL,
    -- [{due_on, amount_cents}], adding up to debt_cents.
    instalments  jsonb NOT NULL,
    -- The signed agreement, if uploaded.
    document_id  uuid,
    cancelled_on date,
    created_at   timestamptz NOT NULL,
    created_by   text NOT NULL,
    updated_at   timestamptz NOT NULL,
    updated_by   text NOT NULL,
    version      integer NOT NULL DEFAULT 1,
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id),
    CHECK (cancelled_on IS NULL OR cancelled_on >= agreed_on)
);
CREATE INDEX payment_plans_lease ON payment_plans (lease_id, agreed_on);

ALTER TABLE payment_plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_plans FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON payment_plans FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_insert ON payment_plans FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON payment_plans FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON payment_plans TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT, UPDATE ON payment_plans TO tenure_api;
GRANT SELECT ON payment_plans TO tenure_jobs;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('recovery.plan', '2024-01-01', '{"grace_days":5,"max_instalments":36}'::jsonb, 'Plan d’apurement amiable (RENT-SPEC-011) : délai de grâce d’une échéance et nombre maximal d’échéances, valeurs par défaut sans base légale ; le juge peut accorder jusqu’à trois ans de délais de paiement (loi 89-462 art. 24 V) ; à vérifier sur Légifrance avant la barrière de la phase 2', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'recovery.plan';
DROP TABLE payment_plans;
