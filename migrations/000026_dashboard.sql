-- 000026_dashboard — the dashboard's parameter (RENT-SPEC-016, part 16a,
-- Q-129). The dashboard itself is computed from the journal, the vacancies
-- and the arrears already stored; only how it ages the arrears is a
-- parameter, seeded in legal_params.

-- +goose Up
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('dashboard.aging', '2024-01-01', '{"buckets_days":[30,60,90]}'::jsonb, 'RENT-SPEC-016 : ancienneté des impayés à 30, 60 et 90 jours après l’échéance ; règle de gestion, sans base légale', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'dashboard.aging';
