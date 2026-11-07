# homeflix — Stage 3 plan: music and audiobooks

Stages 1 and 2 made homeflix a server for movies: Jellyfin API for Moonfin,
a web client of its own, remuxing and transcoding for browsers. Stage 3 adds
the two other collections of the house: music and audiobooks. Both are
read from the files and their tags alone; nothing is looked up outside.

## 1. Goals and non-goals

Goals:

- A music library built from the tags: artists, album artists, albums with
  discs, tracks, genres, compilations, covers and artist pictures. Moonfin
  browses it, plays albums, artists and mixes, and shows lyrics.
- An audiobook library: one item per book with author, narrator, series,
  description, cover and chapters. Where the listener stopped is kept per
  book and is the same in Moonfin and the web client.
- Every track and book plays in Moonfin and in current browsers: the file
  itself where the player takes it, AAC otherwise (WMA, Musepack).
- Favourites, play counts, recently played and recently added, playlists
  (made in Moonfin or the web client), lyrics from `.lrc` files and tags.
- The web client gets music and audiobook pages and an audio player that
  keeps playing while the listener browses, with a queue for music and
  chapters, speed and resume for books.

Non-goals for stage 3: podcasts and internet radio, scrobbling (Last.fm,
ListenBrainz), fetching artwork, lyrics or biographies from the internet,
editing tags, audio fingerprinting, smart playlists, SyncPlay and remote
control, offline downloads in the web client, e-books, gapless playback
and crossfading in the server (they are the player's business).

## 2. Facts this plan rests on

### 2.1 The music collection

Surveyed on 2026-11-07; tag shares from 400 tracks picked at random.

| Tree | Tracks | Size | Content |
|---|---|---|---|
| FLAC | 3,851 FLAC, 1 WAV | 104 GB | 16 bit/44.1 kHz, a few albums 24 bit |
| MP3 | 19,438 MP3, 1,532 M4A (AAC), 1,737 WMA, 17 Musepack, 14 Ogg Vorbis | 139 GB | MP3 mostly 160–255 kbit/s |

- Both trees are laid out alike: `Albums/<artist>/<album>[/Disc NN]`,
  `Compilations/<album>`, `Singletons`, `Soundtracks/<album>`. Together
  about 900 artist folders and 2,230 album folders. No album is in both
  trees, but many artists are, so artists are merged across trees by their
  tags, not by folder.
- Next to the two trees lie folders for importing and tagging. They are not
  part of the library; the configuration names the two trees as paths and
  nothing else.
- The tags were written with MusicBrainz Picard and are nearly complete:
  title, artist, album and track number in every file, album artist in
  99.8 %, disc number in 99.5 %, date in 94 %, genre in 89 %, MusicBrainz
  release and artist IDs in 85 %, the compilation flag in 93 %, multiple
  artists (`ARTISTS`) in 61 %, R128 or ReplayGain values in about 40 %.
- Covers are embedded in nearly every track; 69 album folders also have a
  `folder.jpg`. Artist folders have `folder.jpg` (629), `backdrop.jpg`
  (608) and `logo.png` (573), left over from an earlier Jellyfin, as are
  2,860 `album.nfo`/`artist.nfo` files.
- 323 tracks have an `.lrc` file next to them; many MP3s carry a lyrics
  tag, mostly empty.
- No M3U playlists are in the collection.

### 2.2 The audiobook collection

- 45 GB: 231 `.m4b` files (AAC, about 64 kbit/s, up to about ten hours
  each) and 3 books as folders of MP3 files (59, 22 and 1 files).
- Layout `<author>/<title>/`, one book per folder; 9 books (`.m4b`) lie
  loose at the top, and one folder holds several `.m4b` files.
- Tags of the `.m4b` files: title and album are the book title, artist and
  album artist the author, composer the narrator (most), genre
  "Audiobook". About two thirds carry chapters.
- Nearly every book folder has a `cover.jpg` and a `metadata.json` written
  by Audiobookshelf, with title, subtitle, authors, narrators, series,
  genres and tags, year, publisher, description and chapters. It is the
  best source for books whose files lack chapters or a description.

### 2.3 What Moonfin asks for

From Moonfin's source (Moonfin-Core, 2026-11-06), Jellyfin backend only.

General:

- The library type comes from `CollectionType`: `music` opens the music
  screens, `books` the book screens. homeflix reports `books` for
  audiobooks, as Jellyfin does.
- Nearly every list is `GET /Users/{id}/Items` with `ParentId`,
  `IncludeItemTypes`, `ArtistIds`, `GenreIds`, `Filters` (`IsPlayed`,
  `IsFavorite`, `IsResumable`), `IsFavorite`, `NameStartsWith`,
  `NameLessThan`, `SortBy`, `StartIndex`, `Limit` and
  `EnableTotalRecordCount`. `TotalRecordCount` is needed for paging, also
  with `Limit=0` (Moonfin counts songs that way). `AlbumArtistIds` is
  never sent.
- Tracks are listed with few `Fields`, but Moonfin reads `AlbumId`,
  `AlbumPrimaryImageTag`, `Album`, `AlbumArtist`, `AlbumArtists`,
  `Artists`, `ArtistItems`, `IndexNumber`, `ParentIndexNumber`,
  `RunTimeTicks`, `UserData`, `ImageTags` and `MediaType` anyway. These
  come whatever `Fields` asks for, as in Jellyfin. `MediaType` must be
  `Audio`, or Moonfin treats the item as a video (and asks for prerolls).

Music:

- The library screen asks for `/Items/Latest` without item types and
  expects albums back (Jellyfin groups tracks into albums there); recently
  played tracks (`SortBy=DatePlayed`, `Filters=IsPlayed`); favourite
  albums; playlists; `/Artists/AlbumArtists`; artists as `/Items` with
  `IncludeItemTypes=MusicArtist` (not `/Artists`); albums; `/Genres` with
  `IncludeItemTypes=MusicAlbum` and the album count of each genre. Sorts:
  `SortName`, `DateCreated`, `ProductionYear`, `PremiereDate`,
  `AlbumArtist`, `Random`, `PlayCount`, `DatePlayed`.
- An album's tracks are asked for with `ParentId=<album>` and **without**
  `Recursive`, sorted by `ParentIndexNumber,IndexNumber`. Tracks are
  therefore direct children of their album, whatever disc folders the
  files lie in. Disc headers appear when tracks have different disc
  numbers.
- An artist page asks for albums and tracks with `ArtistIds`, and for
  `/Items/{id}/Similar`. Artist IDs must work with `/Users/{id}/Items/{id}`
  and with the favourite endpoints.
- `/Items/{id}/InstantMix?Limit=25` is used only to extend a queue that is
  about to run out ("radio").
- `/Audio/{id}/Lyrics` is asked for every track; any error means "no
  lyrics". Only `Lyrics[].Start` (ticks) and `Lyrics[].Text` are read.
- `/MusicGenres`, `HasLyrics` and the item's `NormalizationGain` are not
  used. Volume normalisation is read from the first audio stream of the
  media source (`NormalizationGain`, in dB).

Playing:

- Moonfin calls `PlaybackInfo` for each track with its device profile and
  plays `/Audio/{id}/stream?Static=true` when the source supports direct
  play; otherwise it follows `TranscodingUrl` unchanged.
  `/Audio/{id}/universal` is only a fallback when `PlaybackInfo` fails.
- Direct play in the native apps: containers `aac, ac3, alac, ape, dts,
  eac3, flac, m4a, m4b, mka, mp3, oga, ogg, opus, wav, wma`, but the codec
  list has no `wmav2` and no Musepack, so WMA and Musepack need
  transcoding. Transcoding profile: HLS, MPEG-TS container, AAC.
- An embedded cover must be reported as a stream of type `EmbeddedImage`,
  not `Video`, or Moonfin takes the track for a video.
- There is no gapless playback and no preloading of the next track; every
  track is its own play session.

Audiobooks:

- In a `books` library Moonfin lists `Book` and `AudioBook` items only;
  items of type `Audio` do not appear as books.
- The author is taken from `People` with type `Author`, else
  `AlbumArtist` or `Artists`; an author list comes from `/Artists` with
  the library as parent. Series are grouped by `SeriesName`. The narrator
  is not shown.
- Chapters come from `Chapters[]` (`StartPositionTicks`, `Name`) of the
  item. Several media sources of one item are versions, not parts.
- A book as one `AudioBook` item with one file and chapters works best
  everywhere, also in CarPlay and Android Auto, which play exactly one
  item. Folders of files work only in parts of the app.
- The audiobook player never sends `/Sessions/Playing/Stopped`; the
  position has to be taken from the progress reports (every 5 s), and the
  book is never marked played by the client.
- Bookmarks and notes are kept in the app and synchronised with Moonfin's
  own server plugin (`GET`/`POST /Moonfin/Bookmarks/{id}`,
  `POST /Moonfin/Bookmarks/{id}/Notes`); errors are ignored, so the
  feature can be added later.
- Continue listening: `/UserItems/Resume` with `IncludeItemTypes=Book,
  AudioBook` or `MediaTypes=Audio`.

Playlists:

- Moonfin hides a playlist that reports neither `ChildCount` nor
  `RecursiveItemCount`, and shows it under music only when all its
  entries are of type `Audio`.
- Editing: `POST /Playlists` (`Name`, `Ids`, `IsPublic`),
  `POST /Playlists/{id}/Items?Ids=`, `DELETE /Playlists/{id}/Items?EntryIds=`,
  `POST /Playlists/{id}/Items/{entry}/Move/{index}`, `POST /Playlists/{id}`
  (rename), `DELETE /Items/{id}`. Editing is offered only when every entry
  has a `PlaylistItemId`.

### 2.4 Jellyfin's play-state rules for audio

From `UserDataManager.UpdatePlayState`:

- Tracks follow the movie rules, and since nearly every track is shorter
  than the minimum resume duration (5 minutes), a track counts as played
  once 5 % of it has been reported, and no position is kept.
- Audiobooks have rules of their own: a position in the first 5 minutes is
  not kept, and within the last 5 minutes the book counts as played and
  the position is reset (`MinAudiobookResume`, `MaxAudiobookResume`).

### 2.5 What browsers can play

MP3, AAC (M4A) and FLAC play in Safari, Chrome, Firefox and Edge; Ogg
Vorbis not in every Safari; WMA and Musepack in none. So about 1,770
tracks (7 %) need conversion to AAC for browsers, the same WMA and
Musepack files as for Moonfin. All audiobooks play directly.

### 2.6 Cost

- ffprobe takes about 11 ms per track once the file is cached. Measured in
  A0: the first scan with probing of all 26,889 files (music and books)
  took 1 min 55 s with 6 probe workers; an unchanged rescan takes about a
  second; the database grows by 19 MB; the catalog loads in 0.65 s. Probing already stores tags and chapters
  (`-show_format -show_chapters`).
- Converting a track to AAC takes a small share of one core; audio
  conversions are cheap enough not to need a limit of their own.

## 3. Architecture

### 3.1 Libraries and configuration

A library gets a type; without one it holds movies, as now:

```
library "Musik" {
	type music
	path /srv/music/flac
	path /srv/music/mp3
}
library "Hörbücher" {
	type audiobooks
	path /srv/audiobooks
}
```

`CollectionType` is `music` and `books`. The scanner walks each type with
rules of its own: a music library yields one item per audio file, an
audiobook library one item per book.

### 3.2 Data model

- **Tracks** are items of type `Audio`, each with one media file, as
  movies have. When a file is probed, the tags that matter are copied into
  a table of their own (`audio_tags`: title, artists, album artist,
  album, track and disc number, year, genres, compilation flag,
  MusicBrainz IDs, gain), so that loading the catalog does not parse
  26,600 probe documents.
- **Albums and artists** are not files. The catalog derives them from the
  tags when it loads, with IDs computed from what identifies them: the
  MusicBrainz release ID, else album artist and album title (normalised);
  the MusicBrainz artist ID, else the normalised name. The IDs stay the
  same across scans and restarts, so favourites and play counts of albums
  and artists need no table of their own. Retagging an album that has no
  MusicBrainz ID gives it a new ID and loses its favourite mark.
- An album's tracks are its children (`ParentId`), sorted by disc and
  track, whatever folders they lie in. Tracks without an album tag belong
  to no album and appear as tracks only. Compilations have the album
  artist "Various Artists" (MusicBrainz) and appear under it, as in
  Jellyfin.
- `Artists` and `ArtistItems` come from the multiple-artists tag, else the
  artist tag; `AlbumArtists` from the album artist. An artist exists when
  some track or album names it.
- **Books** are items of type `AudioBook`. A folder with one audio file, or
  a loose file, is one book. A folder with several `.m4b` files is one book
  per file. A folder of MP3s is one book whose files play one after
  another as one stream (3.4), with one chapter per file unless the files
  have chapters.
- Book metadata comes from `metadata.json` first, then the tags, then the
  folder name: title, authors (`People` of type `Author`, and
  `AlbumArtist`), narrators (`People` of type `Narrator`, also as the
  composer), series (`SeriesName`, index), description, genres, year,
  publisher. Chapters come from the file, else from `metadata.json`.
- User data stays in `user_data`: tracks, albums, artists, books and
  playlists are items with IDs like any other.
- New tables: `audio_tags`, `playlists` and `playlist_entries` (3.6), and
  later `bookmarks` (Moonfin's plugin endpoints).

### 3.3 Images

- Album cover: `folder.jpg` or `cover.jpg` in the album folder, else the
  picture embedded in the first track, extracted once and kept with the
  other generated images. Tracks report their album's cover
  (`AlbumId`, `AlbumPrimaryImageTag`) and have no picture of their own.
- Artist: `folder.jpg` (Primary), `backdrop.jpg` (Backdrop) and
  `logo.png` (Logo) from the artist folder, else the cover of the newest
  album.
- `album.nfo` and `artist.nfo` take precedence over the tags for what
  they hold (title, sort name, year, genres, MusicBrainz IDs, review or
  biography); see section 6.
- Book: `cover.jpg`, else the embedded picture.

### 3.4 Playback

- **Direct:** `/Audio/{id}/stream` with ranges, as the movie streams.
- **Decision** in `PlaybackInfo` from the device profile, as for movies:
  direct play when container and codec are in the profile, else a
  `TranscodingUrl` to an AAC stream. This also brings the `TranscodingUrl`
  for movies (left open in stage 2) one step closer, since both share the
  profile evaluation.
- **Conversion:** HLS with fixed 6 s segments through the existing
  session manager, audio only, AAC 256 kbit/s stereo, in MPEG-TS as
  Moonfin's profile asks (fMP4 for the web client if that is simpler).
  Seeking restarts ffmpeg at the segment, as for movies. Audio sessions
  count against `transcode workers` but not against `video workers`.
- **Multi-file books** play as one stream: ffmpeg reads the files in order
  (concat demuxer) and the playlist spans the whole book. Duration and
  chapters are those of the whole book, so resume, chapters and CarPlay
  work as for an `.m4b`.
- `/Audio/{id}/universal` answers like `PlaybackInfo` followed by the
  chosen stream, for clients without `PlaybackInfo`.
- **Normalisation:** the audio stream reports `NormalizationGain` from
  the ReplayGain or R128 track gain (R128 converted to the ReplayGain
  reference), where present.
- The per-user conversion level of stage 2 applies: *none* plays only what
  the player takes directly; *audio* and *all* allow AAC conversion
  (Jellyfin's `EnableAudioPlaybackTranscoding`).

### 3.5 Play state

- Tracks: played and play count from the start and stop reports with the
  rules of 2.4; `last_played` gives recently played and the "most played"
  mix.
- Albums and artists are not played themselves; `UserData.Played` of an
  album is true when all its tracks are.
- Books: positions with the audiobook rules of 2.4. Because Moonfin never
  sends a stop for books, the last progress report counts as the stop
  after a minute without reports, and the end rule marks the book
  played.

### 3.6 Playlists

- Owned by one user, private (Jellyfin's `IsPublic` is accepted and
  ignored for now). Entries have IDs of their own (`PlaylistItemId`), so
  the same track can appear twice and be moved and removed.
- Endpoints of 2.3; `ChildCount`, `RecursiveItemCount` and `MediaType`
  are always reported.
- A playlist that refers to a track no longer in the library keeps the
  entry and shows it as missing until the track returns or the grace
  period of the scanner ends.

### 3.7 Lyrics and mixes

- Lyrics: an `.lrc` file with the track's name (synchronised), else the
  lyrics tag (plain text). Empty tags count as none; no lyrics is a 404.
- Instant mix (the "radio" of 2.3): tracks of the same genres and album
  artists, shuffled, without the queue's own tracks. Similar artists: by
  shared genres. No outside data.

### 3.8 Web client

- Music: library page with albums, artists, genres and playlists; album
  page with discs and tracks; artist page with albums and top tracks;
  search covers artists, albums and tracks.
- Audiobooks: grid by author or series; book page with description,
  narrator, chapters and resume.
- One audio player at the bottom of every page: queue (play album, play
  next, add to queue, shuffle, repeat), cover and title, seek, volume;
  for books chapter list, speed (0.75–2×) and a sleep timer. Progress
  reports as in the video player.
- The player has to survive moving to another page. The web client
  renders pages on the server; links inside the client are therefore
  loaded with `fetch` and only the content area is replaced
  (`history.pushState`), while the player stays. Without scripts, links
  work as before and the player is simply absent.

### 3.9 Admin UI and commands

- The overview counts tracks, albums, artists and books per library, and
  lists files that cannot be read and tracks without album or album
  artist.
- `homeflix status` shows the new libraries; `homeflix scan` covers them.

## 4. Milestones

Each milestone ends with a check in Moonfin on the iMac and, from A5 on,
in Safari, Chrome and Firefox.

| # | Content | Done when | Estimate |
|---|---|---|---|
| A0 | Library types in the configuration and manpage, `CollectionType`, scanner for music (one item per file) and audiobooks (one item per book), `audio_tags` filled when probing, migration, status counts; first-scan time measured | both libraries scan completely; counts match 2.1 and 2.2; an unchanged rescan takes well under a second | 2–3 days |
| A1 | Music in the Jellyfin API: `Audio`, `MusicAlbum`, `MusicArtist` items with derived IDs, album children, `/Artists`, `/Artists/AlbumArtists`, `/Genres` with album counts, `ArtistIds`, music sorts and filters, `Latest` grouped into albums, covers and artist pictures, direct streaming, play state of tracks | Moonfin shows the music library with its rows, plays an album with two discs in order, opens artists from albums and back; covers and artist pictures appear | 1–1.5 weeks |
| A2 | Audio in `PlaybackInfo` with the device profile, `TranscodingUrl`, audio-only HLS with AAC, `/Audio/{id}/universal`, `NormalizationGain`, conversion level | a WMA album and a Musepack track play in Moonfin with seeking; a user with level *none* gets a clear refusal | 3–4 days |
| A3 | Audiobooks: `AudioBook` items, `metadata.json`, authors, narrators, series, chapters, multi-file books as one stream, audiobook resume rules, continue listening | Moonfin's audiobook player shows chapters, resumes a ten-hour `.m4b` and a 59-file MP3 book where it stopped; a book finished in the last five minutes counts as played | 4–5 days |
| A4 | Favourites and rows for music, mixes, playlists with editing, lyrics, instant mix, similar artists, Moonfin bookmarks and notes | a playlist made in Moonfin can be reordered and edited there; synchronised lyrics scroll; the radio extends a queue; a bookmark set in one Moonfin app shows in another | 4–5 days |
| A5 | Web client: music and audiobook pages, the persistent player with queue, chapters, speed and sleep timer, content loading that keeps the player | music plays on while browsing on phone and laptop; a book stopped in the browser resumes in Moonfin and the other way round; WMA plays in Safari | 1.5 weeks |
| A6 | Polish: README, manpage, API coverage, smoke test with a generated test album and book, admin overview | the smoke test browses and plays a track, a converted track and a book | 2–3 days |

Altogether about six weeks. A1 is the largest step because albums and
artists are the first items that are not files.

## 5. Testing

- Scanner tests on generated files (ffmpeg with tags and chapters): album
  over two disc folders, compilation, track without album, artist in both
  trees, book with `metadata.json`, loose book, folder of MP3s, folder of
  several `.m4b`.
- ID stability: the same tags give the same album and artist IDs after a
  rescan and a restart; moving files keeps them.
- API tests per Moonfin request of 2.3, with the exact parameters, so a
  missing `TotalRecordCount`, `ChildCount` or `MediaType` is caught.
- Playback decisions from the native and web device profiles of Moonfin
  for each codec of 2.1.
- Play-state rules of 2.4 for tracks and books, including books with
  progress reports only.
- HLS tests on short generated tracks and a concatenated book: playlist
  length, segment continuity, seek restart.
- Manual acceptance in Moonfin on the iMac and in the three browsers.

## 6. Decisions

Settled on 2026-11-07:

1. **Clients.** Moonfin and the web client for now; other clients
   (Finamp, for example) later.
2. **Persistent player in the web client.** Replacing the content area, as
   in 3.8.
3. **Albums without MusicBrainz ID.** By tags, as planned (album artist and
   title). Part of the library was organised with beets, which writes the
   same tags as Picard.
4. **`.nfo` files.** Read wherever they are and take precedence over the
   tags: `album.nfo` for albums, `artist.nfo` for artists (name, sort name,
   biography, genres, MusicBrainz IDs, years), as Jellyfin does.
5. **Multi-file books.** Played as one stream; converting them to `.m4b`
   may be considered later.
6. **Shared playlists.** Later. The data model leaves room for them: a
   table of shares (user, may edit) next to the owner, reported as
   Jellyfin's `Shares` and `OpenAccess`.

## 7. Status

Built on 2026-11-07, A0 to A6, checked with unit and API tests, the smoke
test (a generated album with an MP3 and a WMA track, and an M4B with
chapters) and a throwaway instance over the real collections in Chrome.
Not yet checked: Moonfin itself, Safari and Firefox.

Found on the real collections:

- 26,578 tracks, 2,062 albums, 2,689 artists (842 of them album
  artists), 232 books, 210 genres.
- 12 FLAC files of one album cannot be read by the account homeflix runs
  as (permission denied); they are left out and reported by the scan.
- 452 albums carry the genre "Data", written by a tagging tool; homeflix
  shows the tags as they are.
- 99 books have no chapters in their files nor in `metadata.json`.

Built differently from the plan:

- Sidecar files (`.nfo`, `metadata.json`, pictures, `.lrc`) are recorded by
  the scanner in a table of their own, so the catalog finds them without
  touching the disks; descriptions are read once and again only when they
  change.
- The tags of a track are kept as JSON in a column of its media file
  (`audio_meta`) instead of a table of columns.
- Tracks and books are items of the catalog like movies, which gives them
  the media sources, playback, play state and images of movies; albums and
  artists are built in memory with derived IDs.
- A library's children without `Recursive` are none: Moonfin asks for them
  to queue a book's "siblings", and Jellyfin's answer would be folders.
- In the web client the queue is kept in the browser's session storage, so
  after a full page load the player comes back paused where it was.
