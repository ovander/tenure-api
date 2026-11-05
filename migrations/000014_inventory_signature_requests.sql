-- 000014_inventory_signature_requests — the electronic signature requests of
-- an inventory (RENT-INV-001 slice 6, OD-08, INV-OD-03): one row per request
-- sent to the provider, with each party's signer and signing link, opened on
-- the owner's tablet in turn. A wet-signed scan needs no request: it is an
-- inventory_signatures row with its evidence document. Immutable once the
-- inventory is issued, like every child of an inventory.
-- Reversible: Down drops the table.

-- +goose Up
CREATE TYPE signature_request_state AS ENUM ('pending', 'done', 'cancelled', 'refused');

CREATE TABLE inventory_signature_requests (
    id                  uuid PRIMARY KEY,
    owner_id            uuid NOT NULL REFERENCES owners (id),
    inventory_id        uuid NOT NULL,
    provider            text NOT NULL CHECK (provider <> ''),
    provider_request_id text NOT NULL CHECK (provider_request_id <> ''),
    state               signature_request_state NOT NULL,
    -- [{key, party, signer_name, signer_id, link, signed}]: the link signs for
    -- that party; it is shown to the owner only and never logged.
    signers             jsonb NOT NULL,
    evidence_document_id uuid,
    version             integer NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL,
    created_by          text NOT NULL,
    updated_at          timestamptz NOT NULL,
    updated_by          text NOT NULL,
    FOREIGN KEY (inventory_id, owner_id) REFERENCES inventories (id, owner_id),
    FOREIGN KEY (evidence_document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE UNIQUE INDEX inventory_signature_requests_pending ON inventory_signature_requests (inventory_id) WHERE state = 'pending';
CREATE TRIGGER inventory_signature_requests_immutable BEFORE INSERT OR UPDATE OR DELETE ON inventory_signature_requests
    FOR EACH ROW EXECUTE FUNCTION inventory_child_immutable();
ALTER TABLE inventory_signature_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_signature_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON inventory_signature_requests FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_insert ON inventory_signature_requests FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY api_update ON inventory_signature_requests FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner')
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON inventory_signature_requests TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
GRANT SELECT, INSERT, UPDATE ON inventory_signature_requests TO tenure_api;
GRANT SELECT ON inventory_signature_requests TO tenure_jobs;

-- +goose Down
DROP TABLE inventory_signature_requests;
DROP TYPE signature_request_state;
