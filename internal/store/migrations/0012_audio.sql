-- Music and audiobook libraries (stage 3).

-- The tags and stream facts of an audio file, extracted from the probe
-- when it is made, so that loading the catalog does not parse the probe
-- data of every track. JSON of media.AudioMeta.
ALTER TABLE media_files ADD COLUMN audio_meta TEXT;

-- Files next to the media that describe or illustrate it: album.nfo,
-- artist.nfo, metadata.json, folder.jpg, cover.jpg, backdrop.jpg,
-- logo.png and .lrc files. data holds what was read from a description
-- (JSON), '' for images and lyrics.
CREATE TABLE sidecars (
    path       TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    dir        TEXT NOT NULL,
    name       TEXT NOT NULL,
    size       INTEGER NOT NULL,
    mtime      INTEGER NOT NULL,
    data       TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX sidecars_by_library ON sidecars(library_id);

-- Playlists of a user. Entries have IDs of their own, so a track can be
-- in a playlist twice and be moved or removed by entry.
CREATE TABLE playlists (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created    INTEGER NOT NULL,
    updated    INTEGER NOT NULL
) STRICT;

CREATE INDEX playlists_by_user ON playlists(user_id);

-- item_id is not a foreign key: entries of tracks that went missing are
-- kept and shown as missing until the track returns.
CREATE TABLE playlist_entries (
    id          TEXT PRIMARY KEY,
    playlist_id TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
    item_id     TEXT NOT NULL,
    position    INTEGER NOT NULL
) STRICT;

CREATE INDEX playlist_entries_by_playlist ON playlist_entries(playlist_id, position);

-- Bookmarks and notes Moonfin keeps for audiobooks (its server plugin's
-- endpoints), per user and item, as the JSON the app sends.
CREATE TABLE bookmarks (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_id TEXT NOT NULL,
    kind    TEXT NOT NULL,
    data    TEXT NOT NULL,
    updated INTEGER NOT NULL,
    PRIMARY KEY (user_id, item_id, kind)
) STRICT;
