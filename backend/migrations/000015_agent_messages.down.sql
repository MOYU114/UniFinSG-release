ALTER TABLE messages
    DROP INDEX idx_messages_session_id,
    DROP COLUMN verified,
    DROP COLUMN turn_id,
    DROP COLUMN parts;
