-- Single statement per migration file. Rows that use add_expense_by_chat must
-- be removed manually before a rollback; MySQL rejects the narrowing otherwise.
ALTER TABLE eval_events
    MODIFY COLUMN task ENUM('onboarding', 'add_expense', 'understand_budget', 'get_advice', 'find_offer') NOT NULL;
