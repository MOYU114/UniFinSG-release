-- Per-user BYOK configuration. The API key is stored only as AES-256-GCM
-- ciphertext; api_key_last4 exists for display and never identifies the key.
CREATE TABLE llm_configs (
    user_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    kind ENUM('openai_compatible','anthropic') NOT NULL,
    base_url VARCHAR(512) NOT NULL,
    model VARCHAR(128) NOT NULL,
    api_key_ciphertext TEXT NOT NULL,
    api_key_last4 CHAR(4) NOT NULL,
    last_test_at DATETIME(6) NULL,
    last_test_ok BOOLEAN NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    CONSTRAINT fk_llm_configs_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
