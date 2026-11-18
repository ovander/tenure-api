-- 000039_sci_2044_lines — the 2044 section of an associate's SCI shares
-- (RENT-SPEC-019, Q-133, Q-135): lines 111 to 114 of the millésime 2026,
-- read per income year, as the line map is.

-- +goose Up
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('form_2044_sci_line_map', '2025-01-01', '{"expenses":"112","income":"111","interest":"113","millesime":2026,"net":"114","tax_year":2025}'::jsonb, 'Formulaire 2044, millésime 2026 (revenus 2025), rubrique 110 « Revenus des parts de sociétés immobilières » : 111 revenus bruts, 112 frais et charges hors intérêts d’emprunt, 113 intérêts d’emprunt (dont ceux de l’emprunt personnel pour l’acquisition des parts), 114 bénéfice ou déficit ; Q-135', 'https://www.impots.gouv.fr/sites/default/files/formulaires/2044/2026/2044_5483.pdf');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'form_2044_sci_line_map';
