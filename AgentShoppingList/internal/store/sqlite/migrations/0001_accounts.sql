CREATE TABLE accounts (
    id TEXT PRIMARY KEY NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE identities (
    issuer TEXT NOT NULL,
    subject TEXT NOT NULL,
    owner_id TEXT NOT NULL UNIQUE REFERENCES accounts(id),
    PRIMARY KEY (issuer, subject)
) STRICT;

CREATE TABLE preferences (
    owner_id TEXT PRIMARY KEY NOT NULL REFERENCES accounts(id),
    timezone TEXT NOT NULL CHECK (length(timezone) > 0),
    currency TEXT NOT NULL CHECK (length(currency) = 3 AND currency NOT GLOB '*[^A-Z]*'),
    preferred_retailer TEXT NOT NULL CHECK (length(preferred_retailer) > 0),
    week_start TEXT NOT NULL CHECK (week_start IN ('sunday', 'monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0)
) STRICT;
