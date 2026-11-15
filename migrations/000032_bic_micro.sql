-- 000032_bic_micro — the micro-BIC regime of long-term furnished letting
-- (RENT-SPEC-019, part 18c, Q-132): abatement, minimum and receipts
-- threshold, seeded in legal_params for the comparison with the réel.

-- +goose Up
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('bic.micro', '2024-01-01', '{"abatement_bp":5000,"minimum_abatement_cents":30500,"threshold_cents":7770000}'::jsonb, 'Micro-BIC de la location meublée de longue durée : abattement de 50 %, minimum 305 €, recettes jusqu’à 77 700 € (CGI art. 50-0) ; Q-132', 'https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000050840931');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'bic.micro';
