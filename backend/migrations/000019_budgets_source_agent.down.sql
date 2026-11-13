-- Rows with source='agent' must be removed or changed before this rollback;
-- MySQL rejects the narrowing otherwise and marks the schema dirty.
ALTER TABLE budgets
    DROP CHECK chk_budgets_source,
    ADD CONSTRAINT chk_budgets_source CHECK (
        source IN ('saved', 'profile_default')
        OR source REGEXP '^copied_from_[0-9]{4}-(0[1-9]|1[0-2])$'
    );
