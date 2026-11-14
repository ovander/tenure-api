-- 000028_maintenance_contracts — recurring maintenance of an asset
-- (RENT-SPEC-015, part 17b, Q-131): the boiler's yearly servicing, the
-- chimney's sweeping, any other periodic check, with their visits and
-- certificates. The latest visit gives the next one, which the obligations
-- calendar shows. The legal rhythm and who must have each kind done are
-- seeded in legal_params.

-- +goose Up
CREATE TABLE maintenance_contracts (
    id              uuid PRIMARY KEY,
    owner_id        uuid NOT NULL REFERENCES owners (id),
    asset_id        uuid NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('boiler', 'chimney', 'other')),
    title           text NOT NULL CHECK (title <> ''),
    contractor_id   uuid,
    interval_months integer NOT NULL CHECK (interval_months BETWEEN 1 AND 120),
    -- tenant or owner: who must have the maintenance done.
    responsible     text NOT NULL CHECK (responsible IN ('tenant', 'owner')),
    -- the first visit is due on first_due_on; each visit then sets the next one.
    first_due_on    date NOT NULL,
    end_on          date,
    document_id     uuid,
    notes           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL,
    created_by      text NOT NULL,
    updated_at      timestamptz NOT NULL,
    updated_by      text NOT NULL,
    version         integer NOT NULL DEFAULT 1,
    UNIQUE (id, owner_id),
    CHECK (end_on IS NULL OR end_on >= first_due_on),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id),
    FOREIGN KEY (contractor_id, owner_id) REFERENCES contractors (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE INDEX maintenance_contracts_asset ON maintenance_contracts (owner_id, asset_id);

CREATE TABLE maintenance_visits (
    id          uuid PRIMARY KEY,
    owner_id    uuid NOT NULL REFERENCES owners (id),
    contract_id uuid NOT NULL,
    done_on     date NOT NULL,
    document_id uuid,
    notes       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL,
    created_by  text NOT NULL,
    FOREIGN KEY (contract_id, owner_id) REFERENCES maintenance_contracts (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE INDEX maintenance_visits_contract ON maintenance_visits (owner_id, contract_id, done_on);

ALTER TABLE maintenance_contracts ENABLE ROW LEVEL SECURITY;
ALTER TABLE maintenance_contracts FORCE ROW LEVEL SECURITY;
ALTER TABLE maintenance_visits ENABLE ROW LEVEL SECURITY;
ALTER TABLE maintenance_visits FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON maintenance_contracts FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON maintenance_contracts FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON maintenance_contracts FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON maintenance_contracts TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
CREATE POLICY api_read ON maintenance_visits FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON maintenance_visits FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON maintenance_visits TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT, UPDATE ON maintenance_contracts TO tenure_api;
-- A visit is recorded, never changed: its certificate is evidence.
GRANT SELECT, INSERT ON maintenance_visits TO tenure_api;
GRANT SELECT ON maintenance_contracts, maintenance_visits TO tenure_jobs;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('maintenance.recurring', '2024-01-01', '{"kinds":{"boiler":{"interval_months":12,"responsible":"tenant"},"chimney":{"interval_months":6,"responsible":"tenant"}}}'::jsonb, 'Chaudière : entretien annuel à la charge de l’occupant (décret n° 2009-649 du 9 juin 2009, arrêté du 15 septembre 2009 ; décret n° 87-712, menues réparations). Ramonage : règlement sanitaire départemental type, art. 31 — une ou deux fois par an selon le combustible et le département ; la lecture prudente retient deux fois par an (Q-131)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000020722484');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'maintenance.recurring';
DROP TABLE maintenance_visits;
DROP TABLE maintenance_contracts;
