package jellyfin

import (
	"net/http"
	"sort"
	"strings"
)

// Router matches request paths the way Jellyfin does: case-insensitively,
// ignoring empty segments and a trailing slash, and with an optional legacy
// "/emby" or "/mediabrowser" prefix. Path parameters are written as {name},
// either as a whole segment or after a literal prefix ("stream.{container}"),
// and are available through Request.PathValue. A GET route also answers
// HEAD. When several routes match, literal segments win over parameters,
// position by position.
type Router struct {
	routes   []route
	NotFound http.Handler
}

type segmentKind int

const (
	kindLiteral segmentKind = iota
	kindPrefixed
	kindParam
)

type segment struct {
	kind  segmentKind
	text  string // lowercased literal or prefix
	param string
}

type route struct {
	method   string
	pattern  string
	segments []segment
	handler  http.Handler
	order    int
}

// Handle registers h for method and pattern, for example
// "GET", "/Items/{id}/Images/{type}".
func (rt *Router) Handle(method, pattern string, h http.Handler) {
	var segs []segment
	for _, s := range splitPath(pattern) {
		open := strings.IndexByte(s, '{')
		switch {
		case open < 0:
			segs = append(segs, segment{kind: kindLiteral, text: strings.ToLower(s)})
		case !strings.HasSuffix(s, "}") || strings.Count(s, "{") != 1:
			panic("jellyfin: bad path segment " + s + " in " + pattern)
		case open == 0:
			segs = append(segs, segment{kind: kindParam, param: s[1 : len(s)-1]})
		default:
			segs = append(segs, segment{kind: kindPrefixed, text: strings.ToLower(s[:open]), param: s[open+1 : len(s)-1]})
		}
	}
	rt.routes = append(rt.routes, route{
		method:   method,
		pattern:  pattern,
		segments: segs,
		handler:  h,
		order:    len(rt.routes),
	})
	rt.sortRoutes()
}

// HandleFunc registers a handler function.
func (rt *Router) HandleFunc(method, pattern string, h http.HandlerFunc) {
	rt.Handle(method, pattern, h)
}

func (rt *Router) sortRoutes() {
	sort.SliceStable(rt.routes, func(i, j int) bool {
		a, b := rt.routes[i].segments, rt.routes[j].segments
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k].kind != b[k].kind {
				return a[k].kind < b[k].kind
			}
		}
		return rt.routes[i].order < rt.routes[j].order
	})
}

func splitPath(p string) []string {
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// requestSegments splits the request path and drops a legacy prefix.
func requestSegments(p string) []string {
	segs := splitPath(p)
	if len(segs) > 0 {
		switch strings.ToLower(segs[0]) {
		case "emby", "mediabrowser":
			segs = segs[1:]
		}
	}
	return segs
}

func (r *route) match(segs []string) (map[string]string, bool) {
	if len(segs) != len(r.segments) {
		return nil, false
	}
	var params map[string]string
	set := func(k, v string) {
		if params == nil {
			params = make(map[string]string, 2)
		}
		params[k] = v
	}
	for i, s := range r.segments {
		got := segs[i]
		switch s.kind {
		case kindLiteral:
			if !strings.EqualFold(got, s.text) {
				return nil, false
			}
		case kindPrefixed:
			if len(got) <= len(s.text) || !strings.EqualFold(got[:len(s.text)], s.text) {
				return nil, false
			}
			set(s.param, got[len(s.text):])
		case kindParam:
			set(s.param, got)
		}
	}
	return params, true
}

// ServeHTTP dispatches the request. Routes must not be added concurrently
// with serving.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	segs := requestSegments(r.URL.Path)
	var allowed []string
	for i := range rt.routes {
		rte := &rt.routes[i]
		params, ok := rte.match(segs)
		if !ok {
			continue
		}
		if rte.method != r.Method && !(rte.method == http.MethodGet && r.Method == http.MethodHead) {
			allowed = append(allowed, rte.method)
			continue
		}
		for k, v := range params {
			r.SetPathValue(k, v)
		}
		r.Pattern = rte.pattern
		rte.handler.ServeHTTP(w, r)
		return
	}
	if len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if rt.NotFound != nil {
		rt.NotFound.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}
