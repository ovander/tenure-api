-- 000019_recovery_steps — the recovery of unpaid rent (RENT-SPEC-011, part
-- 13a, Q-121): each step (formal notice, surety's information, insurer's
-- claim, commandement de payer, its notice to the surety, court) is a dated,
-- immutable event on the lease, with its letter or deed. The formal notice
-- (MD) and the surety's information (IC) are documents of class recovery,
-- numbered in series of their own. The escalation parameters are seeded in
-- legal_params.
-- Not reversible for the enum value: Down drops the table, its type and the
-- seed; 'recovery' stays in document_class (PostgreSQL cannot drop an enum
-- value), unused.

-- +goose Up
ALTER TYPE document_class ADD VALUE IF NOT EXISTS 'recovery';
CREATE TYPE recovery_step_kind AS ENUM ('formal_notice', 'surety_notice', 'insurance_claim', 'commandement', 'surety_commandement', 'court');
CREATE TYPE recovery_delivery AS ENUM ('registered_letter', 'bailiff', 'hand_delivery', 'email');

CREATE TABLE recovery_steps (
    id                uuid PRIMARY KEY,
    owner_id          uuid NOT NULL REFERENCES owners (id),
    lease_id          uuid NOT NULL,
    kind              recovery_step_kind NOT NULL,
    done_on           date NOT NULL,
    -- What the lease owed on that day, as tenure computed it.
    outstanding_cents bigint NOT NULL CHECK (outstanding_cents >= 0),
    -- The surety informed, for surety_notice and surety_commandement.
    guarantee_id      uuid,
    document_id       uuid,
    delivery          recovery_delivery,
    tracking_ref      text NOT NULL DEFAULT '',
    -- The insurer's deadline, as the owner reads it in the policy.
    deadline_on       date,
    note              text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL,
    created_by        text NOT NULL,
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id),
    CHECK ((kind IN ('surety_notice', 'surety_commandement')) = (guarantee_id IS NOT NULL)),
    CHECK (kind NOT IN ('formal_notice', 'surety_notice') OR document_id IS NOT NULL)
);
CREATE INDEX recovery_steps_lease ON recovery_steps (lease_id, done_on);

-- +goose StatementBegin
CREATE FUNCTION recovery_steps_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'a recovery step is immutable' USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER recovery_steps_immutable BEFORE UPDATE OR DELETE ON recovery_steps
    FOR EACH ROW EXECUTE FUNCTION recovery_steps_immutable();

ALTER TABLE recovery_steps ENABLE ROW LEVEL SECURITY;
ALTER TABLE recovery_steps FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON recovery_steps FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_insert ON recovery_steps FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON recovery_steps TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT ON recovery_steps TO tenure_api;
GRANT SELECT ON recovery_steps TO tenure_jobs;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('recovery.escalation', '2024-01-01', '{"commandement_after_days":15,"formal_notice_after_days":15,"former_resolution_months":2,"insurance_claim_after_days":30,"resolution_weeks":6,"resolution_weeks_from":"2023-07-29","surety_commandement_days":15,"surety_notice_after_months":1}'::jsonb, 'Loi 89-462 art. 24 I modifié par la loi 2023-668 art. 10 (clause résolutoire : six semaines après un commandement de payer resté infructueux, pour un bail conclu ou renouvelé depuis le 29 juillet 2023 — Cass. avis 13 juin 2024 n° 24-70.002 ; deux mois auparavant) ; commandement signifié à la caution dans les quinze jours ; code civil art. 2303 (caution personne physique informée du premier incident de paiement non régularisé dans le mois) ; délais de mise en demeure, de commandement et de déclaration à l’assureur : valeurs par défaut, sans base légale, à adapter au contrat d’assurance ; à vérifier sur Légifrance avant la barrière de la phase 2', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'recovery.escalation';
DROP TABLE recovery_steps;
DROP FUNCTION recovery_steps_immutable();
DROP TYPE recovery_delivery;
DROP TYPE recovery_step_kind;
