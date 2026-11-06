-- How far the server may convert movies for a user: none (the original
-- file only), audio (video copied, audio converted) or all (video
-- transcoded as well). Jellyfin allows everything by default.
ALTER TABLE users ADD COLUMN conversion TEXT NOT NULL DEFAULT 'all'
    CHECK (conversion IN ('none', 'audio', 'all'));
