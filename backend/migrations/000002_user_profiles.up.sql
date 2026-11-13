CREATE TABLE user_profiles (
    user_id BIGINT UNSIGNED NOT NULL,
    monthly_disposable_cents BIGINT NOT NULL,
    housing ENUM('on_campus', 'off_campus', 'with_family') NOT NULL,
    default_budget_cents BIGINT NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id),
    CONSTRAINT fk_user_profiles_user FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT chk_profiles_disposable CHECK (monthly_disposable_cents > 0),
    CONSTRAINT chk_profiles_budget CHECK (default_budget_cents >= 0),
    CONSTRAINT chk_profiles_housing CHECK (housing IN ('on_campus', 'off_campus', 'with_family'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
