CREATE TABLE eval_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    participant_code CHAR(3) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    task ENUM('onboarding', 'add_expense', 'understand_budget', 'get_advice', 'find_offer') NOT NULL,
    event ENUM('start', 'end') NOT NULL,
    success BOOLEAN NULL,
    error_code VARCHAR(32) NULL,
    client_ts VARCHAR(35) CHARACTER SET ascii COLLATE ascii_bin NULL,
    received_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    INDEX idx_eval_events_task_participant_time (task, participant_code, received_at, id),
    INDEX idx_eval_events_time (received_at, id),
    CONSTRAINT chk_eval_participant CHECK (participant_code REGEXP '^P[0-9]{2}$'),
    CONSTRAINT chk_eval_success CHECK (success IS NULL OR success IN (0, 1)),
    CONSTRAINT chk_eval_end_success CHECK (event <> 'end' OR success IS NOT NULL)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
