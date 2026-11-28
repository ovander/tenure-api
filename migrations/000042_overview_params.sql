-- 000042_overview_params — the thresholds of the month cockpit (RENT-FE-001
-- §4b, RENT-BE-001 §3 GET /overview/month, Q-139): business rules without a
-- legal basis, kept in legal_params with the others.

-- +goose Up
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('overview.cockpit', '2024-01-01', '{"expiring_days":60,"job_window_days":7,"overdue_grace_days":0,"receipt_delay_hours":2,"regularisation_after_days":120,"statement_gap_days":35}'::jsonb, 'RENT-FE-001 §4b, Q-139 : seuils du cockpit du mois (quittance attendue deux heures après le paiement, relevé de moins de 35 jours, retard dès le lendemain de l’échéance, traitements en échec sur sept jours, régularisation 120 jours après la fin de l’exercice, diagnostics et dépôts surveillés 60 jours à l’avance) ; règle de gestion, sans base légale, validée par le propriétaire le 2026-11-28', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'overview.cockpit' AND effective_from = '2024-01-01';
