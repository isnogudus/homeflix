# Jellyfin API coverage

homeflix implements the part of the Jellyfin 10.11 server API that movie
clients use, derived from the requests Moonfin sends. This page lists every
endpoint homeflix answers and how far each one goes. Anything not listed
returns 404 and is logged once per path as `unsupported endpoint`, without
its query string, so gaps show up in the log of a normal session.

Routes match as in Jellyfin: case-insensitively, with or without a trailing
slash, and with an optional `/emby` or `/mediabrowser` prefix. GET routes
also answer HEAD. "Auth" means the request needs an access token, sent in
the `Authorization: MediaBrowser … Token="…"` header, in `X-Emby-Token`, or
as the `ApiKey` or `api_key` query parameter.

## System

| Endpoint | Auth | Notes |
|---|---|---|
| `GET /System/Info/Public` | no | server name, ID, version 10.11.8, `ProductName` "Jellyfin Server" |
| `GET /System/Info` | yes | public info plus OS and architecture |
| `GET\|POST /System/Ping` | no | |
| `GET /System/Configuration/encoding` | yes | reports no transcoding and no hardware acceleration; other sections 404 |
| `GET /Branding/Configuration` | no | empty |
| `GET /Plugins` | yes | empty list |
| `GET /Moonfin/…`, `GET /Plugins/AchievementBadges/public-config` | no | 404 without a log entry: Moonfin probes for server plugins homeflix does not have |

## Sign-in and users

| Endpoint | Auth | Notes |
|---|---|---|
| `POST /Users/AuthenticateByName` | no | name and password; failed attempts are delayed |
| `POST /Sessions/Logout` | yes | revokes the token |
| `GET /Users/Public` | no | always empty, so login screens ask for the name |
| `GET /QuickConnect/Enabled` | no | `false` |
| `POST /QuickConnect/Initiate`, `GET /QuickConnect/Connect`, `POST /QuickConnect/Authorize`, `POST /Users/AuthenticateWithQuickConnect` | no | 401 "Quick connect is disabled", as Jellyfin answers with QuickConnect off |
| `GET /Users`, `GET /Users/Me`, `GET /Users/{userId}` | yes | administrators see everyone, others only themselves |
| `POST /Users/Configuration`, `POST /Users/{userId}/Configuration` | yes | client preferences such as audio and subtitle language |
| `GET /Users/{userId}/Images/{type}` | no | 404: there are no avatars |

Users are created, changed and deleted with `homeflix user` only.

## Views and display preferences

| Endpoint | Auth | Notes |
|---|---|---|
| `GET /UserViews`, `GET /Users/{userId}/Views` | yes | one view per library |
| `GET\|POST /DisplayPreferences/{id}` | yes | stored per user, client and ID |

## Items

| Endpoint | Auth | Notes |
|---|---|---|
| `GET /Items`, `GET /Users/{userId}/Items` | yes | `ParentId`, `IncludeItemTypes`, `ExcludeItemTypes`, `Recursive`, `Ids`, `SortBy` (name, year, premiere date, rating, age rating, date added, runtime, date played, play count, favourite, played, random), `SortOrder`, `StartIndex`, `Limit`, `Fields`, `Filters` (IsPlayed, IsUnplayed, IsFavorite, IsResumable), `IsPlayed`, `IsFavorite`, `SearchTerm`, `NameStartsWith`, `NameStartsWithOrGreater`, `NameLessThan`, `Genres`, `GenreIds`, `PersonIds`, `Studios`, `StudioIds`, `Years`, `HasSubtitles`, `IsHd`, `AudioLanguages`, `SubtitleLanguages`, `HasTrailer`, `HasSpecialFeature`, `AnyProviderIdEquals` |
| `GET /Items/{itemId}`, `GET /Users/{userId}/Items/{itemId}` | yes | movies, versions, parts and extras with media sources, streams and chapters; `People` carry `PrimaryImageTag` for their portraits |
| `GET /Items/Latest`, `GET /Users/{userId}/Items/Latest` | yes | by date added |
| `GET /UserItems/Resume`, `GET /Users/{userId}/Items/Resume` | yes | started, not finished |
| `GET /Items/Filters`, `GET /Items/Filters2` | yes | genres and years of a view |
| `GET /Items/Counts` | yes | movie count |
| `GET /Items/{itemId}/Ancestors` | yes | parent movie of an extra, and the library |
| `GET /Items/{itemId}/Similar` | yes | most shared genres, then closest year |
| `GET /Items/{itemId}/SpecialFeatures`, `GET /Users/{userId}/Items/{itemId}/SpecialFeatures` | yes | extras from the movie's folder |
| `GET /Items/{itemId}/LocalTrailers`, `GET /Users/{userId}/Items/{itemId}/LocalTrailers` | yes | trailers from the movie's folder |
| `GET /Items/{itemId}/Intros`, `GET /Users/{userId}/Items/{itemId}/Intros` | yes | empty |
| `GET /Items/{itemId}/ThemeMedia` | yes | empty |
| `GET /MediaSegments/{itemId}` | yes | empty |
| `GET /Shows/NextUp` | yes | empty: homeflix has no series |
| `GET /LiveTv/Channels` | yes | empty: homeflix has no live TV |
| `GET /Genres`, `GET /Persons`, `GET /Studios` | yes | from TMDB metadata; people with portrait tag, and their biography with `Fields=Overview` |
| `GET /Items/{personId}` | yes | a person: biography (`Overview`), birth (`PremiereDate`, `ProductionYear`), death (`EndDate`), place of birth (`ProductionLocations`), TMDB and IMDb IDs |

## Images

| Endpoint | Auth | Notes |
|---|---|---|
| `GET /Items/{itemId}/Images/{type}[/{index}]` | no | Primary, Backdrop and Thumb, index 0 only; for people the portrait as Primary; `maxWidth`, `maxHeight`, `width`, `height`, `fillWidth`, `fillHeight`, `quality`; immutable caching when `tag` matches |

Images need no token, as in Jellyfin: clients load them through image
caches that send no credentials.

## Playback

| Endpoint | Auth | Notes |
|---|---|---|
| `GET\|POST /Items/{itemId}/PlaybackInfo` | yes | evaluates the device profile like Jellyfin's StreamBuilder; direct play only, no transcoding |
| `GET /Videos/{itemId}/stream[.{container}]` | yes | the original file with range requests; remux and transcode parameters are ignored |
| `GET /Items/{itemId}/Download` | yes | the original file as an attachment |
| `GET /Videos/{itemId}/AdditionalParts` | yes | later parts of a multi-part movie |
| `GET /Videos/{itemId}/{mediaSourceId}/Subtitles/{index}[/{startTicks}]/Stream.{format}` | yes | embedded text subtitles such as SubRip, ASS or mov_text, as `srt`, `vtt`, `ass` or `ssa`; see below |

Subtitle tracks are reported with `SupportsExternalStream: false`, so
clients render them from the container, which costs nothing. The subtitle
endpoint serves clients that ask for a track as a file anyway: ffmpeg reads
the whole video to extract it, which takes up to a few minutes for a large
file. The result is cached in `subtitles/` in the data directory and
removed after a scan once the video is gone or has changed. Image-based
subtitles (PGS, DVD) are not converted. `startTicks` is ignored, since
homeflix only serves the original file, which starts at zero.

## Sessions and playback reports

| Endpoint | Auth | Notes |
|---|---|---|
| `POST /Sessions/Capabilities`, `POST /Sessions/Capabilities/Full` | yes | accepted, not stored |
| `GET /Sessions` | yes | the caller's own session |
| `POST /Sessions/Playing`, `…/Progress`, `…/Stopped`, `…/Ping` | yes | resume position and played state, with Jellyfin's thresholds (5 %, 90 %, 5 minutes) |
| `POST\|DELETE /UserPlayedItems/{itemId}`, `/Users/{userId}/PlayedItems/{itemId}` | yes | |
| `POST\|DELETE /UserFavoriteItems/{itemId}`, `/Users/{userId}/FavoriteItems/{itemId}` | yes | |
| `GET\|POST /UserItems/{itemId}/UserData`, `/Users/{userId}/Items/{itemId}/UserData` | yes | |
| `POST\|DELETE /UserItems/{itemId}/Rating`, `/Users/{userId}/Items/{itemId}/Rating` | yes | likes are not kept; returns the current user data |
| `GET /socket`, `GET /embywebsocket` | yes | WebSocket; see below |

The WebSocket carries keep-alives only. After connecting, the client gets
`ForceKeepAlive` with an interval of 60 seconds and is answered on every
`KeepAlive`; a connection that stays silent for two intervals is closed.
Remote control and library change notifications are not sent.

## Local network discovery

Not HTTP, but part of what Jellyfin clients expect: homeflix answers
`who is JellyfinServer?` (and the older `who is EmbyServer?`) on UDP port
7359 with its address, ID and name, so clients list it without the address
being typed in. Only queries from private, loopback and link-local
addresses are answered, and nothing is sent unasked. `no discovery` in
homeflix.conf(5) turns it off.

## Not implemented

- Transcoding and remuxing: HLS playlists (`/Videos/{id}/master.m3u8`),
  `/Videos/{id}/hls1/…`, audio and subtitle burn-in.
- Series, music, books, photos, live TV and collections.
- Administration through the API: users, libraries, scheduled tasks,
  plugins, server configuration and the startup wizard.
- Remote control, SyncPlay and session messages over the WebSocket.
- Trickplay images, lyrics, chapter images and user avatars.
