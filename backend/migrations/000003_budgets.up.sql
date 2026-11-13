CREATE TABLE budgets (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    month CHAR(7) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    total_cents BIGINT NOT NULL,
    warn_bp INT NOT NULL DEFAULT 8000,
    source VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'saved',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_budgets_user_month (user_id, month),
    CONSTRAINT fk_budgets_user FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT chk_budgets_total CHECK (total_cents >= 0),
    CONSTRAINT chk_budgets_warn CHECK (warn_bp BETWEEN 5000 AND 10000),
    CONSTRAINT chk_budgets_month CHECK (month REGEXP '^[0-9]{4}-(0[1-9]|1[0-2])$'),
    CONSTRAINT chk_budgets_source CHECK (
        source IN ('saved', 'profile_default')
        OR source REGEXP '^copied_from_[0-9]{4}-(0[1-9]|1[0-2])$'
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
