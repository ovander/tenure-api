-- 000017_lease_notices — the end of a lease (RENT-SPEC-010, part 12a,
-- Q-118): a notice given by the tenant or by the owner, received on a date,
-- from which tenure computes the lease's end; the owner's notice letter (CG)
-- is a document of class notice and a lease document of kind owner_notice.
-- One notice per lease, immutable once recorded. The notice periods and the
-- legal grounds are seeded in legal_params.
-- Not reversible for the enum values: Down drops the table, the trigger and
-- the seed; 'notice' stays in document_class and 'owner_notice' in
-- lease_document_kind (PostgreSQL cannot drop an enum value), unused.

-- +goose Up
ALTER TYPE document_class ADD VALUE IF NOT EXISTS 'notice';
ALTER TYPE lease_document_kind ADD VALUE IF NOT EXISTS 'owner_notice';
CREATE TYPE notice_given_by AS ENUM ('tenant', 'owner');
CREATE TYPE notice_delivery AS ENUM ('registered_letter', 'bailiff', 'hand_delivery');

CREATE TABLE lease_notices (
    id           uuid PRIMARY KEY,
    owner_id     uuid NOT NULL REFERENCES owners (id),
    lease_id     uuid NOT NULL,
    given_by     notice_given_by NOT NULL,
    -- The tenant's reduced-notice ground (empty for the standard period), or
    -- the owner's ground: sale, repossession, legitimate.
    reason       text NOT NULL DEFAULT '',
    received_on  date NOT NULL,
    months       integer NOT NULL CHECK (months > 0),
    end_on       date NOT NULL CHECK (end_on >= received_on),
    delivery     notice_delivery NOT NULL,
    sent_on      date,
    tracking_ref text NOT NULL DEFAULT '',
    -- What the ground states (the sale offer, the beneficiary, the grounds,
    -- the protected-tenant answer), as recorded.
    ground       jsonb NOT NULL DEFAULT '{}',
    -- The owner's letter (class notice), or the tenant's letter uploaded.
    document_id  uuid,
    created_at   timestamptz NOT NULL,
    created_by   text NOT NULL,
    UNIQUE (lease_id),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id),
    CHECK (given_by = 'tenant' OR document_id IS NOT NULL),
    CHECK (sent_on IS NULL OR sent_on <= received_on)
);

-- +goose StatementBegin
CREATE FUNCTION lease_notices_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'a lease notice is immutable' USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER lease_notices_immutable BEFORE UPDATE OR DELETE ON lease_notices
    FOR EACH ROW EXECUTE FUNCTION lease_notices_immutable();

ALTER TABLE lease_notices ENABLE ROW LEVEL SECURITY;
ALTER TABLE lease_notices FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON lease_notices FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_insert ON lease_notices FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON lease_notices TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT ON lease_notices TO tenure_api;
GRANT SELECT ON lease_notices TO tenure_jobs;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('notice.reasons', '2024-01-01', '{"owner":["sale","repossession","legitimate"],"repossession_beneficiaries":["owner","spouse","pacs_partner","concubin","ascendant","descendant","spouse_ascendant","spouse_descendant"],"tenant_reduced":["tense_zone","first_job","job_transfer","job_loss","new_job_after_loss","health","rsa","aah","social_housing"]}'::jsonb, 'Loi 89-462 art. 15 I : préavis réduit (zone tendue, premier emploi, mutation, perte d’emploi, nouvel emploi après une perte d’emploi, état de santé, RSA, AAH, logement social) ; congé du bailleur pour reprise, vente ou motif légitime et sérieux ; bénéficiaires d’une reprise ; à vérifier sur Légifrance avant la barrière de la phase 2', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('notice_months.owner', '2024-01-01', '{"furnished":3,"unfurnished":6}'::jsonb, 'Loi 89-462 art. 15 I (congé du bailleur au moins six mois avant le terme) et art. 25-8 (meublé : trois mois) ; à vérifier sur Légifrance avant la barrière de la phase 2', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('notice_months.tenant', '2024-01-01', '{"furnished":1,"mobility":1,"reduced":1,"unfurnished":3}'::jsonb, 'Loi 89-462 art. 15 I (préavis du locataire : trois mois, un mois dans les cas listés), art. 25-8 (meublé : un mois) et art. 25-12 (bail mobilité : un mois) ; à vérifier sur Légifrance avant la barrière de la phase 2', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key IN ('notice_months.tenant', 'notice_months.owner', 'notice.reasons');
DROP TABLE lease_notices;
DROP FUNCTION lease_notices_immutable();
DROP TYPE notice_delivery;
DROP TYPE notice_given_by;
