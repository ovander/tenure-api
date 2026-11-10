-- 000023_bank_charges — bank debits matched to charges (RENT-SPEC-013, part
-- 14c, Q-125): a charge names the bank line that paid it, so the charges
-- ledger is bank-backed; one line may pay several charges (a syndic call),
-- a charge is paid by one line at most. The debit matching parameter is
-- seeded in legal_params.

-- +goose Up
ALTER TABLE bank_lines ADD CONSTRAINT bank_lines_id_owner UNIQUE (id, owner_id);
ALTER TABLE charges ADD COLUMN bank_line_id uuid;
ALTER TABLE charges ADD CONSTRAINT charges_bank_line FOREIGN KEY (bank_line_id, owner_id) REFERENCES bank_lines (id, owner_id);
CREATE INDEX charges_bank_line ON charges (bank_line_id) WHERE bank_line_id IS NOT NULL;

-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('bank.charges', '2024-01-01', '{"window_days":45}'::jsonb, 'RENT-SPEC-013 : sorties bancaires rapprochées des charges (appels du syndic, taxe foncière, assurance, artisans) ; délai entre la charge et son paiement, règle de gestion sans base légale', 'https://www.legifrance.gouv.fr/loda/id/JORFTEXT000000509310/');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key = 'bank.charges';
DROP INDEX charges_bank_line;
ALTER TABLE charges DROP CONSTRAINT charges_bank_line;
ALTER TABLE charges DROP COLUMN bank_line_id;
ALTER TABLE bank_lines DROP CONSTRAINT bank_lines_id_owner;
