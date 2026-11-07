#!/bin/sh
# Runs a throwaway homeflix instance on 127.0.0.1 with its own data
# directory, a generated test user and a generated two-second movie, walks
# through the client flow with curl, and removes everything afterwards.
# A generated album (an MP3 and a WMA track) and audiobook (an M4B with
# chapters) check music, conversion to AAC and audiobooks.
# Needs ffmpeg and ffprobe in PATH.
#
# Usage: scripts/smoke-test.sh <path-to-homeflix-binary> [port]
# On the development host: ssh devhost 'sh -s -- homeflix/bin/homeflix' < scripts/smoke-test.sh
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

mkdir "$work/media"
printf '1\n00:00:00,500 --> 00:00:01,500\nsmoke subtitle\n' >"$work/sub.srt"
ffmpeg -loglevel error -f lavfi -i testsrc=duration=2:size=320x240:rate=10 -i "$work/sub.srt" \
	-map 0 -map 1 -c:v mpeg4 -c:s srt "$work/media/Smoke Test (2026).mkv" || fail "cannot make the test movie"
mkdir -p "$work/music/Smoke Band/Smoke Album" "$work/books/Smoke Author/Smoke Book"
for n in 1 2; do
	ext=mp3
	[ $n = 2 ] && ext=wma
	ffmpeg -loglevel error -f lavfi -i "sine=frequency=44$n:duration=8" -metadata title="Smoke Song $n" \
		-metadata artist="Smoke Band" -metadata album_artist="Smoke Band" -metadata album="Smoke Album" \
		-metadata track="$n" -metadata date=2026 -metadata genre=Smoke \
		"$work/music/Smoke Band/Smoke Album/0$n Smoke Song $n.$ext" || fail "cannot make test track $n"
done
printf ';FFMETADATA1\ntitle=Smoke Book\nartist=Smoke Author\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=5000\ntitle=One\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=5000\nEND=10000\ntitle=Two\n' >"$work/chapters.txt"
ffmpeg -loglevel error -f lavfi -i "sine=frequency=330:duration=10" -i "$work/chapters.txt" -map 0 -map_metadata 1 \
	-c:a aac "$work/books/Smoke Author/Smoke Book/Smoke Book.m4b" || fail "cannot make the test book"

cat >"$work/homeflix.conf" <<EOF
server name "smoke"
listen on 127.0.0.1 port $port
data directory "data"
log level debug
no scan interval
library "Smoke" { path "$work/media" }
library "Smoke Music" {
	type music
	path "$work/music"
}
library "Smoke Books" {
	type audiobooks
	path "$work/books"
}
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

expect 200 GET '/Items?Recursive=true&IncludeItemTypes=Movie'
item=$(sed -E 's/.*"Id":"([0-9a-f]{32})".*/\1/' "$work/body")
[ ${#item} -eq 32 ] || fail "test movie not in the library: $(cat "$work/body")"
expect 200 GET "/Videos/$item/$item/Subtitles/1/0/Stream.srt"
grep -q "smoke subtitle" "$work/body" || fail "extracted subtitles lack the text: $(cat "$work/body")"
expect 200 GET "/Videos/$item/$item/Subtitles/1/Stream.vtt"
grep -q "^WEBVTT" "$work/body" || fail "WebVTT output lacks its header: $(cat "$work/body")"
expect 404 GET "/Videos/$item/$item/Subtitles/0/Stream.srt"

# Music: an album of two tracks; the WMA track plays converted to AAC.
expect 200 GET /UserViews
grep -q '"CollectionType":"music"' "$work/body" || fail "no music library: $(cat "$work/body")"
grep -q '"CollectionType":"books"' "$work/body" || fail "no audiobook library: $(cat "$work/body")"
# ids prints the IDs of the items of a listing in order.
ids() { grep -o '"ServerId":"[0-9a-f]\{32\}","Id":"[0-9a-f]\{32\}"' "$work/body" | cut -d'"' -f8; }
expect 200 GET '/Items?Recursive=true&IncludeItemTypes=MusicAlbum'
album=$(ids | head -1)
grep -q '"Name":"Smoke Album"' "$work/body" || fail "test album missing: $(cat "$work/body")"
expect 200 GET "/Items?ParentId=$album&IncludeItemTypes=Audio&SortBy=ParentIndexNumber,IndexNumber"
grep -q '"TotalRecordCount":2' "$work/body" || fail "album tracks: $(cat "$work/body")"
mp3=$(ids | sed -n 1p)
wma=$(ids | sed -n 2p)
expect 200 GET "/Audio/$mp3/stream?Static=true"
profile='{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Audio","Container":"mp3,wma","AudioCodec":"mp3"}],"TranscodingProfiles":[{"Type":"Audio","Container":"ts","Protocol":"hls","AudioCodec":"aac"}]}}'
expect 200 POST "/Items/$wma/PlaybackInfo" "$profile"
url=$(sed -E 's/.*"TranscodingUrl":"([^"]+)".*/\1/; s/\\u0026/\&/g' "$work/body")
case $url in /Audio/*main.m3u8*) ;; *) fail "no conversion offered for WMA: $(cat "$work/body")" ;; esac
expect 200 GET "$url"
seg=$(grep '^hls/0.ts' "$work/body")
expect 200 GET "/Audio/$wma/$seg"
[ "$(head -c 1 "$work/body" | od -An -tx1 | tr -d ' ')" = 47 ] || fail "segment is not MPEG-TS"
expect 404 GET "/Audio/$wma/Lyrics"
# Audiobooks: one book with its chapters.
expect 200 GET '/Items?Recursive=true&IncludeItemTypes=AudioBook&Fields=Chapters'
grep -q '"Name":"Two"' "$work/body" || fail "book chapters missing: $(cat "$work/body")"

# The WebSocket handshake; curl then waits for data until it times out.
got=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
	-H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: c21va2UtdGVzdC1rZXkxMg==' \
	"$base/socket?ApiKey=$token&deviceId=smoke-1" || true)
[ "$got" = 101 ] || fail "WebSocket handshake: status $got, want 101"
echo "ok   101 GET /socket"
expect 204 POST /Sessions/Logout
expect 401 GET /Users/Me

grep -q "$token" "$work/server.log" && fail "access token appears in the server log"
grep -q "$password" "$work/server.log" && fail "password appears in the server log"
echo "smoke test passed"
