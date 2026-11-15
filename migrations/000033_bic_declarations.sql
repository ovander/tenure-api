-- 000033_bic_declarations — the BIC declaration of a year (RENT-SPEC-019,
-- LMNP, part 18d, Q-132): the regime chosen, the figures as declared, the
-- grid versions they used. A declaration is issued once and never changed;
-- the years after it start from its figures. The grid versions it used are
-- frozen (bic_grids.frozen_at, the one column tenure_api may update), and a
-- frozen grid takes no new version. The deficit lifetime is seeded in
-- legal_params.

-- +goose Up
CREATE TABLE bic_declarations (
    id         uuid PRIMARY KEY,
    owner_id   uuid NOT NULL REFERENCES owners (id),
    tax_year   integer NOT NULL CHECK (tax_year BETWEEN 1970 AND 9999),
    regime     text NOT NULL CHECK (regime IN ('reel', 'micro')),
    -- the year's result (bic.YearResult), its assets, the grids used.
    result     jsonb NOT NULL,
    assets     jsonb NOT NULL,
    grids      jsonb NOT NULL,
    issued_at  timestamptz NOT NULL,
    issued_by  text NOT NULL,
    UNIQUE (owner_id, tax_year)
);

ALTER TABLE bic_declarations ENABLE ROW LEVEL SECURITY;
ALTER TABLE bic_declarations FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON bic_declarations FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON bic_declarations FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON bic_declarations TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
-- A declaration is recorded, never changed.
GRANT SELECT, INSERT ON bic_declarations TO tenure_api;
GRANT SELECT ON bic_declarations TO tenure_jobs;

-- Freezing a grid version: frozen_at only, set once.
CREATE POLICY api_freeze ON bic_grids FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner' AND frozen_at IS NULL)
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
GRANT UPDATE (frozen_at) ON bic_grids TO tenure_api;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('bic.deficit_years', '2024-01-01', '{"years":10}'::jsonb, 'Déficits de la location meublée non professionnelle : imputables sur les revenus de même nature des dix années suivantes (CGI art. 156, I-1° ter) ; Q-132', 'https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000048825835');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'bic.deficit_years';
REVOKE UPDATE (frozen_at) ON bic_grids FROM tenure_api;
DROP POLICY api_freeze ON bic_grids;
DROP TABLE bic_declarations;
