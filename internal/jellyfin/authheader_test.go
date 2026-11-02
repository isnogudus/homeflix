package jellyfin

import (
	"net/http/httptest"
	"testing"
)

func TestParseAuthHeader(t *testing.T) {
	tests := []struct {
		name, header, value string
		want                AuthHeader
	}{
		{
			name:   "moonfin",
			header: "Authorization",
			value:  `MediaBrowser Client="Moonfin", Device="iMac", DeviceId="abc", Version="3.0.0", Token="t0k"`,
			want:   AuthHeader{Client: "Moonfin", Device: "iMac", DeviceID: "abc", Version: "3.0.0", Token: "t0k"},
		},
		{
			name:   "legacy header, emby scheme, url-encoded, odd casing",
			header: "X-Emby-Authorization",
			value:  `Emby client="Jellyfin%20Web",DEVICE="Fire, fox",deviceId="x"`,
			want:   AuthHeader{Client: "Jellyfin Web", Device: "Fire, fox", DeviceID: "x"},
		},
		{
			name:   "bearer is not a jellyfin header",
			header: "Authorization",
			value:  "Bearer abc",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set(tc.header, tc.value)
			if got := parseAuthHeader(r); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
