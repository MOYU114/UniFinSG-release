-- Keep the compatible VARBINARY(255) widening on rollback: narrowing to 001's
-- VARCHAR(128) ascii_bin can truncate opaque subjects or merge identities that
-- differ by trailing spaces. A full rollback still removes cognito_sub in 001.
ALTER TABLE users
    DROP COLUMN cognito_username,
    DROP COLUMN email;
