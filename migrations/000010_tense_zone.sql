-- 000010_tense_zone — zone tendue separated from rent-ceiling zones
-- (RENT-BE-001 §2, Q-102). assets.tense_zone makes the relocation rule
-- (loi 89-462 art. 18) apply and the contract state the previous rent. Every
-- rent-ceiling zone is a tense area, so the service reads an asset as tense
-- when either column is set and writes tense_zone with controlled_zone; no
-- backfill here, since tenure_migrate sees no owner's rows (FORCE ROW LEVEL
-- SECURITY). Reversible: Down drops the column.

-- +goose Up
ALTER TABLE assets ADD COLUMN tense_zone boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE assets DROP COLUMN tense_zone;
