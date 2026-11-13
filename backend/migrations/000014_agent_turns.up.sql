-- One row per agent turn; credits_charged is an audit trail, not the source of
-- truth for the daily counter (Redis owns the live usage).
CREATE TABLE agent_turns (
    id CHAR(32) NOT NULL PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    session_id BIGINT UNSIGNED NOT NULL,
    mode ENUM('platform','byok') NOT NULL,
    kind ENUM('chat','tool','vision') NOT NULL,
    credits_charged INT NOT NULL DEFAULT 0,
    status ENUM('running','completed','awaiting_approval','failed') NOT NULL,
    model_calls INT NOT NULL DEFAULT 0,
    error_code VARCHAR(64) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    finished_at DATETIME(6) NULL,
    KEY idx_agent_turns_user_created (user_id, created_at),
    CONSTRAINT fk_agent_turns_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
