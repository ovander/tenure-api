-- 000025_ledger — the owner's journal and the period lock (RENT-SPEC-014,
-- part 15b, Q-127). The journal is computed from the movements already
-- stored; its chart of accounts is seeded in legal_params. Once the tax
-- summary of a year is issued, that year is closed: a trigger refuses any
-- write that would change what the summary was computed from, dated in it
-- (payments and their matches, the charges' tax columns, loan instalments);
-- attaching a missing invoice to a charge stays possible.
-- The refusal carries SQLSTATE TL001, which the API maps to the problem
-- ledger/period-locked. An issued summary is final, so a closed year stays
-- closed.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION ledger_closed(owner uuid, d date) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT d IS NOT NULL AND EXISTS (
        SELECT 1 FROM tax_summaries s
        WHERE s.owner_id = owner AND s.tax_year = extract(year FROM d)::integer AND s.state = 'issued')
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION ledger_refuse(owner uuid, d date) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF ledger_closed(owner, d) THEN
        RAISE EXCEPTION 'the year % is closed by its issued tax summary', extract(year FROM d)::integer
            USING ERRCODE = 'TL001';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION ledger_lock_payments() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') AND (TG_OP = 'DELETE' OR OLD.paid_on IS DISTINCT FROM NEW.paid_on
            OR OLD.amount_cents IS DISTINCT FROM NEW.amount_cents OR OLD.lease_id IS DISTINCT FROM NEW.lease_id) THEN
        PERFORM ledger_refuse(OLD.owner_id, OLD.paid_on);
    END IF;
    IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND (OLD.paid_on IS DISTINCT FROM NEW.paid_on
            OR OLD.amount_cents IS DISTINCT FROM NEW.amount_cents OR OLD.lease_id IS DISTINCT FROM NEW.lease_id)) THEN
        PERFORM ledger_refuse(NEW.owner_id, NEW.paid_on);
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER ledger_lock AFTER INSERT OR UPDATE OR DELETE ON payments
    FOR EACH ROW EXECUTE FUNCTION ledger_lock_payments();

-- +goose StatementBegin
CREATE FUNCTION ledger_lock_matches() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    r record;
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.billing_document_id IS NOT DISTINCT FROM NEW.billing_document_id
            AND OLD.amount_cents IS NOT DISTINCT FROM NEW.amount_cents THEN
        RETURN NULL;
    END IF;
    FOR r IN SELECT p.owner_id, p.paid_on FROM payments p
            WHERE p.id IN (CASE WHEN TG_OP = 'INSERT' THEN NEW.payment_id ELSE OLD.payment_id END,
                           CASE WHEN TG_OP = 'DELETE' THEN OLD.payment_id ELSE NEW.payment_id END) LOOP
        PERFORM ledger_refuse(r.owner_id, r.paid_on);
    END LOOP;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER ledger_lock AFTER INSERT OR UPDATE OR DELETE ON payment_matches
    FOR EACH ROW EXECUTE FUNCTION ledger_lock_matches();

-- +goose StatementBegin
CREATE FUNCTION ledger_lock_charges() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.paid_on IS NOT DISTINCT FROM NEW.paid_on
            AND OLD.amount_cents IS NOT DISTINCT FROM NEW.amount_cents
            AND OLD.recoverable_cents IS NOT DISTINCT FROM NEW.recoverable_cents
            AND OLD.category IS NOT DISTINCT FROM NEW.category
            AND OLD.asset_id IS NOT DISTINCT FROM NEW.asset_id
            AND OLD.status IS NOT DISTINCT FROM NEW.status THEN
        RETURN NULL;
    END IF;
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM ledger_refuse(OLD.owner_id, OLD.paid_on);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM ledger_refuse(NEW.owner_id, NEW.paid_on);
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER ledger_lock AFTER INSERT OR UPDATE OR DELETE ON charges
    FOR EACH ROW EXECUTE FUNCTION ledger_lock_charges();

-- +goose StatementBegin
CREATE FUNCTION ledger_lock_instalments() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM ledger_refuse(OLD.owner_id, OLD.due_on);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM ledger_refuse(NEW.owner_id, NEW.due_on);
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER ledger_lock AFTER INSERT OR UPDATE OR DELETE ON loan_instalments
    FOR EACH ROW EXECUTE FUNCTION ledger_lock_instalments();

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('ledger.chart', '2024-01-01', '{"accounts":{"bank":{"code":"512000","label":"Banque"},"deposits":{"code":"165000","label":"Dépôts et cautionnements reçus"},"loan_insurance":{"code":"616100","label":"Assurance emprunteur"},"loan_interest":{"code":"661100","label":"Intérêts des emprunts"},"loans":{"code":"164000","label":"Emprunts auprès des établissements de crédit"},"other_income":{"code":"708000","label":"Produits des activités annexes"},"penalties":{"code":"671200","label":"Pénalités et amendes"},"provisions":{"code":"419800","label":"Locataires, provisions sur charges"},"rent":{"code":"706000","label":"Loyers"},"tenant_advances":{"code":"419100","label":"Locataires, avances reçues"},"tenants":{"code":"411000","label":"Locataires"}},"categories":{"copro_provision":{"code":"614000","label":"Charges locatives et de copropriété"},"copro_regularisation_prior":{"code":"614000","label":"Charges locatives et de copropriété"},"copro_statement":{"code":"614000","label":"Charges locatives et de copropriété"},"indemnity":{"code":"708000","label":"Produits des activités annexes"},"insurance":{"code":"616000","label":"Primes d’assurance"},"loan_insurance":{"code":"616100","label":"Assurance emprunteur"},"loan_interest":{"code":"661100","label":"Intérêts des emprunts"},"management":{"code":"622600","label":"Honoraires de gestion"},"owner_expense_charged_to_tenant":{"code":"708000","label":"Produits des activités annexes"},"property_tax":{"code":"635120","label":"Taxes foncières"},"recoverable_utilities":{"code":"614000","label":"Charges locatives et de copropriété"},"repairs":{"code":"615200","label":"Entretien et réparations sur biens immobiliers"},"teom":{"code":"635120","label":"Taxes foncières"},"unrecovered_at_close":{"code":"614000","label":"Charges locatives et de copropriété"}},"income_categories":["owner_expense_charged_to_tenant","indemnity"]}'::jsonb, 'RENT-SPEC-014 : journal du bailleur en partie double ; comptes du plan comptable général (règlement ANC n° 2014-03), choix de gestion à valider par l’expert-comptable', 'https://www.anc.gouv.fr/normes-francaises/plan-comptable-general');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'ledger.chart';
DROP TRIGGER ledger_lock ON loan_instalments;
DROP TRIGGER ledger_lock ON charges;
DROP TRIGGER ledger_lock ON payment_matches;
DROP TRIGGER ledger_lock ON payments;
DROP FUNCTION ledger_lock_instalments();
DROP FUNCTION ledger_lock_charges();
DROP FUNCTION ledger_lock_matches();
DROP FUNCTION ledger_lock_payments();
DROP FUNCTION ledger_refuse(uuid, date);
DROP FUNCTION ledger_closed(uuid, date);
