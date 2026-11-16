-- 000037_loan_parts — a loan that financed parts of an SCI (RENT-SPEC-019,
-- part 19e, Q-133). An associate who borrowed to buy their parts deducts
-- the loan's interest and insurance on their own 2044, with their share of
-- the SCI's result. Such a loan finances no asset of theirs: it has kind
-- 'parts', the SCI's name as the associate types it (the SCI is another
-- owner scope, which a foreign key cannot reach), and no asset.

-- +goose Up
ALTER TABLE loans
    ADD COLUMN kind text NOT NULL DEFAULT 'asset' CHECK (kind IN ('asset', 'parts')),
    ADD COLUMN sci_name text NOT NULL DEFAULT '' CHECK (length(sci_name) <= 200),
    ALTER COLUMN asset_id DROP NOT NULL,
    ADD CONSTRAINT loans_kind_asset CHECK ((kind = 'asset') = (asset_id IS NOT NULL)),
    ADD CONSTRAINT loans_kind_sci CHECK (kind = 'asset' OR sci_name <> '');

-- +goose Down
-- A loan for parts has no asset to go back to: Down refuses then.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM loans WHERE kind = 'parts') THEN
        RAISE EXCEPTION 'loans of kind parts exist: 000037 is not reversible';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE loans
    DROP CONSTRAINT loans_kind_sci,
    DROP CONSTRAINT loans_kind_asset,
    ALTER COLUMN asset_id SET NOT NULL,
    DROP COLUMN sci_name,
    DROP COLUMN kind;
