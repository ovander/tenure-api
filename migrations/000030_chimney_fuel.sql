-- 000030_chimney_fuel — the chimney sweeping rhythm depends on the fuel
-- (RENT-SPEC-015, Q-131 resolved): 12 months by default and for gas, 6 for
-- wood, pellets and oil (maintenance.chimney). A chimney contract records
-- its fuel; the API requires it for a new or changed chimney contract, and
-- a contract recorded before keeps its interval.

-- +goose Up
ALTER TABLE maintenance_contracts ADD COLUMN fuel text
    CHECK (fuel IN ('gas', 'wood', 'pellets', 'oil', 'other'));
ALTER TABLE maintenance_contracts ADD CONSTRAINT maintenance_contracts_fuel_kind
    CHECK (fuel IS NULL OR kind = 'chimney');

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('maintenance.chimney', '2024-01-01', '{"default_months":12,"fuels":{"gas":12,"oil":6,"pellets":6,"wood":6}}'::jsonb, 'Ramonage : au moins une fois tous les 12 mois par l’occupant (décret n° 2023-641 du 20 juillet 2023, en vigueur le 1er octobre 2023) ; le règlement sanitaire départemental peut en exiger davantage selon le combustible. Lecture retenue : 12 mois pour le gaz et par défaut, 6 mois pour le bois, les granulés et le fioul (Q-131)', 'https://www.legifrance.gouv.fr/jorf/id/JORFTEXT000047867011');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'maintenance.chimney';
ALTER TABLE maintenance_contracts DROP COLUMN fuel;
