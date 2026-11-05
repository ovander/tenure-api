-- 000011_inventories — the structured inventory of RENT-INV-001 §3 (phase 2,
-- inventory slice 2): asset layouts, inventory templates, the inventory header
-- with its items, meters, keys, photos, signatures and completions, vétusté
-- grids and deposit deductions; leases.vetuste_grid_id and
-- assets.collective_building; the restitution and window parameters.
--
-- The phase 1 rows of inventories become the header of an uploaded inventory:
-- the new columns state and mode are added with the defaults 'issued' and
-- 'upload', which DDL writes into every existing row (tenure_migrate sees no
-- owner's rows, so no UPDATE could), and the defaults are then dropped so a new
-- row must state both. items is renamed legacy_items, kept as it was.
--
-- Once an inventory is issued, its row and its items, meters, keys, photos and
-- signatures are immutable (trigger). A completion is made on an issued entry
-- and is not a child under that rule.
--
-- Reversible while no phase 2 row exists: Down restores the NOT NULL of
-- done_on and document_id, and refuses (fails) when a draft lacks either.

-- +goose Up
CREATE TYPE inventory_mode AS ENUM ('amicable', 'bailiff', 'upload');
CREATE TYPE inventory_state AS ENUM ('draft', 'scheduled', 'in_progress', 'completed', 'signed', 'issued');
-- INV-OD-04: five ordinal levels, works recorded apart.
CREATE TYPE item_condition AS ENUM ('new', 'good', 'fair', 'poor', 'broken');
CREATE TYPE item_works AS ENUM ('ok', 'ko', 'n/a');
CREATE TYPE room_kind AS ENUM ('entry', 'living', 'kitchen', 'bedroom', 'bathroom', 'wc', 'storage', 'outside',
    'garage', 'box', 'other');
CREATE TYPE element_category AS ENUM ('floor', 'walls', 'ceiling', 'joinery', 'electrical', 'plumbing', 'heating',
    'appliance', 'furniture', 'other');
CREATE TYPE meter_kind AS ENUM ('elec_peak', 'elec_offpeak', 'elec_base', 'gas', 'water_cold', 'water_hot',
    'heating_energy');
CREATE TYPE key_kind AS ENUM ('key', 'badge', 'remote', 'card', 'code');
CREATE TYPE signature_party AS ENUM ('owner', 'tenant', 'representative');
CREATE TYPE signature_method AS ENUM ('esign', 'wet_scan');
CREATE TYPE completion_reason AS ENUM ('ten_day', 'heating');
CREATE TYPE completion_decision AS ENUM ('pending', 'accepted', 'refused', 'partial');
CREATE TYPE deduction_basis AS ENUM ('quote', 'invoice');

-- A collective building allows the restitution holdback (loi 89-462 art. 22).
ALTER TABLE assets ADD COLUMN collective_building boolean NOT NULL DEFAULT false;

-- The asset's layout, kept between leases (RENT-INV-001 §4).
CREATE TABLE asset_rooms (
    id                    uuid PRIMARY KEY,
    owner_id              uuid NOT NULL REFERENCES owners (id),
    asset_id              uuid NOT NULL,
    position              integer NOT NULL CHECK (position > 0),
    kind                  room_kind NOT NULL,
    label                 text NOT NULL CHECK (label <> ''),
    surface_hundredths_m2 integer CHECK (surface_hundredths_m2 IS NULL OR surface_hundredths_m2 > 0),
    version               integer NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    created_by            text NOT NULL,
    updated_at            timestamptz NOT NULL,
    updated_by            text NOT NULL,
    UNIQUE (id, owner_id),
    UNIQUE (asset_id, position) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id)
);

CREATE TABLE asset_elements (
    id           uuid PRIMARY KEY,
    owner_id     uuid NOT NULL REFERENCES owners (id),
    room_id      uuid NOT NULL,
    position     integer NOT NULL CHECK (position > 0),
    category     element_category NOT NULL,
    label        text NOT NULL CHECK (label <> ''),
    quantity     integer NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    installed_on date,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL,
    created_by   text NOT NULL,
    updated_at   timestamptz NOT NULL,
    updated_by   text NOT NULL,
    UNIQUE (id, owner_id),
    UNIQUE (room_id, position) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (room_id, owner_id) REFERENCES asset_rooms (id, owner_id) ON DELETE CASCADE
);

-- Templates seed a new layout. owner_id null: a system template, read by every
-- scope, written only by migrations. The structure carries the rooms and the
-- furniture a furnished dwelling adds (internal/inventory.Template).
CREATE TABLE inventory_templates (
    id         uuid PRIMARY KEY,
    owner_id   uuid REFERENCES owners (id),
    asset_kind asset_kind NOT NULL,
    version    integer NOT NULL CHECK (version > 0),
    structure  jsonb NOT NULL,
    source     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    UNIQUE NULLS NOT DISTINCT (owner_id, asset_kind, version)
);

-- Vétusté grids, agreed by the parties and annexed to a lease (décret
-- 2016-382 art. 4). Tenure ships none (INV-OD-08). Rates in basis points.
CREATE TABLE vetuste_grids (
    id         uuid PRIMARY KEY,
    owner_id   uuid NOT NULL REFERENCES owners (id),
    name       text NOT NULL CHECK (name <> ''),
    source     text NOT NULL CHECK (source <> ''),
    lines      jsonb NOT NULL DEFAULT '[]',
    version    integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    UNIQUE (id, owner_id)
);
ALTER TABLE leases ADD COLUMN vetuste_grid_id uuid;
ALTER TABLE leases ADD CONSTRAINT leases_vetuste_grid_fk
    FOREIGN KEY (vetuste_grid_id, owner_id) REFERENCES vetuste_grids (id, owner_id);

-- The inventory header (INV-OD-01: items normalised below).
ALTER TABLE inventories RENAME COLUMN items TO legacy_items;
ALTER TABLE inventories ADD COLUMN mode inventory_mode NOT NULL DEFAULT 'upload';
ALTER TABLE inventories ALTER COLUMN mode DROP DEFAULT;
ALTER TABLE inventories ADD COLUMN state inventory_state NOT NULL DEFAULT 'issued';
ALTER TABLE inventories ALTER COLUMN state DROP DEFAULT;
ALTER TABLE inventories ALTER COLUMN done_on DROP NOT NULL;
ALTER TABLE inventories ALTER COLUMN document_id DROP NOT NULL;
ALTER TABLE inventories ADD COLUMN scheduled_at timestamptz;
ALTER TABLE inventories ADD COLUMN keys_returned_on date;
ALTER TABLE inventories ADD COLUMN place text NOT NULL DEFAULT '';
ALTER TABLE inventories ADD COLUMN entry_inventory_id uuid;
ALTER TABLE inventories ADD COLUMN tenant_new_address text NOT NULL DEFAULT '';
ALTER TABLE inventories ADD COLUMN observations text NOT NULL DEFAULT '';
ALTER TABLE inventories ADD COLUMN template_version text NOT NULL DEFAULT '';
ALTER TABLE inventories ADD COLUMN version integer NOT NULL DEFAULT 1;
ALTER TABLE inventories ADD COLUMN updated_at timestamptz;
ALTER TABLE inventories ADD COLUMN updated_by text;
ALTER TABLE inventories ADD CONSTRAINT inventories_id_owner UNIQUE (id, owner_id);
ALTER TABLE inventories ADD CONSTRAINT inventories_entry_fk
    FOREIGN KEY (entry_inventory_id, owner_id) REFERENCES inventories (id, owner_id);
ALTER TABLE inventories ADD CONSTRAINT inventories_entry_only_on_exit
    CHECK (kind = 'exit' OR entry_inventory_id IS NULL);
ALTER TABLE inventories ADD CONSTRAINT inventories_issued_has_document
    CHECK (state <> 'issued' OR document_id IS NOT NULL);
ALTER TABLE inventories ADD CONSTRAINT inventories_dated_once_done
    CHECK (state IN ('draft', 'scheduled', 'in_progress') OR done_on IS NOT NULL);
ALTER TABLE inventories ADD CONSTRAINT inventories_upload_has_no_capture
    CHECK (mode <> 'upload' OR state IN ('draft', 'issued'));

CREATE TABLE inventory_items (
    id            uuid PRIMARY KEY,
    owner_id      uuid NOT NULL REFERENCES owners (id),
    inventory_id  uuid NOT NULL,
    room_id       uuid,
    element_id    uuid,
    room_label    text NOT NULL,
    position      integer NOT NULL CHECK (position > 0),
    category      element_category NOT NULL,
    label         text NOT NULL CHECK (label <> ''),
    condition     item_condition,
    works         item_works,
    quantity      integer NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    comment       text NOT NULL DEFAULT '',
    to_check      boolean NOT NULL DEFAULT false,
    entry_item_id uuid,
    version       integer NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL,
    created_by    text NOT NULL,
    updated_at    timestamptz NOT NULL,
    updated_by    text NOT NULL,
    UNIQUE (id, owner_id),
    UNIQUE (inventory_id, position),
    FOREIGN KEY (inventory_id, owner_id) REFERENCES inventories (id, owner_id),
    FOREIGN KEY (room_id, owner_id) REFERENCES asset_rooms (id, owner_id) ON DELETE SET NULL (room_id),
    FOREIGN KEY (element_id, owner_id) REFERENCES asset_elements (id, owner_id) ON DELETE SET NULL (element_id),
    FOREIGN KEY (entry_item_id, owner_id) REFERENCES inventory_items (id, owner_id)
);

CREATE TABLE inventory_meters (
    id           uuid PRIMARY KEY,
    owner_id     uuid NOT NULL REFERENCES owners (id),
    inventory_id uuid NOT NULL,
    kind         meter_kind NOT NULL,
    meter_number text NOT NULL DEFAULT '',
    -- As read on the meter: a decimal string, never a float.
    reading      text NOT NULL DEFAULT '' CHECK (reading ~ '^([0-9]+([.,][0-9]+)?)?$'),
    unit         text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL,
    created_by   text NOT NULL,
    UNIQUE (id, owner_id),
    FOREIGN KEY (inventory_id, owner_id) REFERENCES inventories (id, owner_id)
);

CREATE TABLE inventory_keys (
    id           uuid PRIMARY KEY,
    owner_id     uuid NOT NULL REFERENCES owners (id),
    inventory_id uuid NOT NULL,
    kind         key_kind NOT NULL,
    destination  text NOT NULL CHECK (destination <> ''),
    quantity     integer NOT NULL CHECK (quantity >= 0),
    comment      text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL,
    created_by   text NOT NULL,
    FOREIGN KEY (inventory_id, owner_id) REFERENCES inventories (id, owner_id)
);

-- Photos are archived objects (EXIF stripped, the cleaned file hashed); the
-- upload's own hash is kept for its audit event only (RENT-INV-001 §3).
CREATE TABLE inventory_photos (
    id              uuid PRIMARY KEY,
    owner_id        uuid NOT NULL REFERENCES owners (id),
    inventory_id    uuid NOT NULL,
    item_id         uuid,
    meter_id        uuid,
    object_key      text NOT NULL,
    object_version  text NOT NULL DEFAULT '',
    sha256          text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    original_sha256 text NOT NULL CHECK (original_sha256 ~ '^[0-9a-f]{64}$'),
    taken_at        timestamptz,
    width           integer NOT NULL CHECK (width > 0),
    height          integer NOT NULL CHECK (height > 0),
    created_at      timestamptz NOT NULL,
    created_by      text NOT NULL,
    FOREIGN KEY (inventory_id, owner_id) REFERENCES inventories (id, owner_id),
    FOREIGN KEY (item_id, owner_id) REFERENCES inventory_items (id, owner_id),
    FOREIGN KEY (meter_id, owner_id) REFERENCES inventory_meters (id, owner_id),
    CHECK (num_nonnulls(item_id, meter_id) <= 1)
);

CREATE TABLE inventory_signatures (
    id                   uuid PRIMARY KEY,
    owner_id             uuid NOT NULL REFERENCES owners (id),
    inventory_id         uuid NOT NULL,
    party                signature_party NOT NULL,
    signer_name          text NOT NULL CHECK (signer_name <> ''),
    method               signature_method NOT NULL,
    signed_at            timestamptz NOT NULL,
    reservations         text NOT NULL DEFAULT '',
    evidence_document_id uuid,
    created_at           timestamptz NOT NULL,
    created_by           text NOT NULL,
    FOREIGN KEY (inventory_id, owner_id) REFERENCES inventories (id, owner_id),
    FOREIGN KEY (evidence_document_id, owner_id) REFERENCES documents (id, owner_id)
);

-- A completion request on an issued entry (loi 89-462 art. 3-2): the entry is
-- never edited; the accepted lines and the decision's EC document are.
CREATE TABLE inventory_completions (
    id           uuid PRIMARY KEY,
    owner_id     uuid NOT NULL REFERENCES owners (id),
    inventory_id uuid NOT NULL,
    reason       completion_reason NOT NULL,
    requested_on date NOT NULL,
    lines        jsonb NOT NULL DEFAULT '[]',
    decision     completion_decision NOT NULL DEFAULT 'pending',
    decided_on   date,
    document_id  uuid,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL,
    created_by   text NOT NULL,
    updated_at   timestamptz NOT NULL,
    updated_by   text NOT NULL,
    FOREIGN KEY (inventory_id, owner_id) REFERENCES inventories (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id),
    CHECK ((decision = 'pending') = (decided_on IS NULL))
);

-- Deductions from the deposit, each on the exit item it charges, justified by
-- an archived quote or invoice, never a flat amount (loi 89-462 art. 22).
CREATE TABLE deposit_deductions (
    id              uuid PRIMARY KEY,
    owner_id        uuid NOT NULL REFERENCES owners (id),
    lease_id        uuid NOT NULL,
    exit_item_id    uuid,
    description     text NOT NULL CHECK (description <> ''),
    cost_cents      bigint NOT NULL CHECK (cost_cents >= 0),
    tenant_share_bp integer NOT NULL CHECK (tenant_share_bp BETWEEN 0 AND 10000),
    amount_cents    bigint NOT NULL CHECK (amount_cents >= 0 AND amount_cents <= cost_cents),
    basis           deduction_basis NOT NULL,
    -- The quote or invoice, even when a vétusté grid sets the share.
    document_id     uuid NOT NULL,
    charge_id       uuid,
    version         integer NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL,
    created_by      text NOT NULL,
    updated_at      timestamptz NOT NULL,
    updated_by      text NOT NULL,
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (exit_item_id, owner_id) REFERENCES inventory_items (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE INDEX deposit_deductions_lease ON deposit_deductions (lease_id);
CREATE INDEX inventory_items_inventory ON inventory_items (inventory_id, position);
CREATE INDEX asset_rooms_asset ON asset_rooms (asset_id, position);

-- Issued means immutable: the header, then its children (RENT-INV-001 §3).
-- +goose StatementBegin
CREATE FUNCTION inventories_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state = 'issued' THEN
        RAISE EXCEPTION 'an issued inventory is immutable' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER inventories_immutable BEFORE UPDATE OR DELETE ON inventories
    FOR EACH ROW EXECUTE FUNCTION inventories_immutable();

-- A child row is refused when its inventory is issued, or cannot be seen
-- (fail closed).
-- +goose StatementBegin
CREATE FUNCTION inventory_child_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    inv uuid := CASE WHEN TG_OP = 'DELETE' THEN OLD.inventory_id ELSE NEW.inventory_id END;
    st  inventory_state;
BEGIN
    SELECT state INTO st FROM inventories WHERE id = inv;
    IF st IS NULL OR st = 'issued' THEN
        RAISE EXCEPTION 'an issued inventory is immutable' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.inventory_id <> NEW.inventory_id THEN
        RAISE EXCEPTION 'a row cannot move to another inventory' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER inventory_items_immutable BEFORE INSERT OR UPDATE OR DELETE ON inventory_items
    FOR EACH ROW EXECUTE FUNCTION inventory_child_immutable();
CREATE TRIGGER inventory_meters_immutable BEFORE INSERT OR UPDATE OR DELETE ON inventory_meters
    FOR EACH ROW EXECUTE FUNCTION inventory_child_immutable();
CREATE TRIGGER inventory_keys_immutable BEFORE INSERT OR UPDATE OR DELETE ON inventory_keys
    FOR EACH ROW EXECUTE FUNCTION inventory_child_immutable();
CREATE TRIGGER inventory_photos_immutable BEFORE INSERT OR UPDATE OR DELETE ON inventory_photos
    FOR EACH ROW EXECUTE FUNCTION inventory_child_immutable();
CREATE TRIGGER inventory_signatures_immutable BEFORE INSERT OR UPDATE OR DELETE ON inventory_signatures
    FOR EACH ROW EXECUTE FUNCTION inventory_child_immutable();

-- Row-level security: the owner policies of every owner table, with delete
-- where a row may be removed before issue (layout edits, replaced lists).
-- +goose StatementBegin
CREATE FUNCTION tenure_owner_policies(t regclass, deletes boolean) RETURNS void LANGUAGE plpgsql AS $$
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
    IF deletes THEN
        EXECUTE format('CREATE POLICY api_delete ON %s FOR DELETE TO tenure_api
            USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = ''owner'')', t);
    END IF;
    EXECUTE format('CREATE POLICY jobs_scope ON %s TO tenure_jobs
        USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id())', t);
END
$$;
-- +goose StatementEnd
SELECT tenure_owner_policies('asset_rooms', true);
SELECT tenure_owner_policies('asset_elements', true);
SELECT tenure_owner_policies('vetuste_grids', false);
SELECT tenure_owner_policies('inventory_items', true);
SELECT tenure_owner_policies('inventory_meters', true);
SELECT tenure_owner_policies('inventory_keys', true);
SELECT tenure_owner_policies('inventory_photos', true);
SELECT tenure_owner_policies('inventory_signatures', false);
SELECT tenure_owner_policies('inventory_completions', false);
SELECT tenure_owner_policies('deposit_deductions', true);
DROP FUNCTION tenure_owner_policies(regclass, boolean);
-- inventories already has its policies (000004, api_update included); the
-- grant below lets a draft be worked on, the trigger keeps an issued one as it is.

-- System templates: read by every scope and every job, written by migrations
-- only; an owner's own templates follow the owner policies.
ALTER TABLE inventory_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_templates FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON inventory_templates FOR SELECT TO tenure_api
    USING (owner_id IS NULL OR (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL));
CREATE POLICY api_insert ON inventory_templates FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_read ON inventory_templates FOR SELECT TO tenure_jobs
    USING (owner_id IS NULL OR owner_id = app_owner_id());
CREATE POLICY migrate_system ON inventory_templates TO tenure_migrate
    USING (owner_id IS NULL) WITH CHECK (owner_id IS NULL);

GRANT SELECT, INSERT, UPDATE, DELETE ON asset_rooms, asset_elements, inventory_items, inventory_meters,
    inventory_keys, inventory_photos, deposit_deductions TO tenure_api;
GRANT SELECT, INSERT, UPDATE ON vetuste_grids, inventory_signatures, inventory_completions TO tenure_api;
GRANT SELECT, INSERT ON inventory_templates TO tenure_api;
GRANT UPDATE ON inventories TO tenure_api;
GRANT SELECT ON asset_rooms, asset_elements, inventory_templates, vetuste_grids, inventory_items, inventory_meters,
    inventory_keys, inventory_photos, inventory_signatures, inventory_completions, deposit_deductions TO tenure_jobs;

-- System inventory templates, generated from testdata/inventory/templates.json
-- by go test ./migrations -update.
-- seed:inventory_templates:begin
INSERT INTO inventory_templates (id, owner_id, asset_kind, version, structure, source, created_at, created_by) VALUES ('f7b85363-29ef-51df-8435-f0fd0ce4de45', NULL, 'apartment', 1, '{"asset_kind":"apartment","version":1,"rooms":[{"kind":"entry","label":"Entrée","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"}]},{"kind":"living","label":"Séjour","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"}]},{"kind":"kitchen","label":"Cuisine","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"},{"category":"plumbing","label":"Évier"},{"category":"plumbing","label":"Robinetterie"},{"category":"other","label":"Plan de travail"},{"category":"appliance","label":"Hotte"},{"category":"appliance","label":"Plaques"},{"category":"appliance","label":"Four"}]},{"kind":"bedroom","label":"Chambre","repeat":"bedroom","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"}]},{"kind":"bathroom","label":"Salle d’eau","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"},{"category":"plumbing","label":"Douche ou baignoire"},{"category":"plumbing","label":"Lavabo"},{"category":"plumbing","label":"WC"},{"category":"electrical","label":"VMC"}]},{"kind":"wc","label":"WC","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"plumbing","label":"WC"},{"category":"electrical","label":"Éclairage"}]},{"kind":"storage","label":"Cellier ou cave","when":"cellar","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"electrical","label":"Éclairage"}]}],"furnished":{"bedroom":[{"category":"furniture","label":"Literie avec couette ou couverture","quantity":1},{"category":"furniture","label":"Dispositif d’occultation des fenêtres","quantity":1}],"kitchen":[{"category":"furniture","label":"Plaques de cuisson","quantity":1,"replaces":"Plaques"},{"category":"furniture","label":"Four ou four à micro-ondes","quantity":1,"replaces":"Four"},{"category":"furniture","label":"Réfrigérateur avec compartiment congélation","quantity":1},{"category":"furniture","label":"Vaisselle","quantity":1},{"category":"furniture","label":"Ustensiles de cuisine","quantity":1},{"category":"furniture","label":"Matériel d’entretien ménager","quantity":1}],"living":[{"category":"furniture","label":"Table et sièges","quantity":1},{"category":"furniture","label":"Étagères de rangement","quantity":1},{"category":"furniture","label":"Luminaires","quantity":1}]}}'::jsonb, 'RENT-INV-001 §4; furniture: décret 2015-981 art. 2. Verify the furniture list against Légifrance before inventory slice 6.', '2026-11-05T00:00:00Z', 'migration');
INSERT INTO inventory_templates (id, owner_id, asset_kind, version, structure, source, created_at, created_by) VALUES ('95675205-4c15-5922-be24-a54b2107c243', NULL, 'house', 1, '{"asset_kind":"house","version":1,"rooms":[{"kind":"entry","label":"Entrée","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"}]},{"kind":"living","label":"Séjour","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"}]},{"kind":"kitchen","label":"Cuisine","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"},{"category":"plumbing","label":"Évier"},{"category":"plumbing","label":"Robinetterie"},{"category":"other","label":"Plan de travail"},{"category":"appliance","label":"Hotte"},{"category":"appliance","label":"Plaques"},{"category":"appliance","label":"Four"}]},{"kind":"bedroom","label":"Chambre","repeat":"bedroom","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"}]},{"kind":"bathroom","label":"Salle d’eau","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Fenêtres et volets"},{"category":"electrical","label":"Prises et interrupteurs"},{"category":"electrical","label":"Éclairage"},{"category":"heating","label":"Radiateur"},{"category":"plumbing","label":"Douche ou baignoire"},{"category":"plumbing","label":"Lavabo"},{"category":"plumbing","label":"WC"},{"category":"electrical","label":"VMC"}]},{"kind":"wc","label":"WC","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"plumbing","label":"WC"},{"category":"electrical","label":"Éclairage"}]},{"kind":"storage","label":"Cellier ou cave","when":"cellar","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"electrical","label":"Éclairage"}]},{"kind":"garage","label":"Garage","when":"garage","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte de garage"},{"category":"electrical","label":"Éclairage"},{"category":"electrical","label":"Prise"}]},{"kind":"outside","label":"Extérieurs","when":"garden","elements":[{"category":"other","label":"Jardin"},{"category":"walls","label":"Façades visibles"},{"category":"joinery","label":"Portail"},{"category":"other","label":"Clôture"},{"category":"floor","label":"Terrasse"},{"category":"other","label":"Gouttières visibles"},{"category":"electrical","label":"Éclairage extérieur"},{"category":"plumbing","label":"Robinet extérieur"}]}],"furnished":{"bedroom":[{"category":"furniture","label":"Literie avec couette ou couverture","quantity":1},{"category":"furniture","label":"Dispositif d’occultation des fenêtres","quantity":1}],"kitchen":[{"category":"furniture","label":"Plaques de cuisson","quantity":1,"replaces":"Plaques"},{"category":"furniture","label":"Four ou four à micro-ondes","quantity":1,"replaces":"Four"},{"category":"furniture","label":"Réfrigérateur avec compartiment congélation","quantity":1},{"category":"furniture","label":"Vaisselle","quantity":1},{"category":"furniture","label":"Ustensiles de cuisine","quantity":1},{"category":"furniture","label":"Matériel d’entretien ménager","quantity":1}],"living":[{"category":"furniture","label":"Table et sièges","quantity":1},{"category":"furniture","label":"Étagères de rangement","quantity":1},{"category":"furniture","label":"Luminaires","quantity":1}]}}'::jsonb, 'RENT-INV-001 §4; furniture: décret 2015-981 art. 2. Verify the furniture list against Légifrance before inventory slice 6.', '2026-11-05T00:00:00Z', 'migration');
INSERT INTO inventory_templates (id, owner_id, asset_kind, version, structure, source, created_at, created_by) VALUES ('cd22ac29-aa11-5d8b-a3bf-c1075f4e8a2f', NULL, 'box', 1, '{"asset_kind":"box","version":1,"rooms":[{"kind":"box","label":"Box","elements":[{"category":"floor","label":"Sol"},{"category":"walls","label":"Murs"},{"category":"ceiling","label":"Plafond"},{"category":"joinery","label":"Porte"},{"category":"joinery","label":"Serrure"},{"category":"electrical","label":"Télécommande"},{"category":"electrical","label":"Éclairage"},{"category":"electrical","label":"Prise"},{"category":"other","label":"Ventilation"},{"category":"other","label":"Numéro de place"}]}]}'::jsonb, 'RENT-INV-001 §4; furniture: décret 2015-981 art. 2. Verify the furniture list against Légifrance before inventory slice 6.', '2026-11-05T00:00:00Z', 'migration');
-- seed:inventory_templates:end

-- Seed of legal_params added since 000010, generated from testdata/params/*.json
-- by go test ./migrations -update.
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit.collective_holdback_bp', '2024-01-01', '2000'::jsonb, 'Loi 89-462 art. 22 (rédaction de la loi 2014-366 ALUR) : dans un immeuble collectif, conservation d’une provision d’au plus 20 % du dépôt jusqu’à l’arrêté annuel des comptes', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit.holdback_balance_months_after_approval', '2024-01-01', '1'::jsonb, 'Loi 89-462 art. 22 (rédaction de la loi 2014-366 ALUR) : régularisation et solde dans le mois qui suit l’approbation définitive des comptes de l’immeuble', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit.late_penalty_bp_of_rent', '2024-01-01', '1000'::jsonb, 'Loi 89-462 art. 22 (rédaction de la loi 2014-366 ALUR) : majoration de 10 % du loyer mensuel en principal pour chaque période mensuelle commencée en retard, non due si le locataire n’a pas transmis sa nouvelle adresse', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit.return_months.conforming', '2024-01-01', '1'::jsonb, 'Loi 89-462 art. 22 (rédaction de la loi 2014-366 ALUR) : restitution dans un délai d’un mois à compter de la remise des clés lorsque l’état des lieux de sortie est conforme à l’état des lieux d’entrée ; Tenure applique ce délai dès qu’aucune retenue n’est faite (INV-OD-13)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('deposit.return_months.with_deductions', '2024-01-01', '2'::jsonb, 'Loi 89-462 art. 22 (rédaction de la loi 2014-366 ALUR) : délai de deux mois à compter de la remise des clés dans les autres cas', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('heating_season.end_month', '2024-01-01', '5'::jsonb, 'RENT-INV-001 INV-OD-07 (règle produit) : la loi ne fixe pas la période de chauffe ; mai par défaut, modifiable par bien', '');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('heating_season.start_month', '2024-01-01', '10'::jsonb, 'RENT-INV-001 INV-OD-07 (règle produit) : la loi ne fixe pas la période de chauffe ; octobre par défaut, modifiable par bien', '');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('inventory.completion_window_days', '2024-01-01', '10'::jsonb, 'Loi 89-462 art. 3-2 : le locataire peut demander de compléter l’état des lieux d’entrée dans un délai de dix jours à compter de son établissement', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('inventory.heating_window_months', '2024-01-01', '1'::jsonb, 'Loi 89-462 art. 3-2 : l’état des éléments de chauffage peut être complété pendant le premier mois de la période de chauffe', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key IN ('deposit.return_months.conforming', 'deposit.return_months.with_deductions',
    'deposit.collective_holdback_bp', 'deposit.holdback_balance_months_after_approval', 'deposit.late_penalty_bp_of_rent',
    'inventory.completion_window_days', 'inventory.heating_window_months', 'heating_season.start_month',
    'heating_season.end_month');
DROP TABLE deposit_deductions;
DROP TABLE inventory_completions;
DROP TABLE inventory_signatures;
DROP TABLE inventory_photos;
DROP TABLE inventory_keys;
DROP TABLE inventory_meters;
DROP TABLE inventory_items;
DROP FUNCTION inventory_child_immutable();
DROP TRIGGER inventories_immutable ON inventories;
DROP FUNCTION inventories_immutable();
REVOKE UPDATE ON inventories FROM tenure_api;
ALTER TABLE inventories DROP CONSTRAINT inventories_upload_has_no_capture;
ALTER TABLE inventories DROP CONSTRAINT inventories_dated_once_done;
ALTER TABLE inventories DROP CONSTRAINT inventories_issued_has_document;
ALTER TABLE inventories DROP CONSTRAINT inventories_entry_only_on_exit;
ALTER TABLE inventories DROP CONSTRAINT inventories_entry_fk;
ALTER TABLE inventories DROP CONSTRAINT inventories_id_owner;
ALTER TABLE inventories DROP COLUMN updated_by;
ALTER TABLE inventories DROP COLUMN updated_at;
ALTER TABLE inventories DROP COLUMN version;
ALTER TABLE inventories DROP COLUMN template_version;
ALTER TABLE inventories DROP COLUMN observations;
ALTER TABLE inventories DROP COLUMN tenant_new_address;
ALTER TABLE inventories DROP COLUMN entry_inventory_id;
ALTER TABLE inventories DROP COLUMN place;
ALTER TABLE inventories DROP COLUMN keys_returned_on;
ALTER TABLE inventories DROP COLUMN scheduled_at;
ALTER TABLE inventories ALTER COLUMN document_id SET NOT NULL;
ALTER TABLE inventories ALTER COLUMN done_on SET NOT NULL;
ALTER TABLE inventories DROP COLUMN state;
ALTER TABLE inventories DROP COLUMN mode;
ALTER TABLE inventories RENAME COLUMN legacy_items TO items;
ALTER TABLE leases DROP CONSTRAINT leases_vetuste_grid_fk;
ALTER TABLE leases DROP COLUMN vetuste_grid_id;
DROP TABLE vetuste_grids;
DROP TABLE inventory_templates;
DROP TABLE asset_elements;
DROP TABLE asset_rooms;
ALTER TABLE assets DROP COLUMN collective_building;
DROP TYPE deduction_basis;
DROP TYPE completion_decision;
DROP TYPE completion_reason;
DROP TYPE signature_method;
DROP TYPE signature_party;
DROP TYPE key_kind;
DROP TYPE meter_kind;
DROP TYPE element_category;
DROP TYPE room_kind;
DROP TYPE item_works;
DROP TYPE item_condition;
DROP TYPE inventory_state;
DROP TYPE inventory_mode;
