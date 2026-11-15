-- 000035_sci_declarations — the 2072 of an SCI at the income tax
-- (RENT-SPEC-019, part 19c, Q-133). A declaration holds the year's result
-- computed with the rules of the 2044 réel, as issued, and the holders of
-- the parts on 31 December; each associate's share is one allocation row.
-- Both are recorded once and never changed: a movement of parts dated in a
-- declared year is refused from then on. The allocations name the Tenure
-- person of a linked associate (person_sub), which 19d lets that person read.

-- +goose Up
CREATE TABLE sci_declarations (
    id          uuid PRIMARY KEY,
    owner_id    uuid NOT NULL REFERENCES owners (id),
    tax_year    integer NOT NULL CHECK (tax_year BETWEEN 1970 AND 9999),
    -- the year's figures (sci.Figures), the 2044 lines and flags they come from.
    result      jsonb NOT NULL,
    -- the parts held on 31 December, by associate.
    holders     jsonb NOT NULL,
    issued_at   timestamptz NOT NULL,
    issued_by   text NOT NULL,
    UNIQUE (owner_id, tax_year),
    UNIQUE (id, owner_id)
);

CREATE TABLE sci_allocations (
    id                   uuid PRIMARY KEY,
    owner_id             uuid NOT NULL REFERENCES owners (id),
    declaration_id       uuid NOT NULL,
    tax_year             integer NOT NULL,
    associate_id         uuid NOT NULL REFERENCES sci_associates (id),
    associate_name       text NOT NULL,
    person_sub           text,
    parts                integer NOT NULL CHECK (parts > 0),
    total_parts          integer NOT NULL CHECK (total_parts >= parts),
    income_cents         bigint NOT NULL,
    interest_cents       bigint NOT NULL,
    other_expenses_cents bigint NOT NULL,
    net_cents            bigint NOT NULL,
    CHECK (net_cents = income_cents - interest_cents - other_expenses_cents),
    FOREIGN KEY (declaration_id, owner_id) REFERENCES sci_declarations (id, owner_id),
    UNIQUE (declaration_id, associate_id)
);
CREATE INDEX sci_allocations_person ON sci_allocations (person_sub, tax_year) WHERE person_sub IS NOT NULL;

ALTER TABLE sci_declarations ENABLE ROW LEVEL SECURITY;
ALTER TABLE sci_declarations FORCE ROW LEVEL SECURITY;
ALTER TABLE sci_allocations ENABLE ROW LEVEL SECURITY;
ALTER TABLE sci_allocations FORCE ROW LEVEL SECURITY;
CREATE POLICY api_read ON sci_declarations FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON sci_declarations FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON sci_declarations TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
CREATE POLICY api_read ON sci_allocations FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_write ON sci_allocations FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = 'owner');
CREATE POLICY jobs_scope ON sci_allocations TO tenure_jobs
    USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id());
-- Recorded, never changed.
GRANT SELECT, INSERT ON sci_declarations, sci_allocations TO tenure_api;
GRANT SELECT ON sci_declarations, sci_allocations TO tenure_jobs;

-- +goose Down
DROP TABLE sci_allocations;
DROP TABLE sci_declarations;
