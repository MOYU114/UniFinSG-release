ALTER TABLE users
    DROP INDEX uq_users_cognito_sub,
    DROP COLUMN cognito_sub;
