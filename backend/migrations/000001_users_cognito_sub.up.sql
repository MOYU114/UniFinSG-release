ALTER TABLE users
    ADD COLUMN cognito_sub VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
    ADD UNIQUE INDEX uq_users_cognito_sub (cognito_sub);
