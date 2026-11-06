-- The video keyframes of a media file, found once per content: a row whose
-- size and mtime differ from the file's is stale and is found again.
-- times holds the presentation times in microseconds, the first as a
-- signed varint and every further one as the varint of its distance to the
-- one before, a few kilobytes for a feature film. A row with an error and
-- no times records a file whose keyframes could not be found.
CREATE TABLE keyframes (
    file_id    TEXT PRIMARY KEY REFERENCES media_files(id) ON DELETE CASCADE,
    size       INTEGER NOT NULL,
    mtime      INTEGER NOT NULL,
    source     TEXT NOT NULL DEFAULT '',
    times      BLOB,
    count      INTEGER NOT NULL DEFAULT 0,
    max_gap_ms INTEGER NOT NULL DEFAULT 0,
    note       TEXT NOT NULL DEFAULT '',
    error      TEXT NOT NULL DEFAULT '',
    took_ms    INTEGER NOT NULL,
    made_at    INTEGER NOT NULL
) STRICT;
