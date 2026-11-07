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
| `GET /UserViews`, `GET /Users/{userId}/Views` | yes | one view per library, and a view "Collections" ("Sammlungen" with `language de`) of type `boxsets` holding all collections, as Jellyfin's collections folder |
| `GET\|POST /DisplayPreferences/{id}` | yes | stored per user, client and ID |

## Items

| Endpoint | Auth | Notes |
|---|---|---|
| `GET /Items`, `GET /Users/{userId}/Items` | yes | `ParentId`, `IncludeItemTypes`, `ExcludeItemTypes`, `Recursive`, `Ids`, `SortBy` (name, year, premiere date, rating, age rating, date added, runtime, date played, play count, favourite, played, random), `SortOrder`, `StartIndex`, `Limit`, `Fields`, `Filters` (IsPlayed, IsUnplayed, IsFavorite, IsResumable), `IsPlayed`, `IsFavorite`, `SearchTerm`, `NameStartsWith`, `NameStartsWithOrGreater`, `NameLessThan`, `Genres`, `GenreIds`, `PersonIds`, `Studios`, `StudioIds`, `Years`, `HasSubtitles`, `IsHd`, `AudioLanguages`, `SubtitleLanguages`, `HasTrailer`, `HasSpecialFeature`, `AnyProviderIdEquals`, `CollapseBoxSetItems` |
| `GET /Items/{itemId}`, `GET /Users/{userId}/Items/{itemId}` | yes | movies, versions, parts and extras with media sources, streams and chapters; `People` carry `PrimaryImageTag` for their portraits |
| `GET /Items/Latest`, `GET /Users/{userId}/Items/Latest` | yes | by date added |
| `GET /UserItems/Resume`, `GET /Users/{userId}/Items/Resume` | yes | started, not finished |
| `GET /Items/Filters`, `GET /Items/Filters2` | yes | genres and years of a view |
| `GET /Items/Counts` | yes | movie count |
| `GET /Items/{itemId}/Ancestors` | yes | parent movie of an extra, and the library |
| `GET /Items?IncludeItemTypes=BoxSet` | yes | collections: film series that two or more movies belong to, from TMDB; filters match their movies |
| `GET /Items?IncludeItemTypes=Movie,BoxSet&CollapseBoxSetItems=true` | yes | movies with collections in place of their movies, unless movie filters (genre, year, play state, …) are set, as in Jellyfin |
| `GET /Items?ParentId={collectionsViewId}` | yes | all collections |
| `GET /Items?ParentId={collectionId}` | yes | the movies of a collection, by premiere date unless `SortBy` says otherwise |
| `GET /Items/{collectionId}` | yes | a collection as `BoxSet` with overview, child count and `UserData.UnplayedItemCount` |
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
| `GET /Items/{itemId}/Images/{type}[/{index}]` | no | Primary, Backdrop and Thumb, index 0 only; for people the portrait as Primary; for collections their TMDB poster and backdrop, else those of their first movie; `maxWidth`, `maxHeight`, `width`, `height`, `fillWidth`, `fillHeight`, `quality`; immutable caching when `tag` matches |

Images need no token, as in Jellyfin: clients load them through image
caches that send no credentials.

## Playback

| Endpoint | Auth | Notes |
|---|---|---|
| `GET\|POST /Items/{itemId}/PlaybackInfo` | yes | evaluates the device profile like Jellyfin's StreamBuilder; for music and audiobooks it offers the AAC conversion (see below) |
| `GET /Videos/{itemId}/stream[.{container}]` | yes | the original file with range requests; remux and transcode parameters are ignored |
| `GET /Items/{itemId}/Download` | yes | the original file as an attachment, for users with the download right (403 otherwise) |
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

## Music and audiobooks

Music libraries have `CollectionType` `music`, audiobook libraries `books`.
Tracks are `Audio` items, children (`ParentId`) of their `MusicAlbum`;
artists are `MusicArtist` items; books are `AudioBook` items with
`Chapters` over the whole book and their authors as `People` of type
`Author` (narrators as `Narrator`). The item listings below take the music
parameters `IncludeItemTypes` (`Audio`, `MusicAlbum`, `MusicArtist`,
`AudioBook`, `Book`, `Playlist`), `ArtistIds`, `AlbumArtistIds`,
`ContributingArtistIds`, `GenreIds`, `Genres`, `Years`, `MinPremiereDate`,
`MaxPremiereDate`, the play state filters and the sorts `SortName`,
`Album`, `AlbumArtist`, `Artist`, `ParentIndexNumber`, `IndexNumber`,
`ProductionYear`, `PremiereDate`, `DateCreated`, `DatePlayed`,
`PlayCount`, `Runtime` and `Random`.

| Endpoint | Auth | Notes |
|---|---|---|
| `GET /Items`, `/Users/{userId}/Items` | yes | albums, tracks, artists, books and playlists; an album as `ParentId` lists its tracks by disc and number; a library as `ParentId` without `Recursive` lists nothing (its children would be folders) |
| `GET /Items/Latest` | yes | for a music library its newest albums, for an audiobook library its newest books |
| `GET /UserItems/Resume` | yes | also books and long tracks; `MediaTypes` and `IncludeItemTypes` filter |
| `GET /Artists`, `/Artists/AlbumArtists` | yes | artists of a library, or those that are album artists; `IsFavorite`, `SearchTerm`, `NameStartsWith`, paging |
| `GET /Artists/{name}` | yes | an artist by name |
| `GET /Genres`, `/MusicGenres`, `/Genres/{name}`, `/MusicGenres/{name}` | yes | genres of albums or books with `AlbumCount` when asked about a music or audiobook library or types |
| `GET /Items/{itemId}/Similar` | yes | artists sharing genres; albums sharing genres or artists |
| `GET /Items/{itemId}/InstantMix`, `/Albums/…`, `/Songs/…`, `/Artists/…`, `/Artists/InstantMix?id=`, `/MusicGenres/…`, `/Playlists/…/InstantMix` | yes | tracks of the same album artists and genres, shuffled |
| `GET /Audio/{itemId}/Lyrics` | yes | an `.lrc` file next to the track (with times), else the lyrics tag; 404 without lyrics |
| `GET /Audio/{itemId}/stream[.{container}]` | yes | the original file with range requests |
| `GET /Audio/{itemId}/universal` | yes | the file when its container is in `Container`, else a redirect to the AAC conversion |
| `GET /Audio/{itemId}/main.m3u8`, `/master.m3u8`, `/Audio/{itemId}/hls/{n}.ts` | yes (also `ApiKey`) | AAC conversion as HLS with 6-second MPEG-TS segments; a book of several files is one stream |
| `POST /Playlists` | yes | `Name`, `Ids` (body or query); returns `Id` |
| `GET /Playlists/{id}` | yes | `ItemIds`; playlists are private (`OpenAccess` false, no `Shares`) |
| `POST /Playlists/{id}` | yes | renames |
| `GET /Playlists/{id}/Items` | yes | the entries with `PlaylistItemId` |
| `POST /Playlists/{id}/Items?Ids=`, `DELETE …?EntryIds=` | yes | adds and removes entries |
| `POST /Playlists/{id}/Items/{entry}/Move/{index}` | yes | moves an entry |
| `DELETE /Items/{itemId}` | yes | deletes a playlist of the user; library items are never deleted |
| `GET\|POST /Moonfin/Bookmarks/{itemId}`, `POST …/Notes` | yes | Moonfin's audiobook bookmarks and notes, stored as the app sends them |

For tracks and books, `PlaybackInfo` evaluates the audio profiles of the
device profile; a file outside it gets `SupportsTranscoding` with a
`TranscodingUrl` to the AAC conversion (`TranscodingContainer` `ts`,
`TranscodingSubProtocol` `hls`) when the user's conversion level is
*audio* or *all*. The first audio stream carries the ReplayGain track gain
as `NormalizationGain`. Play state follows Jellyfin: a track shorter than
five minutes counts as played after 5 % of it; a book keeps no position in
its first five minutes and counts as played in its last five, also from
progress reports alone, since Moonfin's audiobook player sends no stop.

## Sessions and playback reports

| Endpoint | Auth | Notes |
|---|---|---|
| `POST /Sessions/Capabilities`, `POST /Sessions/Capabilities/Full` | yes | accepted, not stored |
| `GET /Sessions` | yes | the caller's own session |
| `POST /Sessions/Playing`, `…/Progress`, `…/Stopped`, `…/Ping` | yes | resume position and played state, with Jellyfin's thresholds (5 %, 90 %, 5 minutes) |
| `POST\|DELETE /UserPlayedItems/{itemId}`, `/Users/{userId}/PlayedItems/{itemId}` | yes | for a collection: all its movies |
| `POST\|DELETE /UserFavoriteItems/{itemId}`, `/Users/{userId}/FavoriteItems/{itemId}` | yes | also for collections |
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
- Series, e-books, photos and live TV.
- Shared playlists (`Shares`, `OpenAccess`) and smart playlists.
- Collections made or edited by hand (`/Collections`); collections come
  from TMDB only.
- Administration through the API: users, libraries, scheduled tasks,
  plugins, server configuration and the startup wizard.
- Remote control, SyncPlay and session messages over the WebSocket.
- Trickplay images, chapter images and user avatars.
