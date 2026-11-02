#!/bin/sh
# Cross-compiles homeflix for FreeBSD, copies it to a development host and
# (re)starts it there as the login user, without root and without rc.d.
#
# Layout on the host, under ~/homeflix:
#   bin/homeflix    the binary
#   homeflix.toml   configuration, created from deploy/dev.toml if missing
#   data/           database (data_dir = "data" in the dev config)
#   homeflix.log    server log
#   homeflix.pid    pid of the running server
#
# Usage: scripts/deploy-dev.sh [ssh-host]   (default: $HOMEFLIX_DEV_HOST or "claude")
set -eu

host=${1:-${HOMEFLIX_DEV_HOST:-claude}}
root=$(cd "$(dirname "$0")/.." && pwd)
bin="$root/build/homeflix-freebsd-amd64"
version=$(git -C "$root" describe --always --dirty 2>/dev/null || echo dev)

echo "building $version for freebsd/amd64"
(cd "$root" && GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 \
	go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$bin" ./cmd/homeflix)

ssh "$host" 'mkdir -p homeflix/bin'
scp -q "$bin" "$host:homeflix/bin/homeflix.new"
if ! ssh "$host" 'test -e homeflix/homeflix.toml'; then
	scp -q "$root/deploy/dev.toml" "$host:homeflix/homeflix.toml"
fi

ssh "$host" 'sh -s' <<'EOF'
set -eu
cd ~/homeflix
if [ -f homeflix.pid ]; then
	pid=$(cat homeflix.pid)
	if kill "$pid" 2>/dev/null; then
		i=0
		while kill -0 "$pid" 2>/dev/null; do
			i=$((i + 1))
			if [ "$i" -gt 50 ]; then
				echo "homeflix (pid $pid) did not stop within 10 s" >&2
				exit 1
			fi
			sleep 0.2
		done
	fi
	rm -f homeflix.pid
fi
mv bin/homeflix.new bin/homeflix
daemon -f -p homeflix.pid -o homeflix.log -t homeflix bin/homeflix serve -config homeflix.toml
sleep 1
if [ -f homeflix.pid ] && kill -0 "$(cat homeflix.pid)" 2>/dev/null; then
	echo "homeflix running, pid $(cat homeflix.pid)"
	tail -n 5 homeflix.log
else
	echo "homeflix failed to start:" >&2
	tail -n 20 homeflix.log >&2
	exit 1
fi
EOF
