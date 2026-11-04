package clientaddr

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestResolve(t *testing.T) {
	res := Resolver{Trusted: []netip.Prefix{netip.MustParsePrefix("192.168.1.2/32"), netip.MustParsePrefix("127.0.0.1/32")}}
	for name, tc := range map[string]struct {
		remote  string
		headers map[string]string
		want    Info
	}{
		"direct": {"192.168.1.20:5000", nil, Info{Addr: netip.MustParseAddr("192.168.1.20"), Scheme: "http", Host: "kino"}},
		"untrusted sender's headers are ignored": {"203.0.113.9:5000",
			map[string]string{"X-Forwarded-For": "100.64.1.1", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "evil"},
			Info{Addr: netip.MustParseAddr("203.0.113.9"), Scheme: "http", Host: "kino"}},
		"tailscale serve": {"127.0.0.1:40000",
			map[string]string{"X-Forwarded-For": "100.101.102.103", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "homeflix.example.ts.net"},
			Info{Addr: netip.MustParseAddr("100.101.102.103"), Scheme: "https", Host: "homeflix.example.ts.net", Proxy: true}},
		// The client put a tailnet address in front; Caddy appended the real one.
		"caddy, forged prefix": {"192.168.1.2:40000",
			map[string]string{"X-Forwarded-For": "100.64.1.1, 203.0.113.9", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "kino.example.de"},
			Info{Addr: netip.MustParseAddr("203.0.113.9"), Scheme: "https", Host: "kino.example.de", Proxy: true}},
		// tailscale serve in front of Caddy: both are trusted, the client is left of them.
		"two trusted hops": {"192.168.1.2:40000",
			map[string]string{"X-Forwarded-For": "100.101.102.103, 127.0.0.1"},
			Info{Addr: netip.MustParseAddr("100.101.102.103"), Scheme: "http", Host: "kino", Proxy: true}},
		"garbage stops the walk": {"192.168.1.2:40000",
			map[string]string{"X-Forwarded-For": "100.64.1.1, nonsense"},
			Info{Addr: netip.MustParseAddr("192.168.1.2"), Scheme: "http", Host: "kino", Proxy: true}},
		"bad forwarded host": {"127.0.0.1:40000",
			map[string]string{"X-Forwarded-Host": "evil.com/path"},
			Info{Addr: netip.MustParseAddr("127.0.0.1"), Scheme: "http", Host: "kino", Proxy: true}},
		"IPv4 in IPv6": {"[::ffff:192.168.1.2]:40000", map[string]string{"X-Forwarded-For": "203.0.113.9"},
			Info{Addr: netip.MustParseAddr("203.0.113.9"), Scheme: "http", Host: "kino", Proxy: true}},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://kino/", nil)
			r.RemoteAddr = tc.remote
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := res.Resolve(r); got != tc.want {
				t.Errorf("\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestContains(t *testing.T) {
	nets := []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("::1/128")}
	for a, want := range map[string]bool{"100.101.1.1": true, "100.128.0.1": false, "::1": true, "::ffff:100.64.0.1": true} {
		if got := Contains(nets, netip.MustParseAddr(a)); got != want {
			t.Errorf("%s: %v", a, got)
		}
	}
}
