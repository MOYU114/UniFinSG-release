-- Provenance of a transaction: manual user entry or an approved agent draft.
-- draft_id links agent-created rows back to the confirmation that produced
-- them; user edits never change these columns.
ALTER TABLE transactions
    ADD COLUMN source ENUM('manual','agent') NOT NULL DEFAULT 'manual' AFTER note,
    ADD COLUMN draft_id CHAR(26) NULL AFTER source;
