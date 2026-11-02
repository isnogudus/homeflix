package jellyfin

import (
	"net/http"
	"net/url"
	"strings"
)

// AuthHeader holds the fields of a Jellyfin authorization header:
//
//	MediaBrowser Client="Moonfin", Device="iMac", DeviceId="…", Version="3.0.0", Token="…"
type AuthHeader struct {
	Client   string
	Device   string
	DeviceID string
	Version  string
	Token    string
}

// parseAuthHeader reads Authorization, falling back to X-Emby-Authorization.
// Field names are case-insensitive and values may be URL-encoded.
func parseAuthHeader(r *http.Request) AuthHeader {
	raw := r.Header.Get("Authorization")
	if raw == "" {
		raw = r.Header.Get("X-Emby-Authorization")
	}
	scheme, rest, ok := strings.Cut(strings.TrimSpace(raw), " ")
	if !ok || !(strings.EqualFold(scheme, "MediaBrowser") || strings.EqualFold(scheme, "Emby")) {
		return AuthHeader{}
	}
	var h AuthHeader
	for _, part := range splitAuthParams(rest) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if dec, err := url.QueryUnescape(v); err == nil {
			v = dec
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "client":
			h.Client = v
		case "device":
			h.Device = v
		case "deviceid":
			h.DeviceID = v
		case "version":
			h.Version = v
		case "token":
			h.Token = v
		}
	}
	return h
}

// splitAuthParams splits on commas outside double quotes.
func splitAuthParams(s string) []string {
	var parts []string
	inQuotes := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuotes = !inQuotes
		case ',':
			if !inQuotes {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}
