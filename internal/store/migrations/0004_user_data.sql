-- Per-user state of a movie, extra or part of a movie. item_id is not a
-- foreign key: parts are addressed by their media file ID, and state is
-- kept while an item is missing.
CREATE TABLE user_data (
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_id     TEXT NOT NULL,
    played      INTEGER NOT NULL DEFAULT 0,
    play_count  INTEGER NOT NULL DEFAULT 0,
    position_ms INTEGER NOT NULL DEFAULT 0,
    last_played INTEGER,
    is_favorite INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, item_id)
) STRICT;
