-- Metadata of a movie from TMDB. source says how the movie was matched
-- ("imdb", "search" or "manual"); match_key is the file name data the
-- match was made for, so a renamed file is matched again unless the match
-- was made by hand.
CREATE TABLE metadata (
    item_id    TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    source     TEXT NOT NULL,
    match_key  TEXT NOT NULL,
    status     TEXT NOT NULL,
    error      TEXT NOT NULL DEFAULT '',
    tmdb_id    INTEGER,
    data       TEXT NOT NULL DEFAULT '',
    poster     TEXT NOT NULL DEFAULT '',
    backdrop   TEXT NOT NULL DEFAULT '',
    fetched_at INTEGER NOT NULL
) STRICT;
