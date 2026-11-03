#!/bin/sh
# Runs a throwaway homeflix instance on 127.0.0.1 with its own data
# directory and a generated test user, walks through the client login flow
# with curl, and removes everything afterwards.
#
# Usage: scripts/smoke-test.sh <path-to-homeflix-binary> [port]
# On the development host: ssh claude 'sh -s -- homeflix/bin/homeflix' < scripts/smoke-test.sh
set -eu

bin=$1
port=${2:-18096}
base="http://127.0.0.1:$port"
work=$(mktemp -d -t homeflix-smoke)
pid=""

cleanup() {
	if [ -n "$pid" ]; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

fail() {
	echo "FAIL: $*" >&2
	echo "--- server log" >&2
	cat "$work/server.log" >&2
	exit 1
}

cat >"$work/homeflix.conf" <<EOF
server name "smoke"
listen on 127.0.0.1 port $port
data directory "data"
log level debug
no scan interval
EOF

password=$(openssl rand -hex 16)
printf '%s\n' "$password" | "$bin" user add smoke -config "$work/homeflix.conf" >/dev/null

"$bin" serve -config "$work/homeflix.conf" >"$work/server.log" 2>&1 &
pid=$!
i=0
until curl -fs "$base/System/Ping" >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -gt 50 ] && fail "server did not come up"
	sleep 0.1
done

client='MediaBrowser Client="Smoke", Device="smoke-test", DeviceId="smoke-1", Version="0"'

# expect STATUS METHOD PATH [BODY]: checks the status and leaves the body in $work/body
expect() {
	want=$1 method=$2 path=$3 body=${4:-}
	auth=$client
	[ -n "${token:-}" ] && auth="$auth, Token=\"$token\""
	got=$(curl -s -o "$work/body" -w '%{http_code}' -X "$method" -H "Authorization: $auth" \
		-H 'Content-Type: application/json' ${body:+--data "$body"} "$base$path")
	[ "$got" = "$want" ] || fail "$method $path: status $got, want $want: $(cat "$work/body")"
	echo "ok   $want $method $path"
}

expect 200 GET /System/Info/Public
expect 200 GET /Users/Public
expect 401 GET /Users/Me
expect 401 POST /Users/AuthenticateByName '{"Username":"smoke","Pw":"wrong"}'
expect 200 POST /Users/AuthenticateByName "{\"Username\":\"smoke\",\"Pw\":\"$password\"}"
token=$(sed -E 's/.*"AccessToken":"([0-9a-f]{32})".*/\1/' "$work/body")
[ ${#token} -eq 32 ] || fail "no access token in login response"
grep -q '"Configuration":{' "$work/body" || fail "login response lacks a Configuration object"

expect 200 GET /Users/Me
expect 200 GET /System/Info
expect 204 POST /Sessions/Capabilities/Full '{"PlayableMediaTypes":["Video"]}'
expect 200 GET '/DisplayPreferences/usersettings?client=smoke'
expect 204 POST '/DisplayPreferences/usersettings?client=smoke' '{"SortBy":"ProductionYear"}'
expect 200 GET '/DisplayPreferences/usersettings?client=smoke'
grep -q '"SortBy":"ProductionYear"' "$work/body" || fail "display preferences not stored"
expect 200 GET /UserViews
expect 200 GET /Items/Latest
expect 200 GET /Sessions
expect 204 POST /Sessions/Logout
expect 401 GET /Users/Me

grep -q "$token" "$work/server.log" && fail "access token appears in the server log"
grep -q "$password" "$work/server.log" && fail "password appears in the server log"
echo "smoke test passed"
