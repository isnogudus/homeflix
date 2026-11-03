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

Status: milestone M2 of [the stage 1 plan](docs/plan-stage1.md). Clients can
find the server and sign in; the libraries are scanned and probed, but the
API shows them only from M3 on.

## Build

Requires Go 1.26 or newer.

```sh
make check            # vet, gofmt check, tests with the race detector
make build            # binary for this machine in build/
make build-freebsd    # build/homeflix-freebsd-amd64, no cgo needed
```

## Run

```sh
cp deploy/homeflix.toml.sample homeflix.toml   # edit as needed
build/homeflix serve -config homeflix.toml
```

On FreeBSD, install the binary as `/usr/local/bin/homeflix`, the
configuration as `/usr/local/etc/homeflix.toml`, and
`deploy/freebsd/homeflix` as `/usr/local/etc/rc.d/homeflix`, then:

```sh
pw useradd homeflix -d /nonexistent -s /usr/sbin/nologin
sysrc homeflix_enable=YES
service homeflix start
```

The account needs read access to the media directories.

## Libraries

Each `[[library]]` block in the configuration names one or more
directories. A movie is

- a single file: `Heat (1995) [imdbid-tt0113277].mkv`,
- part files next to each other: `Intimacy (2001) cd1.avi`, `… cd2.avi`,
- or a folder `Title (Year)` holding one or more versions (`… [HD].mp4`,
  `… [SD] part1.mp4`, `… [SD] part2.mp4`) and extras folders such as
  `behind the scenes`, `featurettes` or `trailers`.

The year and an `[imdbid-tt…]` or `{imdb-tt…}` tag are read from the name.
Scans run at startup, every `scan_interval` and on `POST /Library/Refresh`;
only new or changed files are probed with ffprobe. Renamed and moved files
keep their item (matched by size and modification time, or by IMDb ID); a
movie that disappears is kept for 30 days, and a library directory that
cannot be read leaves its movies untouched.

```sh
homeflix scan -config /usr/local/etc/homeflix.toml            # scan once
homeflix scan -reprobe -config /usr/local/etc/homeflix.toml   # probe everything again
homeflix stats -config /usr/local/etc/homeflix.toml           # containers, codecs, HDR, failures
```

## Users

Users are managed on the command line; there is no sign-up through the API.
Passwords are read without echo (or as one line from standard input).

```sh
homeflix user add anna -admin -config /usr/local/etc/homeflix.toml
homeflix user passwd anna       # also signs anna out on every device
homeflix user list              # users with their signed-in devices
homeflix user delete anna
```

`/Users/Public` always returns an empty list, so login screens ask for name
and password instead of showing who has an account.

## Development host

`scripts/deploy-dev.sh [host]` cross-compiles, copies the binary to
`~/homeflix` on the host over SSH and restarts it as the login user, without
root. The default host is `claude` (override with `HOMEFLIX_DEV_HOST`).

`scripts/smoke-test.sh <binary>` starts a throwaway instance on 127.0.0.1
with a generated test user, runs the client login flow with curl and
removes everything afterwards:

```sh
ssh claude 'sh -s -- homeflix/bin/homeflix' < scripts/smoke-test.sh
```
