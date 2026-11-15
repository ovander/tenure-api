-- 000036_sci_associate_read — the associate's side of an SCI (RENT-SPEC-019,
-- part 19d, Q-133). A Tenure person linked as an associate reads, in their
-- own scope, the shares of the issued 2072s that name them and the register
-- rows that are theirs, and nothing else of the SCI: tenure_scope_person
-- gives the Socrate subject of the scope's person, when the caller has a role
-- in that scope (their own, or a grant on it: an accountant preparing their
-- 2044 sees their shares too). The job role reads them the same way for the
-- yearly drafts. An allocation carries the SCI's name, as issued.

-- +goose Up
ALTER TABLE sci_allocations ADD COLUMN sci_name text NOT NULL DEFAULT '';
UPDATE sci_allocations al SET sci_name = o.legal_name FROM owners o WHERE o.id = al.owner_id;

-- +goose StatementBegin
CREATE FUNCTION tenure_scope_person(p_owner uuid) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT o.socrate_sub FROM owners o
    WHERE o.id = p_owner AND o.kind = 'person' AND tenure_scope_role(p_owner) IS NOT NULL
$$;
-- +goose StatementEnd
ALTER FUNCTION tenure_scope_person(uuid) OWNER TO tenure_rls;
REVOKE ALL ON FUNCTION tenure_scope_person(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION tenure_scope_person(uuid) TO tenure_api;

CREATE POLICY api_read_person ON sci_allocations FOR SELECT TO tenure_api
    USING (person_sub IS NOT NULL AND person_sub = tenure_scope_person(app_owner_id()));
CREATE POLICY jobs_read_person ON sci_allocations FOR SELECT TO tenure_jobs
    USING (person_sub IS NOT NULL AND person_sub = (SELECT socrate_sub FROM owners WHERE id = app_owner_id()));
CREATE POLICY api_read_person ON sci_associates FOR SELECT TO tenure_api
    USING (person_sub IS NOT NULL AND person_sub = tenure_scope_person(app_owner_id()));
CREATE POLICY jobs_read_person ON sci_associates FOR SELECT TO tenure_jobs
    USING (person_sub IS NOT NULL AND person_sub = (SELECT socrate_sub FROM owners WHERE id = app_owner_id()));

-- +goose Down
DROP POLICY jobs_read_person ON sci_associates;
DROP POLICY api_read_person ON sci_associates;
DROP POLICY jobs_read_person ON sci_allocations;
DROP POLICY api_read_person ON sci_allocations;
DROP FUNCTION tenure_scope_person(uuid);
ALTER TABLE sci_allocations DROP COLUMN sci_name;
