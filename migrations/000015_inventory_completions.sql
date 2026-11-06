-- 000015_inventory_completions — the tenant's requests to complete an issued
-- entry (loi 89-462 art. 3-2; RENT-INV-001 slice 7): the tenant's letter as
-- an optional uploaded document, and the request frozen once decided, since
-- its EC document is issued with the decision. The entry itself is never
-- edited: the accepted lines live here only.
-- Reversible: Down drops the trigger, its function and the column.

-- +goose Up
ALTER TABLE inventory_completions ADD COLUMN request_document_id uuid,
    ADD FOREIGN KEY (request_document_id, owner_id) REFERENCES documents (id, owner_id);
CREATE INDEX inventory_completions_inventory ON inventory_completions (inventory_id, created_at);

-- A request is never deleted; once decided it no longer changes, and what was
-- asked (the entry, the reason, the date, the letter) never changes.
-- +goose StatementBegin
CREATE FUNCTION inventory_completions_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a completion request is never deleted' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF OLD.decision <> 'pending' THEN
        RAISE EXCEPTION 'a decided completion request is immutable' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF OLD.inventory_id <> NEW.inventory_id OR OLD.reason <> NEW.reason OR OLD.requested_on <> NEW.requested_on
        OR OLD.request_document_id IS DISTINCT FROM NEW.request_document_id THEN
        RAISE EXCEPTION 'what a completion request asks does not change' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER inventory_completions_immutable BEFORE UPDATE OR DELETE ON inventory_completions
    FOR EACH ROW EXECUTE FUNCTION inventory_completions_immutable();

-- +goose Down
DROP TRIGGER inventory_completions_immutable ON inventory_completions;
DROP FUNCTION inventory_completions_immutable();
DROP INDEX inventory_completions_inventory;
ALTER TABLE inventory_completions DROP COLUMN request_document_id;
