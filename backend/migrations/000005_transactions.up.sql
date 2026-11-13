CREATE TABLE transactions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    amount_cents BIGINT NOT NULL,
    spent_on DATE NOT NULL,
    category ENUM(
        'food', 'transport', 'housing_tuition', 'daily_shopping',
        'study_digital', 'entertainment_other'
    ) NOT NULL,
    merchant VARCHAR(64) NULL,
    note VARCHAR(200) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    INDEX idx_transactions_user_spent_created (user_id, spent_on, created_at, id),
    CONSTRAINT fk_transactions_user FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT chk_transactions_amount CHECK (amount_cents BETWEEN 1 AND 100000000),
    CONSTRAINT chk_transactions_category CHECK (category <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
