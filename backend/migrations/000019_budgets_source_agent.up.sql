-- Approved agent drafts write budgets with source='agent'. The original
-- constraint only allowed saved/profile_default/copied_from_*; widen it in one
-- ALTER so the existing provenance values stay valid.
ALTER TABLE budgets
    DROP CHECK chk_budgets_source,
    ADD CONSTRAINT chk_budgets_source CHECK (
        source IN ('saved', 'profile_default', 'agent')
        OR source REGEXP '^copied_from_[0-9]{4}-(0[1-9]|1[0-2])$'
    );
