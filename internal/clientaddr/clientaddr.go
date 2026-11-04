// Package clientaddr finds out who sent a request: the client's address and
// the scheme and host it used. X-Forwarded-* headers count only when the
// request comes from a trusted proxy, such as Caddy or tailscale serve;
// from anyone else they could be forged.
package clientaddr

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
)

// Info describes the sender of a request.
type Info struct {
	Addr   netip.Addr // the client, behind any trusted proxies
	Scheme string     // "http" or "https", as the client used it
	Host   string     // the host the client asked for
	Proxy  bool       // the request came through a trusted proxy
}

// Resolver resolves requests with a set of trusted proxies.
type Resolver struct {
	Trusted []netip.Prefix
}

// Contains reports whether a is in one of the networks.
func Contains(nets []netip.Prefix, a netip.Addr) bool {
	a = a.Unmap()
	for _, n := range nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// Resolve finds the sender of r.
func (res Resolver) Resolve(r *http.Request) Info {
	info := Info{Scheme: "http", Host: r.Host}
	if r.TLS != nil {
		info.Scheme = "https"
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		if a, err := netip.ParseAddr(r.RemoteAddr); err == nil {
			info.Addr = a.Unmap()
		}
		return info
	}
	info.Addr = ap.Addr().Unmap()
	if !Contains(res.Trusted, info.Addr) {
		return info
	}
	info.Proxy = true
	// The client is the last address not added by a trusted proxy;
	// addresses further left were sent by the client and prove nothing.
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(hops[i]), "[]"))
		if err != nil {
			break
		}
		info.Addr = a.Unmap()
		if !Contains(res.Trusted, info.Addr) {
			break
		}
	}
	if p := firstValue(r, "X-Forwarded-Proto"); p == "http" || p == "https" {
		info.Scheme = p
	}
	if h := firstValue(r, "X-Forwarded-Host"); h != "" && !strings.ContainsAny(h, " /\\@") {
		info.Host = h
	}
	return info
}

func firstValue(r *http.Request, name string) string {
	v, _, _ := strings.Cut(r.Header.Get(name), ",")
	return strings.ToLower(strings.TrimSpace(v))
}

type key struct{}

// Middleware resolves every request once and keeps the result for From.
func (res Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), key{}, res.Resolve(r))))
	})
}

// From returns the sender of a request passed through Middleware; without
// it, the request is resolved trusting no proxy.
func From(r *http.Request) Info {
	if info, ok := r.Context().Value(key{}).(Info); ok {
		return info
	}
	return Resolver{}.Resolve(r)
}

// String returns the client address, or "" if it is unknown.
func (i Info) String() string {
	if !i.Addr.IsValid() {
		return ""
	}
	return i.Addr.String()
}

// Origin is scheme and host as the client used them, such as
// "https://homeflix.example.ts.net".
func (i Info) Origin() string { return i.Scheme + "://" + i.Host }
