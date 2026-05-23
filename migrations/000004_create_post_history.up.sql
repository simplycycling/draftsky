CREATE TABLE post_history (
    id         SERIAL PRIMARY KEY,
    user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
    uri        TEXT NOT NULL,
    hashtags   TEXT[] NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
