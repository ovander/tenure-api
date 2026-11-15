-- 000034_sci — the SCI at the income tax as an owner scope of its own
-- (RENT-SPEC-019, part 19b, Q-133).
--   - owners.kind: 'person' (every row so far, with its Socrate subject) or
--     'sci' (no Socrate subject: no one logs in as the SCI), with its SIREN,
--     its tax regime ('ir' only: an SCI at the IS is refused) and its capital
--     in parts;
--   - grant role 'manager' (the gérant): the owner's rights in an SCI scope.
--     tenure_scope_role answers 'owner' for it, so every existing policy
--     applies unchanged. While an SCI has no grant at all, its creator is its
--     owner, so that the transaction creating it can record its first
--     manager; the first grant closes that window for good;
--   - the register: associates (a name, and the Socrate subject of the Tenure
--     person they are, when linked), dated holdings of parts, and the
--     movements (subscription, transfer, gift, succession) that open and
--     close them. A holding is never edited: a movement closes it on its day
--     (closed_on, the one change tenure_api may make, once).
-- PostgreSQL cannot drop an enum value, so Down leaves 'manager' in
-- grant_role, unused; and Down refuses once an SCI exists (explicitly
-- marked).

-- +goose Up
ALTER TYPE grant_role ADD VALUE IF NOT EXISTS 'manager';
CREATE TYPE owner_kind AS ENUM ('person', 'sci');

ALTER TABLE owners
    ADD COLUMN kind owner_kind NOT NULL DEFAULT 'person',
    ADD COLUMN siren text NOT NULL DEFAULT '',
    ADD COLUMN tax_regime text CHECK (tax_regime IN ('ir')),
    ADD COLUMN regime_from date,
    ADD COLUMN capital_parts integer CHECK (capital_parts > 0),
    ALTER COLUMN socrate_sub DROP NOT NULL,
    ADD CONSTRAINT owners_kind_identity CHECK ((kind = 'person') = (socrate_sub IS NOT NULL)),
    ADD CONSTRAINT owners_sci_fields CHECK (kind = 'person' OR (tax_regime IS NOT NULL AND capital_parts IS NOT NULL));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION tenure_scope_role(p_owner uuid) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT CASE
        WHEN app_actor_sub() IS NULL OR p_owner IS NULL THEN NULL
        WHEN EXISTS (SELECT 1 FROM owners WHERE id = p_owner AND socrate_sub = app_actor_sub()) THEN 'owner'
        -- The creator of an SCI that has no grant yet: the founding transaction.
        WHEN EXISTS (SELECT 1 FROM owners o WHERE o.id = p_owner AND o.kind = 'sci' AND o.created_by = app_actor_sub()
                     AND NOT EXISTS (SELECT 1 FROM owner_grants g WHERE g.owner_id = o.id)) THEN 'owner'
        -- A manager has the owner's rights, in an SCI only.
        ELSE (SELECT CASE WHEN g.role::text <> 'manager' THEN g.role::text WHEN o.kind = 'sci' THEN 'owner' END
              FROM owner_grants g JOIN owners o ON o.id = g.owner_id
              WHERE g.owner_id = p_owner AND g.grantee_sub = app_actor_sub() AND g.revoked_at IS NULL
              LIMIT 1)
    END
$$;
-- +goose StatementEnd

-- An SCI is created by a person, in its own scope, without a subject.
CREATE POLICY api_insert_entity ON owners FOR INSERT TO tenure_api
    WITH CHECK (kind = 'sci' AND socrate_sub IS NULL AND id = app_owner_id() AND created_by = app_actor_sub());
-- Its managers change its profile.
CREATE POLICY api_update_entity ON owners FOR UPDATE TO tenure_api
    USING (kind = 'sci' AND id = app_owner_id() AND tenure_scope_role(id) = 'owner')
    WITH CHECK (kind = 'sci' AND socrate_sub IS NULL AND id = app_owner_id() AND tenure_scope_role(id) = 'owner');

CREATE TYPE sci_movement_reason AS ENUM ('subscription', 'transfer', 'gift', 'succession');

CREATE TABLE sci_associates (
    id         uuid PRIMARY KEY,
    owner_id   uuid NOT NULL REFERENCES owners (id),
    name       text NOT NULL CHECK (name <> '' AND length(name) <= 200),
    -- the Socrate subject of the Tenure person this associate is (Q-133).
    person_sub text CHECK (person_sub <> ''),
    version    integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by text NOT NULL,
    UNIQUE (owner_id, person_sub)
);
CREATE INDEX sci_associates_owner ON sci_associates (owner_id);

CREATE TABLE sci_movements (
    id               uuid PRIMARY KEY,
    owner_id         uuid NOT NULL REFERENCES owners (id),
    on_date          date NOT NULL,
    reason           sci_movement_reason NOT NULL,
    parts            integer NOT NULL CHECK (parts > 0),
    from_holding_id  uuid,
    to_associate_id  uuid NOT NULL REFERENCES sci_associates (id),
    note             text NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    created_at       timestamptz NOT NULL,
    created_by       text NOT NULL,
    CHECK ((reason = 'subscription') = (from_holding_id IS NULL))
);
CREATE INDEX sci_movements_owner ON sci_movements (owner_id, on_date);

CREATE TABLE sci_holdings (
    id                  uuid PRIMARY KEY,
    owner_id            uuid NOT NULL REFERENCES owners (id),
    associate_id        uuid NOT NULL REFERENCES sci_associates (id),
    parts               integer NOT NULL CHECK (parts > 0),
    -- held from from_on, up to the day before closed_on.
    from_on             date NOT NULL,
    opened_by           uuid NOT NULL REFERENCES sci_movements (id),
    closed_on           date,
    closed_by           uuid REFERENCES sci_movements (id),
    created_at          timestamptz NOT NULL,
    created_by          text NOT NULL,
    updated_at          timestamptz NOT NULL,
    updated_by          text NOT NULL,
    CHECK ((closed_on IS NULL) = (closed_by IS NULL)),
    CHECK (closed_on IS NULL OR closed_on > from_on)
);
CREATE INDEX sci_holdings_owner ON sci_holdings (owner_id, associate_id);
ALTER TABLE sci_movements ADD FOREIGN KEY (from_holding_id) REFERENCES sci_holdings (id);

ALTER TABLE sci_associates ENABLE ROW LEVEL SECURITY;
ALTER TABLE sci_associates FORCE ROW LEVEL SECURITY;
ALTER TABLE sci_movements ENABLE ROW LEVEL SECURITY;
ALTER TABLE sci_movements FORCE ROW LEVEL SECURITY;
ALTER TABLE sci_holdings ENABLE ROW LEVEL SECURITY;
ALTER TABLE sci_holdings FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON sci_associates FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON sci_associates FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON sci_associates FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON sci_associates TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
CREATE POLICY api_read ON sci_movements FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON sci_movements FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON sci_movements TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
CREATE POLICY api_read ON sci_holdings FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON sci_holdings FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
-- Closing a holding: once.
CREATE POLICY api_close ON sci_holdings FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner' AND closed_on IS NULL)
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON sci_holdings TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT, UPDATE ON sci_associates TO tenure_api;
GRANT SELECT, INSERT ON sci_movements, sci_holdings TO tenure_api;
GRANT UPDATE (closed_on, closed_by, updated_at, updated_by) ON sci_holdings TO tenure_api;
GRANT SELECT ON sci_associates, sci_movements, sci_holdings TO tenure_jobs;

-- +goose Down
-- An SCI's rows have nowhere to go without its kind: Down refuses then.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM owners WHERE kind = 'sci') THEN
        RAISE EXCEPTION 'owners of kind sci exist: 000034 is not reversible';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE sci_movements, sci_holdings, sci_associates CASCADE;
DROP TYPE sci_movement_reason;
DROP POLICY api_update_entity ON owners;
DROP POLICY api_insert_entity ON owners;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION tenure_scope_role(p_owner uuid) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT CASE
        WHEN app_actor_sub() IS NULL OR p_owner IS NULL THEN NULL
        WHEN EXISTS (SELECT 1 FROM owners WHERE id = p_owner AND socrate_sub = app_actor_sub()) THEN 'owner'
        ELSE (SELECT role::text FROM owner_grants
              WHERE owner_id = p_owner AND grantee_sub = app_actor_sub() AND revoked_at IS NULL
              LIMIT 1)
    END
$$;
-- +goose StatementEnd
ALTER TABLE owners
    DROP CONSTRAINT owners_sci_fields,
    DROP CONSTRAINT owners_kind_identity,
    ALTER COLUMN socrate_sub SET NOT NULL,
    DROP COLUMN capital_parts,
    DROP COLUMN regime_from,
    DROP COLUMN tax_regime,
    DROP COLUMN siren,
    DROP COLUMN kind;
DROP TYPE owner_kind;
