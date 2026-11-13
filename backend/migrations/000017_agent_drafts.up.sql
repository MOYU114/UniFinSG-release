-- Pending writes proposed by the agent. The model never writes the ledger: a
-- propose_* tool only inserts a row here and the decision endpoint consumes it
-- atomically before executing anything.
--
-- payload stores the validated proposal plus the optional pre-change snapshot
-- and warnings, because the design's table has no dedicated columns for them:
--   {"input": {...}, "before": {...}, "warnings": ["possible_duplicate"]}
-- Readers expose "input" as the draft payload and "before"/"warnings" next to
-- it, so the wire format stays exactly the one documented in section 4.3.
CREATE TABLE agent_drafts (
    id CHAR(26) NOT NULL PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    session_id BIGINT UNSIGNED NOT NULL,
    turn_id CHAR(32) NOT NULL,
    tool_call_id VARCHAR(64) NOT NULL,
    kind ENUM('create_transactions','update_transaction','delete_transaction','update_budget') NOT NULL,
    payload JSON NOT NULL,
    final_payload JSON NULL,
    status ENUM('pending','approved','rejected','expired','failed') NOT NULL DEFAULT 'pending',
    reject_reason VARCHAR(200) NULL,
    result JSON NULL,
    expires_at DATETIME(6) NOT NULL,
    decided_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    KEY idx_agent_drafts_user_status (user_id, status, created_at),
    CONSTRAINT fk_agent_drafts_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT fk_agent_drafts_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
