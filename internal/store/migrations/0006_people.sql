-- Cast and crew members from TMDB, looked up once each. data holds the
-- details as JSON, photo the file name of the portrait in the metadata
-- directory. Rows of people no movie lists any more are deleted.
CREATE TABLE people (
    tmdb_id    INTEGER PRIMARY KEY,
    status     TEXT NOT NULL,
    error      TEXT NOT NULL DEFAULT '',
    data       TEXT NOT NULL DEFAULT '',
    photo      TEXT NOT NULL DEFAULT '',
    fetched_at INTEGER NOT NULL
) STRICT;
