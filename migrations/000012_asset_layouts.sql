-- 000012_asset_layouts — the version of an asset's layout (RENT-INV-001 §4,
-- inventory slice 3): one row per asset whose rooms have been seeded or
-- edited, carrying the version a layout PUT checks with If-Match. Kept apart
-- from assets so an asset edit and a layout edit never overwrite each other.
-- Reversible: Down drops the table.

-- +goose Up
CREATE TABLE asset_layouts (
    asset_id   uuid PRIMARY KEY,
    owner_id   uuid NOT NULL REFERENCES owners (id),
    version    integer NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id)
);
ALTER TABLE asset_layouts ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_layouts FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON asset_layouts FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_insert ON asset_layouts FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON asset_layouts FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON asset_layouts TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT, UPDATE ON asset_layouts TO tenure_api;
GRANT SELECT ON asset_layouts TO tenure_jobs;

-- +goose Down
DROP TABLE asset_layouts;
