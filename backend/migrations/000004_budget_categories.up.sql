CREATE TABLE budget_categories (
    budget_id BIGINT UNSIGNED NOT NULL,
    category ENUM(
        'food', 'transport', 'housing_tuition', 'daily_shopping',
        'study_digital', 'entertainment_other'
    ) NOT NULL,
    budget_cents BIGINT NOT NULL,
    PRIMARY KEY (budget_id, category),
    CONSTRAINT fk_budget_categories_budget FOREIGN KEY (budget_id)
        REFERENCES budgets(id) ON DELETE CASCADE,
    CONSTRAINT chk_budget_categories_amount CHECK (budget_cents >= 0),
    CONSTRAINT chk_budget_categories_category CHECK (category <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
