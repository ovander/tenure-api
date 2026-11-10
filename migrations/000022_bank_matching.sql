-- 000022_bank_matching — how bank credits are matched to rent calls
-- (RENT-SPEC-013, part 14b, Q-124): the window around a call's due day, the
-- keywords of a housing allowance and the shortest name word matched. Only
-- parameters: the matched payments name their bank line in
-- payments.bank_line_id, which exists since 000003.

-- +goose Up
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('bank.matching', '2024-01-01', '{"allowance_keywords":["CAF","APL","ALS","ALF","MSA"],"min_name_length":3,"window_days":10}'::jsonb, 'RENT-SPEC-013 : rapprochement des virements (montant et référence du locataire, puis montant et période ; allocations logement CAF/MSA versées au bailleur) ; règles de gestion, sans base légale', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'bank.matching';
