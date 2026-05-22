CREATE TABLE users (
    id            SERIAL PRIMARY KEY,
    did           TEXT UNIQUE NOT NULL,
    handle        TEXT,
    access_token  TEXT,
    refresh_token TEXT,
    token_expiry  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ DEFAULT NOW()
);
