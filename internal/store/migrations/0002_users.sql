-- Times are Unix milliseconds in UTC.

CREATE TABLE users (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash    TEXT NOT NULL,
    is_admin         INTEGER NOT NULL DEFAULT 0,
    configuration    TEXT NOT NULL DEFAULT '{}',
    created_at       INTEGER NOT NULL,
    last_login_at    INTEGER,
    last_activity_at INTEGER
) STRICT;

-- One access token per user and device; logging in again from the same
-- device replaces it. Only the SHA-256 of the token is stored.
CREATE TABLE tokens (
    token_hash     TEXT PRIMARY KEY,
    session_id     TEXT NOT NULL UNIQUE,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id      TEXT NOT NULL,
    device_name    TEXT NOT NULL,
    client         TEXT NOT NULL,
    client_version TEXT NOT NULL,
    remote_addr    TEXT NOT NULL,
    created_at     INTEGER NOT NULL,
    last_seen_at   INTEGER NOT NULL,
    UNIQUE (user_id, device_id)
) STRICT;

CREATE TABLE display_prefs (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pref_id TEXT NOT NULL,
    client  TEXT NOT NULL,
    data    TEXT NOT NULL,
    PRIMARY KEY (user_id, pref_id, client)
) STRICT;
