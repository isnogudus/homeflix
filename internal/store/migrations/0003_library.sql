-- Times are Unix milliseconds in UTC.

CREATE TABLE libraries (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE COLLATE NOCASE,
    collection_type TEXT NOT NULL
) STRICT;

-- A movie is a single file or a folder. Extras (behind the scenes,
-- trailers, ...) are items of type Video with parent_id set to the movie.
CREATE TABLE items (
    id            TEXT PRIMARY KEY,
    library_id    TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    parent_id     TEXT REFERENCES items(id) ON DELETE CASCADE,
    type          TEXT NOT NULL,
    extra_type    TEXT NOT NULL DEFAULT '',
    path          TEXT NOT NULL UNIQUE,
    title         TEXT NOT NULL,
    sort_title    TEXT NOT NULL,
    year          INTEGER,
    imdb_id       TEXT NOT NULL DEFAULT '',
    tmdb_id       TEXT NOT NULL DEFAULT '',
    date_created  INTEGER NOT NULL,
    missing_since INTEGER
) STRICT;

CREATE INDEX items_by_library ON items(library_id, type);
CREATE INDEX items_by_parent ON items(parent_id);
CREATE INDEX items_by_imdb ON items(imdb_id);

-- A movie has one or more versions (version label '' when there is only
-- one), each made of one file or several parts (part 1, 2, ...; 0 when the
-- version is not split).
CREATE TABLE media_files (
    id          TEXT PRIMARY KEY,
    item_id     TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    version     TEXT NOT NULL,
    part        INTEGER NOT NULL,
    path        TEXT NOT NULL UNIQUE,
    size        INTEGER NOT NULL,
    mtime       INTEGER NOT NULL,
    probe       TEXT,
    probe_error TEXT,
    probed_at   INTEGER,
    duration_ms INTEGER,
    container   TEXT NOT NULL DEFAULT '',
    bit_rate    INTEGER
) STRICT;

CREATE INDEX media_files_by_item ON media_files(item_id);
CREATE INDEX media_files_unprobed ON media_files(item_id) WHERE probe IS NULL AND probe_error IS NULL;
