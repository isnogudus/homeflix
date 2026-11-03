# homeflix — Stage 1 plan: direct play

homeflix is a media server that speaks the Jellyfin server API, so existing
Jellyfin clients can use it unchanged. It is written in Go and runs natively
on FreeBSD. Stage 1 serves a movie library for direct play: the client gets
the original file and decodes it itself. Server-side remuxing and transcoding
come later (stage 2: audio-only HLS fallback; stage 3 only if a client needs
it).

## 1. Goals and non-goals

Goals for stage 1:

- Moonfin 3.0+ on macOS can find the server, log in, browse the movie library
  (sorted by name, year or date added), search, open a details page with the
  audio and subtitle pickers filled, play with seeking, and resume later.
- Watch state (played, position, favourite) is stored per user and survives
  rescans, file renames and server restarts.
- No outbound network connection unless the operator enables it explicitly.
  Every outbound request is logged.
- Runs as a single static binary on FreeBSD 15 under rc.d, without Docker,
  PostgreSQL or Redis.

Non-goals for stage 1: transcoding or remuxing, TV series, music, live TV,
playlists, collections, SyncPlay, QuickConnect, plugins, the Jellyfin admin
dashboard API, and any own web frontend.

## 2. Facts this plan rests on

Library (`/media/movies`, read-only inside the jail):

| Folder   | Entries | Formats                              | IMDb ID in name |
|----------|---------|--------------------------------------|-----------------|
| `recent` | 1463    | 1379 mp4, 45 avi, 23 mkv, 2 m4v, ... | 24              |
| `archive`| 271     | 268 mkv                              | all             |

- Naming scheme: `Title (Year) [imdbid-tt1234567].ext`. Some names are
  irregular, for example `#9 (2009).avi [imdbid-tt0472033].mp4`.
- Multi-part movies live in a folder: `Delete (2013) [imdbid-…]/Delete (2013)
  part1.mp4`, `part2.mp4`. One folder has no year (`Ben Hur- 50th Anniversary
  UCE/`).
- The files carry no poster, plot or NFO. MKVs have good stream titles and
  languages (for example `Deutsch 5.1 (AC3)`, `English 7.1 (DTS-HD MA)`, PGS
  subtitles including forced ones).
- 50 archive MKVs contain a truncated E-AC3 compatibility track (see
  `truncated-audio-tracks.txt`). It matters only for server-side remuxing,
  not for direct play.

Client (Moonfin, source `Moonfin-Client/Moonfin-Core`,
`packages/server_jellyfin`):

- Detects the server type from `ProductName` in `/System/Info/Public`; a name
  containing "Jellyfin" selects the Jellyfin code path. Version gates are
  minor (`>= 10.9` for QuickConnect web links); report `10.11.x`.
- Media URLs carry the token as `ApiKey` query parameter.
- Moonfin 3.0 on macOS direct-plays MKV with DTS-HD MA and PGS (verified with
  remux on 2026-11-01). Moonfin 2.x sends a required h264 `RefFrames <= 4`
  rule for widths >= 1900; it can only pass if the server reports
  `RefFrames`.
- Sort keys used by the client, by frequency: `SortName`, `IndexNumber`,
  `Random`, `DatePlayed`, `DateCreated`, `ProductionYear`, `PremiereDate`,
  `PlayCount`.
- Fields requested most: `Overview`, `Genres`, `People`, `Trickplay`,
  `Studios`, `SortName`, `Chapters`, `Path`, `MediaStreams`, `DateCreated`,
  `ProviderIds`, `MediaSources`.

Environment: the development jail (FreeBSD 15.1, amd64, 12 cores, 32 GB),
ffmpeg and ffprobe from pkg. Go 1.27 is available on the development Mac;
the jail does not need a Go toolchain because the binary is cross-compiled.

## 3. Alternatives checked

- **remux** (Rust): ran on FreeBSD after two patches, but phones home by
  default (RemuxDB submissions with filenames), strips subtitles from item
  details, ignores alphabetical sort on system views, and carries a large
  torrent/IPTV surface. Rejected on 2026-11-01.
- **Silo** (`Silo-Server/silo-server`, Go, AGPL-3.0, pre-release): found while
  preparing this plan. Moonfin supports it natively. About 750k lines of Go;
  requires PostgreSQL with pgvector and Redis; distributed as a Linux Docker
  image; fetches plugin and theme catalogs from GitHub and has a push relay
  at `push.siloserver.org` by default. Non-Linux fallbacks exist in the code,
  but FreeBSD is not a documented target. Not evaluated in practice yet; see
  open decision 1.

homeflix is implemented from the Jellyfin OpenAPI specification and observed
client behaviour. No code is copied from remux or Silo.

## 4. Architecture

One process, one binary, one SQLite database, one cache directory.

```
cmd/homeflix/          main, subcommands: serve, user add|passwd|list, scan
internal/config/       config file + flags, validated at startup
internal/store/        SQLite (modernc.org/sqlite, pure Go), migrations
internal/library/      directory walk, filename parser, multi-part grouping
internal/probe/        ffprobe runner, worker pool, result cache
internal/media/        domain model: Movie, MediaFile, Stream, Chapter
internal/metadata/     providers: local (filename + tags), tmdb (opt-in)
internal/images/       image sources, resize, on-disk cache
internal/outbound/     the only HTTP client allowed to leave the host
internal/auth/         users, password hashing, tokens, header parsing
internal/jellyfin/     HTTP API: routing, DTOs, handlers, device profiles
internal/stream/       file serving with Range, multi-part
deploy/freebsd/        rc.d script, sample config
docs/                  this plan, API coverage table, decisions
```

### 4.1 Storage

SQLite in WAL mode via `modernc.org/sqlite`, so the build stays
`CGO_ENABLED=0` and cross-compiles from the Mac. Tables:

- `libraries(id, name, collection_type, paths)`
- `items(id, library_id, type, title, sort_title, original_title, year,
  imdb_id, tmdb_id, overview, tagline, genres, studios, people,
  community_rating, official_rating, premiere_date, date_created,
  folder_path, missing_since)`
- `media_files(id, item_id, part_index, rel_path, size, mtime, container,
  duration_ticks, bitrate, probe_json, probed_at)`
- `images(item_id, kind, idx, source, path, width, height, tag)`
- `users(id, name, password_hash, is_admin, policy_json)`
- `tokens(token, user_id, device_id, device_name, client, version, created,
  last_seen)`
- `user_data(user_id, item_id, played, play_count, position_ticks,
  last_played, is_favorite)`
- `display_prefs(user_id, pref_id, client, json)`

Streams and chapters are not normalised into tables; they are mapped from the
stored ffprobe JSON when a response is built. That keeps the probe as the
single source of truth and makes adding a new stream field a code change, not
a migration.

Item IDs are random 128-bit values in Jellyfin's 32-hex-digit format,
assigned once. On rescan a file is matched to its item by relative path,
then by IMDb ID, then by size plus duration. That way user data survives
renames and moves, and nothing needs a purge.

### 4.2 Scanning

1. Walk each library path; a file is a movie, a folder with `partN` files is
   one multi-part movie.
2. Parse title, year, IMDb ID and part number from the name.
3. Skip files whose size and mtime match the stored row; otherwise queue
   them for ffprobe (`-show_format -show_streams -show_chapters`, JSON).
   Worker pool size is configurable (default 4).
4. Files no longer found get `missing_since` set; they disappear from listings
   but keep their user data for a configurable grace period.
5. Triggered at startup, by `POST /Library/Refresh`, by `homeflix scan`, and
   on a schedule. Filesystem notification is not needed for stage 1.

Mapping from ffprobe to Jellyfin `MediaStream` must include, besides codec
and language: `RefFrames` (from `refs`), `Profile`, `Level`, `BitDepth`,
`Width`, `Height`, `AverageFrameRate`, `IsInterlaced`, `VideoRange` and
`VideoRangeType` (HDR10, HLG, Dolby Vision from colour transfer and side
data), `Channels`, `ChannelLayout`, `SampleRate`, `IsDefault` and `IsForced`
(from disposition), `Title`, `DisplayTitle` (built like Jellyfin's), and
`IsTextSubtitleStream`.

### 4.3 Metadata and images

The `local` provider is always on and needs no network: title, year and IMDb
ID from the filename, stream data from ffprobe, a primary image grabbed with
ffmpeg at about 10 % of the runtime.

The `tmdb` provider is off by default. When enabled with an API key, it
matches by IMDb ID if present, otherwise by title and year (German first,
English fallback). It stores overview, tagline, genres, people, rating,
poster and backdrop locally, so each movie is fetched once. Manual
corrections live in the database and are set with a CLI command.

All outbound HTTP goes through `internal/outbound`, which refuses requests
unless the provider is enabled and logs each one. A test runs a full scan
with providers disabled and fails on any outbound dial.

Images are served from the cache, resized on demand (`maxWidth`, `maxHeight`,
`fillWidth`, `quality`) with `golang.org/x/image`, and keyed by a tag that
changes when the source changes, so clients can cache them forever.

### 4.4 HTTP API layer

- Standard library `net/http`. Jellyfin routes are case-insensitive and
  clients mix casing, so a router normalises the path for matching while
  keeping the original IDs.
- Token sources, in order: `Authorization: MediaBrowser … Token="…"`,
  `X-Emby-Authorization`, `X-Emby-Token`, `X-MediaBrowser-Token`, query
  `ApiKey` / `api_key`.
- Both route families are served: the current `/Items?userId=` and the legacy
  `/Users/{userId}/Items`.
- DTOs are Go structs with PascalCase JSON names, checked in tests against
  the schemas of the Jellyfin 10.11 OpenAPI document.
- Unknown endpoints return 404 and are logged once per path, which gives the
  list of what a client still misses.

### 4.5 Playback

`POST /Items/{id}/PlaybackInfo` evaluates the client's device profile
properly: `DirectPlayProfiles` against container and codecs, `CodecProfiles`
conditions (including `RefFrames`, `BitDepth`, `VideoRangeType`) and
`ContainerProfiles`. In stage 1 the result is either direct play with a
`DirectStreamUrl`, or a clear refusal with the failed conditions logged. The
evaluator is built so stage 2 can add a transcoding result without changes
to the callers.

`GET|HEAD /Videos/{id}/stream[.{container}]` serves the file with
`http.ServeContent` (Range requests, `206`, correct `Content-Type`).
`MediaSourceId` selects the part of a multi-part movie;
`/Videos/{id}/AdditionalParts` lists the parts.

Embedded subtitles reach the client inside the container. Text subtitles
(SRT, ASS) are also served as separate streams via ffmpeg extraction for
clients that ask for external delivery; PGS is image-based and stays
embedded.

Progress reports (`/Sessions/Playing`, `/Progress`, `/Stopped`) update
`user_data`. As in Jellyfin, positions below 5 % are not stored and positions
above 90 % mark the movie played.

### 4.6 Deployment

- `GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go build`, copied to the jail.
- `deploy/freebsd/homeflix` rc.d script using `daemon(8)`, running as an
  unprivileged user, with `homeflix_enable`, `homeflix_config`,
  `homeflix_user` in `rc.conf`. Installing it needs root, so the operator
  does that step.
- Data under `/var/db/homeflix` in production, `~/.local/share/homeflix` in
  development.
- Port 8096 like Jellyfin. UDP 7359 discovery answers
  `who is JellyfinServer?` so clients find the server on the LAN.

## 5. API surface for stage 1

Derived from the endpoints Moonfin's Jellyfin client calls; admin, live TV,
music and series endpoints are left out.

| Area | Endpoints |
|---|---|
| System | `GET /System/Info/Public`, `GET /System/Info`, `GET\|POST /System/Ping`, `GET /Branding/Configuration` |
| Auth | `POST /Users/AuthenticateByName`, `POST /Sessions/Logout`, `GET /Users/Public`, `GET /QuickConnect/Enabled` (false) |
| Users | `GET /Users/Me`, `GET /Users/{id}`, `GET /Users/{id}/Images/Primary` (404), `POST /Users/Configuration` |
| Views | `GET /UserViews`, `GET /DisplayPreferences/{id}`, `POST /DisplayPreferences/{id}` |
| Items | `GET /Items` (ParentId, IncludeItemTypes, Recursive, SortBy, SortOrder, StartIndex, Limit, Fields, Filters, IsPlayed, IsFavorite, SearchTerm, NameStartsWith, Genres, Years, Ids, EnableTotalRecordCount), `GET /Items/{id}`, `GET /Items/Latest`, `GET /UserItems/Resume`, `GET /Items/Filters`, `GET /Items/{id}/Similar` (genre-based), `GET /Items/{id}/Ancestors`, `GET /Items/{id}/SpecialFeatures` (empty), `GET /Items/{id}/ThemeMedia` (empty), `GET /Genres`, `GET /Persons` |
| Legacy | `GET /Users/{uid}/Items`, `/Users/{uid}/Items/{id}`, `/Users/{uid}/Items/Latest`, `/Users/{uid}/Items/{id}/Intros` (empty), `/Users/{uid}/Items/{id}/LocalTrailers` (empty) |
| User data | `POST\|DELETE /UserPlayedItems/{id}`, `POST\|DELETE /UserFavoriteItems/{id}`, `POST /UserItems/{id}/UserData` |
| Images | `GET\|HEAD /Items/{id}/Images/{type}[/{index}]` for Primary, Backdrop, Thumb, Logo |
| Playback | `POST /Items/{id}/PlaybackInfo`, `GET\|HEAD /Videos/{id}/stream[.ext]`, `GET /Videos/{id}/AdditionalParts`, `GET /Videos/{id}/{msid}/Subtitles/{idx}/Stream.{fmt}`, `GET /Items/{id}/Download`, `GET /MediaSegments/{id}` (empty) |
| Sessions | `POST /Sessions/Playing`, `/Sessions/Playing/Progress`, `/Sessions/Playing/Stopped`, `POST /Sessions/Capabilities/Full`, `GET /Sessions`, WebSocket `/socket` (keep-alive only) |

## 6. Milestones

Each milestone ends with a check against Moonfin on the iMac, with the server
running in the jail.

| # | Content | Done when | Estimate |
|---|---|---|---|
| M0 | Repository, module, config, logging, migrations, `/System/Info/Public`, rc.d script, cross-build and deploy script | Moonfin adds the server and shows the login screen | 2–3 days |
| M1 | Users via CLI, login, token middleware, user endpoints, display preferences | Moonfin logs in and shows an empty home screen | 2–3 days |
| M2 | Scanner, filename parser, multi-part, probe pool, incremental rescan, stable IDs; codec survey of the library as a side product | all 1734 entries indexed; an unchanged rescan takes seconds | 1 week |
| M3 | Views, `/Items` query subset, item details with streams and chapters, latest, resume, search, local images | grid sorts A–Z, by year and by date added; details page lists audio and subtitle tracks | 1–1.5 weeks |
| M4 | PlaybackInfo with profile evaluation, ranged streaming, additional parts, progress reports, user data | Arrival and The Hunger Games play with DTS-HD and PGS, seek, and resume after a server restart; a multi-part movie plays both parts | 1 week |
| M5 | TMDB provider (opt-in), image cache, manual match command, no-outbound test | posters and plots for more than 95 % of movies; zero outbound dials with the provider off | 1 week |
| M6 | WebSocket keep-alive, UDP discovery, subtitle extraction, API coverage doc, README | Moonfin discovers the server by itself; 404 log is empty during a normal session | 3–5 days |

Total: about 5–6 weeks of full-time work, in line with the earlier estimate.

## 7. Testing

- Unit tests for the filename parser with every irregular name found in the
  library (the list is collected during M2).
- Probe mapping tests on small fixture files generated with ffmpeg in
  `testdata/` (h264 with high ref-frame count, HEVC HDR10, DTS, PGS, SRT,
  forced flags, chapters).
- Device-profile tests with the real profiles Moonfin 2.7 and 3.0 send,
  captured once and stored as JSON.
- DTO schema tests against the Jellyfin 10.11 OpenAPI document.
- HTTP tests through `httptest` for every endpoint in section 5.
- Manual acceptance per milestone with Moonfin on macOS.

## 8. Open decisions

1. **Try Silo first?** A time-boxed test (about one session) in the jail
   would show whether Silo builds and runs on FreeBSD with PostgreSQL,
   pgvector and Redis from pkg, and whether its outbound features can be
   switched off. If it works, it saves the stage-1 effort; if not, the plan
   stands unchanged.
2. **Metadata source.** Offline only (filename, ffprobe, frame-grab posters,
   no plots) or TMDB as opt-in (needs a free TMDB API key; sends IMDb IDs or
   title and year, once per movie).
3. **Repository hosting and licence.** Where the repository lives and under
   which licence (BSD-2-Clause would fit FreeBSD).
4. **Watch history import.** Whether played states should be imported from
   the old Plex database, if it is still available.

## 9. Lessons from remux to keep

- Report `RefFrames` and the other profile-relevant stream fields.
- Keep subtitle streams in item details, not only in PlaybackInfo.
- Honour `SortName` on every view.
- Never purge to fix the library; rescans must keep IDs and user data.
- No default outbound endpoints; external sources are opt-in and logged.
- Report stream dispositions (default, forced) exactly as the file has them;
  the client picks the audio track, the server does not override it.
