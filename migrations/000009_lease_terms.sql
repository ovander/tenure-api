-- 000009_lease_terms — the standard lease terms as a legal parameter
-- (RENT-BE-001 §7, Q-100): lease_term_months, which the web form proposes as
-- a lease's term, so that no term is written in code. Reversible: Down
-- deletes the entries this file seeds.

-- +goose Up
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('lease_term_months', '2024-01-01', '{"furnished":12,"furnished_student":9,"unfurnished":36,"unfurnished_legal_owner":72}'::jsonb, 'Loi 89-462 art. 10 (location nue : 3 ans, 6 ans si le bailleur est une personne morale) et art. 25-7 (meublé : 1 an, 9 mois pour un étudiant)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('lease_term_months', '2025-01-01', '{"furnished":12,"furnished_student":9,"unfurnished":36,"unfurnished_legal_owner":72}'::jsonb, 'Loi 89-462 art. 10 (location nue : 3 ans, 6 ans si le bailleur est une personne morale) et art. 25-7 (meublé : 1 an, 9 mois pour un étudiant)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('lease_term_months', '2026-01-01', '{"furnished":12,"furnished_student":9,"unfurnished":36,"unfurnished_legal_owner":72}'::jsonb, 'Loi 89-462 art. 10 (location nue : 3 ans, 6 ans si le bailleur est une personne morale) et art. 25-7 (meublé : 1 an, 9 mois pour un étudiant)', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'lease_term_months';
