-- Passkeys (WebAuthn credentials) of users. A passkey belongs to one
-- relying party, the host of the origin it was registered on.
CREATE TABLE passkeys (
    credential_id BLOB PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rp_id         TEXT NOT NULL,
    name          TEXT NOT NULL,
    data          TEXT NOT NULL, -- the credential as JSON, from the WebAuthn library
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER
) STRICT;

CREATE INDEX passkeys_user ON passkeys (user_id);
