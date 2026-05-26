CREATE TABLE oauth_sessions (
    did        TEXT NOT NULL,
    session_id TEXT NOT NULL,
    data       JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (did, session_id)
);

CREATE TABLE oauth_auth_requests (
    state      TEXT PRIMARY KEY,
    data       JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
