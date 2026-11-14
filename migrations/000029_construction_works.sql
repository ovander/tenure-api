-- 000029_construction_works — improvement works on a dwelling are
-- deductible (RENT-SPEC-015, Q-130 resolved): line 224 of the 2044 covers
-- « réparation, entretien et amélioration » (CGI art. 31, I-1° b); only
-- construction, reconstruction and enlargement are excluded. A new
-- treatment, construction, is capitalised and never becomes a charge; an
-- improvement becomes a repairs charge when the asset is a dwelling
-- (tax.improvement_deductible). PostgreSQL cannot drop an enum value, so
-- Down leaves construction in works_treatment, unused.

-- +goose Up
ALTER TYPE works_treatment ADD VALUE IF NOT EXISTS 'construction';
ALTER TABLE incidents DROP CONSTRAINT incidents_check2;
-- Only a repair or an improvement may carry a charge.
ALTER TABLE incidents ADD CONSTRAINT incidents_charged_treatment
    CHECK (charge_id IS NULL OR treatment IN ('repair', 'improvement'));

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('ledger.capitalised_construction', '2024-01-01', '{"account":{"code":"213100","label":"Bâtiments"}}'::jsonb, 'RENT-SPEC-015 : travaux de construction, de reconstruction ou d’agrandissement, jamais déduits ; compte du plan comptable général (règlement ANC n° 2014-03), classement de gestion à valider par l’expert-comptable (Q-130)', 'https://www.anc.gouv.fr/normes-francaises/plan-comptable-general');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('tax.improvement_deductible', '2024-01-01', '{"asset_kinds":["house","apartment"]}'::jsonb, 'CGI art. 31, I-1° b : les dépenses d’amélioration afférentes aux locaux d’habitation sont déductibles (ligne 224 de la 2044), à l’exclusion des travaux de construction, de reconstruction ou d’agrandissement ; pour les autres locaux, l’amélioration n’est en principe pas déductible (Q-130)', 'https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000041464101');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key IN ('tax.improvement_deductible', 'ledger.capitalised_construction');
ALTER TABLE incidents DROP CONSTRAINT incidents_charged_treatment;
ALTER TABLE incidents ADD CONSTRAINT incidents_check2 CHECK (treatment IS DISTINCT FROM 'improvement' OR charge_id IS NULL);
