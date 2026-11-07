# homeflix

A media server that speaks the Jellyfin client API, written in Go, built to
run natively on FreeBSD. Existing Jellyfin clients (primarily Moonfin) are
used unchanged; homeflix has no frontend of its own.

Principles:

- Direct play first. The server hands out the original file; transcoding is
  added only where a client cannot play a file.
- Nothing leaves the host unless the operator enables it. External metadata
  sources are opt-in.
- One static binary, one SQLite database. No Docker, PostgreSQL or Redis.

Status: milestone M6 of [the stage 1 plan](docs/plan-stage1.md). Clients
find the server in the local network, sign in, browse, search and play the
movies directly, including alternate versions and movies split into parts;
watch state, resume positions and favourites are kept per user. With a TMDB
token, movies get titles, overviews, genres, cast, ratings and artwork.
[docs/api-coverage.md](docs/api-coverage.md) lists the Jellyfin endpoints
homeflix answers.

Playback is direct play only: the client receives the original file and
decodes it. PlaybackInfo evaluates the client's device profile as Jellyfin
does and reports when a file is outside it, but there is no transcoding
yet, so such files play only if the client manages them anyway.

## Build

Requires Go 1.26 or newer.

```sh
make check            # vet, gofmt check, tests with the race detector
make build            # binary for this machine in build/
make build-freebsd    # build/homeflix-freebsd-amd64, no cgo needed
```

## Run

```sh
cp deploy/homeflix.conf.sample homeflix.conf   # edit as needed
build/homeflix serve -n -config homeflix.conf  # check it
build/homeflix serve -config homeflix.conf
```

On FreeBSD, install the binary as `/usr/local/bin/homeflix`, the
configuration as `/usr/local/etc/homeflix.conf`, `homeflix.conf.5` as
`/usr/local/share/man/man5/homeflix.conf.5`, and `deploy/freebsd/homeflix`
as `/usr/local/etc/rc.d/homeflix`, then:

```sh
pw useradd homeflix -d /nonexistent -s /usr/sbin/nologin
sysrc homeflix_enable=YES
service homeflix configtest
service homeflix start
```

The account needs read access to the media directories.

## Configuration

`homeflix.conf` is written in the style of OpenBSD's `httpd.conf` and
`pf.conf`, read with [obsdconf](https://github.com/isnogudus/obsdconf):
keywords instead of punctuation, blocks and lists in braces, macros and
`include`. [homeflix.conf(5)](homeflix.conf.5) describes every statement.

```
server name "Kino"
listen on * port 8096
language de

library "Filme" {
	path "/media/movies/recent"
	path "/media/movies/archive"
}
```

`homeflix serve -n` checks the file and reports every error at once, with
file name and line.

Clients find the server by themselves: homeflix answers the discovery
broadcasts Jellyfin clients send on UDP port 7359, from private network
addresses only. `no discovery` turns that off; a server listening on a
loopback address does not answer anyway.

## Libraries

Each `library` block in the configuration names one or more directories.
A movie is

- a single file: `Heat (1995) [imdbid-tt0113277].mkv`,
- part files next to each other: `Intimacy (2001) cd1.avi`, `… cd2.avi`,
- or a folder `Title (Year)` holding one or more versions (`… [HD].mp4`,
  `… [SD] part1.mp4`, `… [SD] part2.mp4`) and extras folders such as
  `behind the scenes`, `featurettes` or `trailers`.

The year and an `[imdbid-tt…]` or `{imdb-tt…}` tag are read from the name.
Scans run at startup, every `scan interval` and on `POST /Library/Refresh`;
only new or changed files are probed with ffprobe. Renamed and moved files
keep their item (matched by size and modification time, or by IMDb ID); a
movie that disappears is kept for 30 days, and a library directory that
cannot be read leaves its movies untouched.

Artwork comes from the videos themselves, so nothing is fetched from the
internet: ffmpeg takes three frames of each movie, black bars are cut off,
HDR10 frames are tone-mapped to SDR, and the frame with the most contrast
becomes the still that posters (cut to 2:3) and backdrops are made from.
Stills are made in the background after each scan and on first request.

```sh
homeflix scan -config /usr/local/etc/homeflix.conf            # scan once
homeflix scan -reprobe -config /usr/local/etc/homeflix.conf   # probe everything again
homeflix scan -reindex -config /usr/local/etc/homeflix.conf   # find all keyframes again
homeflix stats -config /usr/local/etc/homeflix.conf           # containers, codecs, HDR, failures
homeflix keyframes /media/movies/Film.mkv                     # keyframes as the server finds them
```

## Metadata from TMDB

Without further configuration homeflix stays offline: titles come from the
file names and artwork from stills of the videos. A `tmdb` block turns on
lookups at [The Movie Database](https://www.themoviedb.org):

```
tmdb { token file "/usr/local/etc/homeflix-tmdb.token" }
```

The file holds the API Read Access Token (v4) of a free TMDB account and
must not be readable by others. homeflix then sends TMDB, once per movie,
the IMDb ID from the file name or the title and year, fetches the details
and two images and keeps them in the data directory. Then it fetches, once
per person, biography, dates and portrait of the directors, writers and
first 20 actors, and once per film series that two or more movies belong
to, its name, overview, poster and backdrop, asking by TMDB's own IDs.
Nothing else leaves the host. The first run over a large library takes a
while: 1,700 movies list about 22,000 people, which takes some 70 minutes
at ten requests a second and about 1 GB for the portraits. After the
`refresh interval` (90 days by default) the details are fetched again for
the same TMDB entries, spread over a month, so that ratings, biographies
and artwork stay current; a refresh that fails keeps what was there.

All network access goes through `internal/outbound`, which only reaches the
hosts it was created for; a test fails if any other code opens connections.

Film series show up in clients as collections (Jellyfin box sets): in a
view of their own next to the libraries, in a row of the home screen, and
in place of their movies when a client groups the library into
collections. Collections are built from TMDB data only; they cannot be
edited.

```sh
homeflix match list -config /usr/local/etc/homeflix.conf                 # movies without metadata
homeflix match set "Heat (1995)" 949 -config /usr/local/etc/homeflix.conf # correct a match by hand
homeflix match clear "Heat (1995)" -config /usr/local/etc/homeflix.conf   # look it up again
```

This product uses the TMDB API but is not endorsed or certified by TMDB.

## Web pages and remote access

homeflix has web pages of its own: the web client under `/web/` (the
server address alone leads there) and the admin UI under `/admin/`, both
being extended in [stage 2](docs/plan-stage2.md).

The web client works on phones and laptops: a home screen with "continue
watching", recently added, favourites and collections; the movie grid with
sorting, letters, genres, watched and favourite filters and optional
grouping into collections; search over movies, people and collections;
details with cast and crew, versions, audio and subtitle tracks and similar
movies; collection and person pages; and an account page to change the
password, add and remove passkeys, connect apps with Quick Connect and
sign out devices. The player plays a movie's parts one after
another and keeps the position in sync with Jellyfin clients, and offers
the audio tracks of a file. What browsers decode themselves (h264 with AAC
or MP3 in MP4) plays as it is. Other h264 files, such as MKV with AC3,
DTS or TrueHD, and other audio tracks, are remuxed on the fly: the video
is copied, the audio converted to AAC, and the player gets HLS, cut at the
keyframes of the file. They are found once per file, in the background
after a scan or on first play, and kept in the database until the file
changes: from the container index (Matroska Cues, the MP4 sync sample
table) when it covers the file, else by reading all packets with ffprobe,
one file at a time after the others are done, which takes seconds for small
files and a few minutes for large ones; the admin overview shows how many
files are done. ffmpeg runs only while someone
watches, pauses when it is a minute ahead, and its segments in
`transcode/` in the data directory are removed as the player moves on.
Other video (VC-1, MPEG-2, MPEG-4 ASP, HEVC) is transcoded to h264 with
libx264: scaled down to fit 1080p, deinterlaced if needed, and HDR (HDR10,
HLG, Dolby Vision with an HDR10 base layer) mapped to SDR by ffmpeg's
scaler, which needs ffmpeg 8 or later. Text subtitles are tracks of the
player; image subtitles (PGS, DVD) cannot be shown by browsers, so the
player offers them burnt into a transcode, which then also applies to
files that would otherwise play as they are. Where the video is
transcoded anyway, the image subtitle marked as default (often the forced
one for foreign-language scenes) is burnt in unless the viewer turns it
off. A transcode of 4K HDR takes most of a 6-core processor; `video
workers` in the configuration limits how many run at once. A player that
switches audio or subtitles ends its previous conversion at once, one
that has asked for nothing for 30 seconds gives its ffmpeg up to a new
player, and when none is free the player says the server is busy and
tries again.

The admin UI shows the state of the server and does what the `user` and
`match` commands do:

- overview: library, metadata, requests to TMDB, last scan, and how
  homeflix sees your request (address, proxy);
- users: create, set passwords, grant or remove administrator rights and
  the right to download, choose how far movies are converted for them,
  delete, and sign out single devices;
- library: start a scan, stream survey, probe failures;
- metadata: movies without metadata, search TMDB and match by hand, or
  forget a match;
- settings, read from `homeflix.conf`.

For access from outside, put homeflix behind a reverse proxy that does
TLS, such as Caddy or `tailscale serve`, and name the proxy in
`trusted proxy`, so that homeflix sees the client's own address. The admin
UI and administrative API requests answer only from the networks in
`admin from` (by default the home network and the tailnet), also when a
proxy forwards everything. After 10 failed logins from one address, or 20
for one user, further logins are refused for 15 minutes, in the web pages
and the Jellyfin API alike.

Passkeys sign in to the web client without a password. They are offered on
the HTTPS addresses listed in the `passkeys` block; each address is its own
relying party, so a passkey added through the tailnet name does not work
through the Caddy domain, and the account page says which address a passkey
belongs to. Jellyfin apps cannot use passkeys; they sign in with name and
password or with Quick Connect: the app shows a code, and a user signed in
to the web client confirms it on the account page.

## Users

Users are managed on the command line or in the admin UI; there is no
sign-up through the API.
Passwords are read without echo (or as one line from standard input).

```sh
homeflix user add anna -admin -config /usr/local/etc/homeflix.conf
homeflix user passwd anna       # also signs anna out on every device
homeflix user list              # users with their signed-in devices
homeflix user admin anna        # administrator rights; -remove takes them back
homeflix user download anna     # may download original files; -remove takes it back
homeflix user conversion anna audio   # none, audio or all
homeflix user delete anna
```

As in Jellyfin, an administrator decides per user how far the server may
convert movies for them: `none` plays only files the player takes as they
are, `audio` also lets the web client remux (video copied, audio
converted), and `all`, the default, allows video transcoding and burnt-in
image subtitles as well. Jellyfin apps see the level in the user policy
(`EnablePlaybackRemuxing`, `EnableAudioPlaybackTranscoding`,
`EnableVideoPlaybackTranscoding`).

Downloading the original files needs a right of its own: users who have
it find download links on the movie pages of the web client, and Jellyfin
apps may download (`EnableContentDownloading`, `CanDownload`); for others
download requests are refused. Administrators who existed before the right
was introduced keep it; new users get it only when it is granted. Direct
play still sends a player the whole file, so the right governs saving
files, not what a direct-playing client receives.

`/Users/Public` always returns an empty list, so login screens ask for name
and password instead of showing who has an account.

## Backup

The data directory holds:

| Path | Content | Back up? |
|---|---|---|
| `homeflix.db` | users, sessions, watch state, library, metadata, manual matches | yes, with `homeflix backup` |
| `metadata/` | artwork and portraits from TMDB | yes; fetching it again needs TMDB |
| `images/` | stills and scaled images made from the videos | no, it is rebuilt on demand |
| `subtitles/` | text subtitles extracted for clients that ask for them as files | no |

The database must not be copied as a file while the server runs; use

```sh
homeflix backup /backup/homeflix.db -config /usr/local/etc/homeflix.conf
```

which writes a consistent copy (SQLite `VACUUM INTO`) readable by its owner
only, since it holds password hashes. Artwork, portraits and subtitles no
movie uses any more are removed after each scan, once they are an hour old.

## Development host

`scripts/deploy-dev.sh host` (or `HOMEFLIX_DEV_HOST=host`) cross-compiles,
copies the binary to `~/homeflix` on the host over SSH and restarts it as
the login user, without root. On the first deploy it also copies
`deploy/dev.conf` there as `homeflix.conf`; adjust the library paths in it,
later deploys leave the host's copy alone.

`scripts/smoke-test.sh <binary>` starts a throwaway instance on 127.0.0.1
with a generated test user and a generated two-second movie, runs the client
flow with curl, including subtitle extraction and the WebSocket handshake,
and removes everything afterwards:

```sh
ssh devhost 'sh -s -- homeflix/bin/homeflix' < scripts/smoke-test.sh
```

## License

MIT, see [LICENSE](LICENSE). The web client includes hls.js under the
Apache License 2.0; see [NOTICE](NOTICE).
