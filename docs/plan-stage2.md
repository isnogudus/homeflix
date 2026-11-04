# homeflix — Stage 2 plan: web client, admin UI, transcoding

Stage 1 made homeflix a Jellyfin-compatible server for direct play with
Moonfin. Stage 2 adds what people outside the house need: a web client of
homeflix's own, an admin UI, sign-in with passkeys, and server-side remuxing
and transcoding for files a browser cannot play.

## 1. Goals and non-goals

Goals:

- A small web client of homeflix's own: home screen, library grid, search,
  details, collections and people, and a player with audio and subtitle
  choice, seeking and resume. Watch state is the same as in Moonfin.
- An admin UI for everything that is command-line only today: users and
  their devices, scans, library and metadata status, and correcting matches
  with a TMDB search.
- Sign-in with passkeys in the web client and the admin UI; password as
  fallback. Moonfin apps keep using passwords, or sign in with a code
  confirmed in the web client (QuickConnect).
- Every movie of the library plays in current browsers (Safari, Chrome,
  Firefox, Edge), added in steps: remux with audio conversion first, video
  transcoding after.
- Safe to reach from outside through Caddy and over the tailnet; the admin
  UI only from the house and the tailnet.

Non-goals for stage 2: TLS in homeflix itself (Caddy and the tailnet provide
it), a TV-remote interface (Moonfin covers TVs), series, music, live TV,
offline downloads, editing collections, an adaptive bitrate ladder (see
2.3), hardware encoding.

## 2. Facts this plan rests on

### 2.1 Access

- Two ways in, each with a domain of its own:
  - over the tailnet with `tailscale serve`, which gives homeflix an HTTPS
    name under `ts.net` and forwards to it; clients have tailnet addresses
    from 100.64.0.0/10;
  - through Caddy under a domain of its own (not chosen yet), with TLS from
    Caddy, forwarding to homeflix in the LAN.

  homeflix keeps speaking plain HTTP behind both.
- Behind either proxy every request comes from the proxy's address; the
  client address, scheme and host are in `X-Forwarded-For`,
  `X-Forwarded-Proto` and `X-Forwarded-Host`. Caddy sets all three and
  appends to `X-Forwarded-For`; `tailscale serve` sets all three too and
  replaces `X-Forwarded-For` with the tailnet address it accepted the
  connection from (`ipn/ipnlocal/serve.go`). Requests that come in through
  Tailscale Funnel take the same path but carry a public address. homeflix must
  believe these headers only from the two proxies, or anyone could forge
  their address. With the forwarded address, a tailnet client is told apart
  from one coming in through Caddy.
- Few remote viewers; their devices are not known, so the web client has to
  work on phones and laptops alike.

### 2.2 What browsers can play

The default version of each movie, by what a browser needs (2026-11-04):

| Share | Movies | Content | Needs |
|---|---|---|---|
| 58 % | 1012 | h264 with AAC or MP3 in MP4, VP9 in WebM | nothing: direct play |
| 33 % | 573 | h264 with AC3, E-AC3, DTS or TrueHD, mostly MKV | audio to AAC, video copied (remux to HLS) |
| 1 % | 21 | HEVC (HDR10, Dolby Vision) | Safari plays it; elsewhere video transcoding and HDR to SDR |
| 7 % | 126 | VC-1, MPEG-2, MPEG-4 ASP, msmpeg4, MPEG-1 | video transcoding |

So the remux step alone brings 91 % of the library to every browser. Image
subtitles (about 250 movies with PGS, about 20 with DVD subtitles) cannot
be shown by browsers and have to be burnt into the video, which needs
transcoding.
Text subtitles are extracted as WebVTT already (stage 1, M6).

### 2.3 Capacity

- Upload 500 Mbit/s. Even a 4K remux at 80 Mbit/s fits several times, so
  stage 2 serves original quality and transcodes for compatibility, not to
  save bandwidth. A per-user bitrate cap can follow if needed.
- The jail has 12 cores and no usable hardware encoder. Software h264
  encoding (libx264) of 1080p in real time takes a few cores per stream;
  the real number is measured in N5 and bounded by `transcode workers`.

### 2.4 Passkeys

- WebAuthn works only in a secure context: HTTPS, or `http://localhost`.
  Both ways in are HTTPS (Caddy, `tailscale serve`).
- A passkey is bound to a relying-party ID, a domain, and works on every
  HTTPS origin under it and nowhere else. The Caddy domain and the `ts.net`
  name have no common parent, so one passkey cannot serve both. Each origin
  is its own relying party; a user who uses both ways registers a passkey on
  each, and the account page shows which passkey belongs to which address.
- Related Origin Requests (a `/.well-known/webauthn` file on one domain that
  admits the other) would let one passkey serve both, but not every browser
  supports them yet; they can be added later without changing stored
  passkeys of the main domain.
- Jellyfin clients cannot use passkeys: Moonfin signs in with name and
  password (`/Users/AuthenticateByName`) or with QuickConnect, a code shown
  in the app and confirmed by a signed-in user. Moonfin already offers
  QuickConnect on its login screen (stage 1 answers that it is off).
- Library: `github.com/go-webauthn/webauthn` (BSD-3-Clause, maintained);
  WebAuthn verification is not written by hand.

## 3. Architecture

### 3.1 Requests, proxies and addresses

- New statement `trusted proxy` lists the addresses whose `X-Forwarded-*`
  headers count: Caddy, and the address `tailscale serve` forwards from
  (loopback when tailscaled runs on the same host). From everyone else they
  are ignored.
- The client address found this way is used for logging, the login lockout
  and the admin network check.
- `admin from` lists the networks the admin UI and the admin parts of the
  API answer from; the default is loopback, the private networks and the
  tailnet range. A request through Caddy counts as coming from the client
  behind it, so the admin UI is not reachable from the internet even if
  Caddy forwards everything.

### 3.2 Web sessions

- The web client and the admin UI live under `/web/` and `/admin/` and use a
  session cookie (`HttpOnly`, `SameSite=Lax`, `Secure` when the request came
  over HTTPS). The session is a row of the existing token table, so it shows
  up and can be ended like a Moonfin device.
- The cookie authenticates only `/web/` and `/admin/`, never the Jellyfin
  API; that keeps cross-site requests away from the API. Forms carry a CSRF
  token bound to the session.
- Security headers on every page: a strict Content-Security-Policy (no
  inline script, no third-party sources), `X-Content-Type-Options`,
  `Referrer-Policy`, `frame-ancestors 'none'`.
- Login lockout for passwords and passkeys alike, in the web pages and
  the Jellyfin API: after 10 failures within 15 minutes from one client
  address, or 20 for one user, further attempts wait 15 minutes. The higher
  limit per user makes it harder to lock someone out from outside; a
  successful login clears the user's count, not the address's, so a working
  account cannot be used to keep guessing others. The short delay after a
  failure from stage 1 stays.

### 3.3 Pages and scripts

- Pages are rendered on the server with `html/template`; templates, CSS and
  scripts are embedded in the binary (`embed`). No JavaScript framework and
  no build step: a few small scripts for the player, filters and passkeys.
- hls.js (Apache-2.0) is vendored at a pinned version for HLS in Chrome and
  Firefox; Safari plays HLS natively. Its licence goes into `NOTICE`.
- The web client reads the catalog directly in Go, not through the
  Jellyfin JSON; images use the existing image endpoints and caches.
- The pages are in German with `language de`, else in English.

### 3.4 Admin UI

- Overview: version, uptime, libraries and movie counts, last scan and its
  result, metadata status (matched, not found, failed, next refresh),
  outbound requests of the last 24 hours, running transcodes.
- Users: list with signed-in devices and last use; create; set password;
  grant or remove admin; delete; end sessions; list and remove passkeys.
- Library: start a scan; movies without metadata; search TMDB from the page
  and set or clear a match (the `homeflix match` commands); stream survey
  (`homeflix stats`).
- Settings are shown, not edited: the configuration stays a file checked
  with `homeflix serve -n`.

### 3.5 Playback

The server decides per file and browser; the browser reports what it can
decode (`MediaSource.isTypeSupported`, `canPlayType`), the server knows the
streams.

1. **Direct play:** the original file with ranges, as for Moonfin.
2. **Remux to HLS:** video copied, the chosen audio track converted to AAC
   (stereo, or 5.1 when the browser takes it), fragmented MP4 segments.
   Segments of copied video must start at keyframes, so a keyframe index of
   the file is made once (ffprobe, kept in the database) and the playlist is
   built from it. ffmpeg runs per play session from the segment asked for;
   a seek far ahead restarts it there. Truncated E-AC3 tracks (stage 1,
   section 2) are skipped in favour of another track.
3. **Transcode to HLS:** h264 with libx264 at fixed segment lengths, scaled
   if needed; HDR to SDR tone mapping (`zscale`/`tonemap` if the FreeBSD
   ffmpeg has them, checked in N5); image subtitles burnt in when chosen.

Sessions end when no segment is requested for a minute; their files in
`data/transcode` are removed. At most `transcode workers` ffmpeg processes
run; a further play request gets a clear "busy" answer instead of a stalled
player. Text subtitles come as WebVTT from the extraction cache; the first
request of a large MKV waits for the extraction, so the player shows that.
Progress, resume and played state go through the same store as Moonfin.

Once the HLS pipeline exists, PlaybackInfo also offers it to Jellyfin
clients (`TranscodingUrl`) for files outside their device profile, so
Moonfin apps on weaker devices benefit too.

### 3.6 Passkeys and QuickConnect

- Users register passkeys in the web client (account page); admins see and
  remove them. Stored per user: credential ID, public key, sign count, name,
  last use.
- The login page offers the passkey (conditional UI where the browser
  supports it) and the password. The `passkeys` block lists the HTTPS
  origins passkeys are offered on; each origin's host is its relying-party
  ID, and each stored passkey records it. Without the block, or on an origin
  not listed, only the password is offered.
- QuickConnect becomes real: a Moonfin app asks for a code, a signed-in user
  confirms it in the web client, the app gets its own token. Codes expire
  after a few minutes.

### 3.7 New configuration

In the style of stage 1 (`homeflix.conf.5`, checked with `-n`):

```
trusted proxy { 192.168.1.2, 127.0.0.1 }       # Caddy, tailscale serve
admin from { 192.168.1.0/24, 100.64.0.0/10 }   # default: loopback, private, tailnet
passkeys {                                     # HTTPS origins; each its own relying party
	origin "https://homeflix.example.ts.net"
	origin "https://kino.example.de"
}
transcode workers 2                            # ffmpeg processes for HLS at most
```

## 4. Milestones

Each milestone ends with a check over the tailnet (`tailscale serve`) and,
once it is set up, through Caddy, from a phone and a laptop.

| # | Content | Done when | Estimate |
|---|---|---|---|
| N0 | `trusted proxy`, client address, login lockout, `admin from`, web sessions with CSRF, security headers, embedded templates, login page | login works through `tailscale serve` (and Caddy once it is set up); the admin UI answers over the tailnet and refuses requests through Caddy from outside; lockout and forged `X-Forwarded-For` are tested | 2–3 days |
| N1 | Admin UI: overview, users and devices, scan, unmatched movies with TMDB search and match | every `homeflix user` and `homeflix match` task can be done in the browser; the 14 unmatched movies can be fixed there | 4–5 days |
| N2 | Web client: home screen, grid with sort, filter, search and alphabet, details, collections, people, account page; direct play with resume | browsing works on phone and laptop; the 1012 direct-play movies play and resume in sync with Moonfin; the others say why they cannot play yet | 1 week |
| N3 | Remux to HLS: keyframe index, segmenter, sessions, audio choice, AAC conversion, WebVTT subtitles; TranscodingUrl for Jellyfin clients | the 573 movies with AC3/DTS/TrueHD play with seeking and audio switching in Chrome, Firefox and Safari (91 % of the library) | 1–1.5 weeks |
| N4 | Passkeys per origin: register, sign in, remove; QuickConnect for apps | sign-in with a passkey from a phone over the tailnet name, and through Caddy once its domain exists; a Moonfin app signs in with a code confirmed in the web client | 3–4 days |
| N5 | Transcoding: libx264, HDR to SDR, burnt-in image subtitles, worker limit, idle stop; CPU measurement | Constantine (VC-1), Riddick (MPEG-2, DVD subtitles), Kampf der Welten (AVI) and Der Marsianer (HDR10) play in Chrome; two transcodes run at once without stalling | 1.5–2 weeks |
| N6 | Polish: keyboard use, accessibility checks, docs (README, manpage, web and API coverage), web smoke test | the smoke test signs in, browses and plays a remuxed and a transcoded test file | 2–3 days |

N1 comes before the web client because the admin UI is needed anyway and
exercises sessions, templates and the access rules on a small surface.

## 5. Testing

- HTTP tests through `httptest` for every page and form: access rules
  (admin networks, trusted proxies, forged headers), CSRF, lockout, cookie
  flags, security headers.
- WebAuthn registration and login against recorded ceremonies and the
  library's test helpers; QuickConnect expiry and single use.
- Playback decisions per stream combination from the probe fixtures of
  stage 1 and the browser capability sets of Safari, Chrome and Firefox.
- HLS tests on short files generated with ffmpeg (h264 + AC3 in MKV,
  VC-1, MPEG-2 with DVD subtitles, HEVC HDR10): playlist against keyframes,
  segment continuity, seek restart, session end and cleanup.
- The smoke test grows a web part: sign in, open details, fetch a playlist
  and the first segments.
- Manual acceptance per milestone in Safari, Chrome and Firefox, on a phone
  and a laptop, through Caddy and the tailnet.

## 6. Open decisions

1. **Caddy domain.** Not chosen yet; needed for N4 only. Passkeys on the
   tailnet name work without it.
2. **What Caddy forwards.** Only the web client (`/web/`, images, streams)
   or also the Jellyfin API, so that Moonfin apps work from outside? The
   admin UI stays inside either way (`admin from`).
3. **Accounts of remote viewers.** Created by an admin, as now; viewers can
   change their own password and passkeys. Invitations by link would be an
   addition.
4. **Dependencies.** `go-webauthn/webauthn` and vendored hls.js are the
   only new ones; both licences fit next to MIT.
