-- 000031_bic_depreciation — furnished letting under the réel (RENT-SPEC-019,
-- LMNP, part 18b, Q-132): the component grid of an asset, versioned, and
-- the furniture and works capitalised on it. A grid version is never
-- changed: a change records the next version. A version a declaration used
-- is frozen (frozen_at, set in part 18c). The defaults (grid, furniture
-- duration, expense threshold, acquisition costs) are seeded in
-- legal_params.

-- +goose Up
CREATE TABLE bic_grids (
    id                      uuid PRIMARY KEY,
    owner_id                uuid NOT NULL REFERENCES owners (id),
    asset_id                uuid NOT NULL,
    version                 integer NOT NULL CHECK (version > 0),
    -- the day the asset was first let furnished: depreciation starts then.
    in_service_on           date NOT NULL,
    price_cents             bigint NOT NULL CHECK (price_cents >= 0),
    acquisition_costs_cents bigint NOT NULL DEFAULT 0 CHECK (acquisition_costs_cents >= 0),
    costs_expensed          boolean NOT NULL DEFAULT false,
    basis_cents             bigint NOT NULL CHECK (basis_cents >= 0),
    -- [{component, share_bp, months}], shares adding up to 10 000.
    lines                   jsonb NOT NULL,
    reason                  text NOT NULL DEFAULT '',
    frozen_at               timestamptz,
    created_at              timestamptz NOT NULL,
    created_by              text NOT NULL,
    UNIQUE (asset_id, version),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id)
);

CREATE TABLE bic_fixed_assets (
    id              uuid PRIMARY KEY,
    owner_id        uuid NOT NULL REFERENCES owners (id),
    asset_id        uuid NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('furniture', 'works')),
    -- works only: repair, improvement or construction (Q-130).
    works           text CHECK (works IN ('repair', 'improvement', 'construction')),
    label           text NOT NULL CHECK (label <> ''),
    cost_cents      bigint NOT NULL CHECK (cost_cents >= 0),
    in_service_on   date NOT NULL,
    months          integer NOT NULL CHECK (months BETWEEN 1 AND 1200),
    -- asset (depreciated) or expense; overridden says the owner forced it.
    treatment       text NOT NULL CHECK (treatment IN ('asset', 'expense')),
    overridden      boolean NOT NULL DEFAULT false,
    override_reason text NOT NULL DEFAULT '',
    document_id     uuid,
    disposed_on     date,
    created_at      timestamptz NOT NULL,
    created_by      text NOT NULL,
    updated_at      timestamptz NOT NULL,
    updated_by      text NOT NULL,
    version         integer NOT NULL DEFAULT 1,
    CHECK ((kind = 'works') = (works IS NOT NULL)),
    CHECK (NOT overridden OR override_reason <> ''),
    CHECK (disposed_on IS NULL OR disposed_on >= in_service_on),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE INDEX bic_fixed_assets_asset ON bic_fixed_assets (owner_id, asset_id);

ALTER TABLE bic_grids ENABLE ROW LEVEL SECURITY;
ALTER TABLE bic_grids FORCE ROW LEVEL SECURITY;
ALTER TABLE bic_fixed_assets ENABLE ROW LEVEL SECURITY;
ALTER TABLE bic_fixed_assets FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON bic_grids FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON bic_grids FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON bic_grids TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
CREATE POLICY api_read ON bic_fixed_assets FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON bic_fixed_assets FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON bic_fixed_assets FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON bic_fixed_assets TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
-- A grid version is recorded, never changed.
GRANT SELECT, INSERT ON bic_grids TO tenure_api;
GRANT SELECT, INSERT, UPDATE ON bic_fixed_assets TO tenure_api;
GRANT SELECT ON bic_grids, bic_fixed_assets TO tenure_jobs;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('bic.acquisition_costs', '2024-01-01', '{"capitalise":true}'::jsonb, 'RENT-SPEC-019, Q-132 : valeur par défaut retenue en attendant la validation de l’expert-comptable (BIC, location meublée au réel ; CGI art. 39 et 39 C, PCG art. 214-9 sur l’approche par composants) ; frais d’acquisition immobilisés par défaut (option de déduction immédiate, CGI art. 38 quinquies)', 'https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000006303638');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('bic.components', '2024-01-01', '{"lines":[{"component":"land","months":0,"share_bp":1500},{"component":"structure","months":600,"share_bp":4000},{"component":"roof","months":300,"share_bp":1000},{"component":"facade","months":300,"share_bp":1000},{"component":"technical","months":240,"share_bp":1500},{"component":"fittings","months":180,"share_bp":1000}]}'::jsonb, 'RENT-SPEC-019, Q-132 : valeur par défaut retenue en attendant la validation de l’expert-comptable (BIC, location meublée au réel ; CGI art. 39 et 39 C, PCG art. 214-9 sur l’approche par composants) ; grille de composants : quote-part et durée, terrain non amorti', 'https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000044986768');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('bic.furniture', '2024-01-01', '{"months":84}'::jsonb, 'RENT-SPEC-019, Q-132 : valeur par défaut retenue en attendant la validation de l’expert-comptable (BIC, location meublée au réel ; CGI art. 39 et 39 C, PCG art. 214-9 sur l’approche par composants) ; durée d’amortissement du mobilier', 'https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000044986768');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('bic.small_equipment', '2024-01-01', '{"threshold_cents":50000}'::jsonb, 'Tolérance administrative : petit matériel et mobilier de faible valeur passés en charge sous 500 € HT par élément (BOI-BIC-CHG-20-30-10) ; Q-132', 'https://bofip.impots.gouv.fr/bofip/2101-PGP.html');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key IN ('bic.components', 'bic.furniture', 'bic.small_equipment', 'bic.acquisition_costs');
DROP TABLE bic_fixed_assets;
DROP TABLE bic_grids;
