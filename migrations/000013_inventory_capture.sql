-- 000013_inventory_capture — the entry inventory captured in Tenure
-- (RENT-INV-001 §5, inventory slice 5):
--   * inventory_items.room_kind: the room's kind copied with its label when
--     the item is drafted, so a later layout edit never changes which checks
--     an inventory needs (outdoor parts need no coverings);
--   * document_counters.series: the number prefix within a class, so the
--     entry (EE) and exit (ES) inventories, both of class inventory, keep
--     sequences of their own; '' for every class with one prefix.
-- Reversible: Down drops the columns and restores the counter key, refused
-- while a counter of a non-empty series exists.

-- +goose Up
ALTER TABLE inventory_items ADD COLUMN room_kind room_kind;

ALTER TABLE document_counters ADD COLUMN series text NOT NULL DEFAULT '';
ALTER TABLE document_counters DROP CONSTRAINT document_counters_pkey;
ALTER TABLE document_counters ADD PRIMARY KEY (owner_id, class, series, asset_code, year);

-- +goose Down
ALTER TABLE document_counters DROP CONSTRAINT document_counters_pkey;
ALTER TABLE document_counters ADD PRIMARY KEY (owner_id, class, asset_code, year);
ALTER TABLE document_counters DROP COLUMN series;
ALTER TABLE inventory_items DROP COLUMN room_kind;
