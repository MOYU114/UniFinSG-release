-- Sessions are reused from the legacy schema. kind reserves room for future
-- session types; v2 only writes 'agent'.
ALTER TABLE sessions
    ADD COLUMN kind VARCHAR(16) NOT NULL DEFAULT 'agent' AFTER title;
