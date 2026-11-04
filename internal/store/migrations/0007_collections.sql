-- Film series from TMDB that at least two movies of the library belong
-- to, looked up once each. data holds the details as JSON, poster and
-- backdrop the file names of the artwork in the metadata directory.
CREATE TABLE collections (
    tmdb_id    INTEGER PRIMARY KEY,
    status     TEXT NOT NULL,
    error      TEXT NOT NULL DEFAULT '',
    data       TEXT NOT NULL DEFAULT '',
    poster     TEXT NOT NULL DEFAULT '',
    backdrop   TEXT NOT NULL DEFAULT '',
    fetched_at INTEGER NOT NULL
) STRICT;
