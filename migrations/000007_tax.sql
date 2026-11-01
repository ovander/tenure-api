-- 000007_tax — annual tax summaries (RENT-SPEC-004; RENT-BE-001 §2, §3, §5; slice 7).
-- Reversible: Down drops everything this file creates.

-- +goose Up
CREATE TYPE tax_regime AS ENUM ('micro', 'reel');
CREATE TYPE tax_summary_state AS ENUM ('draft', 'issued');

-- One summary per owner and income year: a draft recomputed on demand and by
-- tax.annual, then issued once (immutable). summary is the tax.Lines result
-- (both regimes, every owner of the scope's assets, flags, source ids);
-- deficits_after are the deficits left for later years once the issued
-- regime is applied, the next year's prior deficits (Q-89).
CREATE TABLE tax_summaries (
    id              uuid PRIMARY KEY,
    owner_id        uuid NOT NULL REFERENCES owners (id),
    tax_year        integer NOT NULL CHECK (tax_year BETWEEN 1970 AND 9999),
    state           tax_summary_state NOT NULL DEFAULT 'draft',
    regime          tax_regime,
    summary         jsonb NOT NULL,
    inputs          jsonb NOT NULL DEFAULT '{}',
    deficits_after  jsonb NOT NULL DEFAULT '{}',
    computed_at     timestamptz NOT NULL,
    issued_at       timestamptz,
    document_id     uuid,
    csv_document_id uuid,
    version         integer NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL,
    created_by      text NOT NULL,
    updated_at      timestamptz NOT NULL,
    updated_by      text NOT NULL,
    UNIQUE (owner_id, tax_year),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id),
    FOREIGN KEY (csv_document_id, owner_id) REFERENCES documents (id, owner_id),
    CHECK ((state = 'issued') = (regime IS NOT NULL AND issued_at IS NOT NULL AND document_id IS NOT NULL AND csv_document_id IS NOT NULL))
);

-- +goose StatementBegin
CREATE FUNCTION tax_summaries_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR OLD.state = 'issued' THEN
        RAISE EXCEPTION 'tax summary % is issued and immutable', OLD.id USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER tax_summaries_immutable BEFORE UPDATE OR DELETE ON tax_summaries
    FOR EACH ROW EXECUTE FUNCTION tax_summaries_immutable();

-- +goose StatementBegin
CREATE FUNCTION tenure_owner_policies(t regclass) RETURNS void LANGUAGE plpgsql AS $$
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
    EXECUTE format('CREATE POLICY jobs_scope ON %s TO tenure_jobs
        USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id())', t);
END
$$;
-- +goose StatementEnd
SELECT tenure_owner_policies('tax_summaries');
DROP FUNCTION tenure_owner_policies(regclass);

GRANT SELECT, INSERT, UPDATE ON tax_summaries TO tenure_api, tenure_jobs;

-- Seed of legal_params added since 000006, generated from testdata/params/*.json
-- by go test ./migrations -update.
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('reel_option_lock_years', '2025-01-01', '3'::jsonb, 'CGI art. 32, 4 : l’option pour le régime réel est irrévocable pendant trois ans', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('reel_option_lock_years', '2026-01-01', '3'::jsonb, 'CGI art. 32, 4 : l’option pour le régime réel est irrévocable pendant trois ans', 'https://www.legifrance.gouv.fr/codes/texte_lc/LEGITEXT000006069577/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key IN ('reel_option_lock_years');
DROP TABLE tax_summaries;
DROP FUNCTION tax_summaries_immutable();
DROP TYPE tax_summary_state;
DROP TYPE tax_regime;
