-- The evaluation flow replaces the retired "find offer" task with the new
-- conversational bookkeeping task. Existing rows keep the old enum value so
-- historical exports remain readable.
ALTER TABLE eval_events
    MODIFY COLUMN task ENUM('onboarding', 'add_expense', 'understand_budget', 'get_advice', 'find_offer', 'add_expense_by_chat') NOT NULL;
