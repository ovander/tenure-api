-- 000040_regularisation_instalments — the number of monthly instalments a
-- late regularisation balance is spread over (loi 89-462 art. 23), read from
-- legal_params instead of being written in the code (code audit, 2026-11-26).

-- +goose Up
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('regularisation_spread_instalments', '2025-01-01', '12'::jsonb, 'Loi 89-462 art. 23 : régularisation tardive payable par douzièmes, à la demande du locataire ; RENT-SPEC-007', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('regularisation_spread_instalments', '2026-01-01', '12'::jsonb, 'Loi 89-462 art. 23 : régularisation tardive payable par douzièmes, à la demande du locataire ; RENT-SPEC-007', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'regularisation_spread_instalments' AND effective_from IN ('2025-01-01', '2026-01-01');
