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

Status: milestone M1 of [the stage 1 plan](docs/plan-stage1.md). Clients can
find the server and sign in; the home screen is empty until the library
arrives in M2–M4.

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
