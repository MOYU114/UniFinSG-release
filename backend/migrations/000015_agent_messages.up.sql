-- Structured message content for the v2 agent. content keeps the plain-text
-- projection used by previews and legacy readers; parts is the ordered list
-- documented in design section 4.1. verified is NULL when the turn called no
-- tools, so "not checked" and "checked and failed" stay distinguishable.
ALTER TABLE messages
    ADD COLUMN parts JSON NULL AFTER content,
    ADD COLUMN turn_id CHAR(32) NULL AFTER parts,
    ADD COLUMN verified BOOLEAN NULL AFTER turn_id,
    ADD INDEX idx_messages_session_id (session_id, id);
