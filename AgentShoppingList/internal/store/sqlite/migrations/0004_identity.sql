ALTER TABLE accounts ADD COLUMN disabled_at INTEGER;

CREATE TABLE login_attempts (
    token_hash BLOB PRIMARY KEY NOT NULL CHECK (length(token_hash) = 32),
    encrypted_state BLOB NOT NULL,
    expires_at INTEGER NOT NULL
) STRICT;
