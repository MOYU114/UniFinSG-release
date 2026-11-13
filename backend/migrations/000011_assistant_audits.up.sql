CREATE TABLE assistant_audits (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    snapshot_hash VARCHAR(128) NOT NULL,
    intent VARCHAR(32) NOT NULL,
    verified BOOLEAN NOT NULL,
    fallback BOOLEAN NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_assistant_audits_user_created (user_id, created_at),
    CONSTRAINT fk_assistant_audits_user FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
