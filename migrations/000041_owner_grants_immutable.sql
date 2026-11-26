-- 000041_owner_grants_immutable — a grant is revoked, never deleted and
-- never revived (CLAUDE.md, RENT-BE-001 §2). The api role could still clear
-- revoked_at or change a grant's role or grantee by UPDATE, and the audit
-- trail was the only trace (code audit, 2026-11-26). The database now
-- refuses it: the one change allowed is setting revoked_at once, with the
-- updated_* columns.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION owner_grants_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a grant is revoked, never deleted' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF OLD.revoked_at IS NOT NULL THEN
        RAISE EXCEPTION 'a revoked grant cannot change' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF NEW.id <> OLD.id OR NEW.owner_id <> OLD.owner_id OR NEW.grantee_sub <> OLD.grantee_sub
        OR NEW.role <> OLD.role OR NEW.granted_at <> OLD.granted_at OR NEW.created_by <> OLD.created_by THEN
        RAISE EXCEPTION 'only revoked_at of a grant can change' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER owner_grants_immutable BEFORE UPDATE OR DELETE ON owner_grants
    FOR EACH ROW EXECUTE FUNCTION owner_grants_immutable();

-- +goose Down
DROP TRIGGER owner_grants_immutable ON owner_grants;
DROP FUNCTION owner_grants_immutable();
