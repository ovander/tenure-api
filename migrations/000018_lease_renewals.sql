-- 000018_lease_renewals — the re-evaluation of an unfurnished lease's rent at
-- its renewal (RENT-SPEC-010, part 12d, loi 89-462 art. 17-2, Q-120): the
-- owner's proposal letter (PR) is a document of class renewal and a lease
-- document of kind renewal_proposal; a lease_renewals row tracks the
-- proposal from its letter to its reception, the tenant's answer and the
-- yearly steps applied to the rent. The re-evaluation parameters are seeded
-- in legal_params.
-- Not reversible for the enum values: Down drops the table, its type and the
-- seed; 'renewal' stays in document_class and 'renewal_proposal' in
-- lease_document_kind (PostgreSQL cannot drop an enum value), unused.

-- +goose Up
ALTER TYPE document_class ADD VALUE IF NOT EXISTS 'renewal';
ALTER TYPE lease_document_kind ADD VALUE IF NOT EXISTS 'renewal_proposal';
CREATE TYPE renewal_status AS ENUM ('prepared', 'received', 'accepted', 'refused');

CREATE TABLE lease_renewals (
    id            uuid PRIMARY KEY,
    owner_id      uuid NOT NULL REFERENCES owners (id),
    lease_id      uuid NOT NULL,
    -- The first day of the renewed term the proposal aims at.
    renewal_on    date NOT NULL,
    status        renewal_status NOT NULL DEFAULT 'prepared',
    -- The proposal as the owner made it (new rent, references) and what
    -- tenure computed from it (cap, schedule of steps), as of the letter.
    proposal      jsonb NOT NULL,
    terms         jsonb NOT NULL,
    document_id   uuid NOT NULL,
    received_on   date,
    delivery      notice_delivery,
    tracking_ref  text NOT NULL DEFAULT '',
    answered_on   date,
    -- How many steps of the schedule were applied to the lease's rent.
    steps_applied integer NOT NULL DEFAULT 0 CHECK (steps_applied >= 0),
    created_at    timestamptz NOT NULL,
    created_by    text NOT NULL,
    updated_at    timestamptz NOT NULL,
    updated_by    text NOT NULL,
    version       integer NOT NULL DEFAULT 1,
    UNIQUE (lease_id, renewal_on),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id),
    CHECK (status = 'prepared' OR (received_on IS NOT NULL AND delivery IS NOT NULL)),
    CHECK ((status IN ('accepted', 'refused')) = (answered_on IS NOT NULL)),
    CHECK (answered_on IS NULL OR answered_on >= received_on),
    CHECK (status = 'accepted' OR steps_applied = 0)
);

ALTER TABLE lease_renewals ENABLE ROW LEVEL SECURITY;
ALTER TABLE lease_renewals FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON lease_renewals FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_insert ON lease_renewals FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON lease_renewals FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON lease_renewals TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT, UPDATE ON lease_renewals TO tenure_api;
GRANT SELECT ON lease_renewals TO tenure_jobs;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('renewal.reevaluation', '2024-01-01', '{"increase_threshold_bp":1000,"lead_months":6,"long_term_months":72,"min_references":3,"min_references_large_agglomeration":6,"steps_long":6,"steps_short":3,"tense_zone_cap_bp":5000}'::jsonb, 'Loi 89-462 art. 17-2 (proposition six mois au moins avant le terme, trois références, six dans les agglomérations de plus d’un million d’habitants ; hausse par tiers ou par sixième selon la durée du contrat, par sixième au-delà de 10 %) ; décret annuel d’évolution des loyers en zone tendue (hausse limitée à la moitié de l’écart avec les références) ; à vérifier sur Légifrance avant la barrière de la phase 2', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'renewal.reevaluation';
DROP TABLE lease_renewals;
DROP TYPE renewal_status;
