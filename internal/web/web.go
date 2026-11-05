// Package web serves homeflix's own web pages: the web client under /web/
// and the admin UI under /admin/. Pages are rendered on the server from
// embedded templates; a session cookie signs the user in.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/clientaddr"
	"homeflix/internal/lockout"
	"homeflix/internal/metadata"
	"homeflix/internal/outbound"
	"homeflix/internal/store"
	"homeflix/internal/subtitles"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

// Server holds what the pages need.
type Server struct {
	ServerName string
	Store      *store.Store
	Log        *slog.Logger
	// Logins refuses logins after too many failures; shared with the
	// Jellyfin API.
	Logins *lockout.Guard
	// AdminFrom are the networks the admin UI answers from.
	AdminFrom []netip.Prefix
	// Language of the pages: "de" or "en".
	Language string
	// FailedLoginDelay slows down password guessing.
	FailedLoginDelay time.Duration

	// For the admin UI:
	Version string
	Started time.Time
	Catalog *catalog.Catalog
	// Metadata and Outbound are nil when TMDB is off.
	Metadata    *metadata.Service
	Outbound    *outbound.Client
	RequestScan func()
	ScanStatus  func() ScanStatus
	// Reload rebuilds the catalog after a match changed.
	Reload   func()
	Settings []Setting

	// For the web client: text subtitles as WebVTT; nil leaves them out.
	Subtitles *subtitles.Service

	pages map[string]*template.Template
}

// Handler returns the handler for /web/ and /admin/.
func (s *Server) Handler() http.Handler {
	s.pages = parsePages()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /web", redirect("/web/"))
	mux.HandleFunc("GET /web/login", s.loginPage)
	mux.HandleFunc("POST /web/login", s.login)
	mux.HandleFunc("POST /web/logout", s.signedIn(s.logout))
	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("GET /web/static/", http.StripPrefix("/web/static/", staticHandler(static)))
	// Browsers ask for it on their own; the icon is an SVG.
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		b, _ := fs.ReadFile(static, "favicon.svg")
		w.Write(b)
	})
	s.registerClient(mux)
	s.registerAdmin(mux)
	return securityHeaders(sameOrigin(mux))
}

func redirect(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) }
}

// securityHeaders keeps the pages from being framed, sniffed or made to
// run scripts from elsewhere.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
			"connect-src 'self'; media-src 'self' blob:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses state-changing requests sent by other sites: their
// Origin (or, without it, Referer) must be this server as the client sees
// it.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			if origin == "" {
				if u, err := url.Parse(r.Referer()); err == nil && u.Host != "" {
					origin = u.Scheme + "://" + u.Host
				}
			}
			if !strings.EqualFold(origin, clientaddr.From(r).Origin()) {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// staticHandler serves embedded files. Pages link them with their version
// (asset), so those requests may be cached for good; others for an hour.
func staticHandler(files fs.FS) http.Handler {
	fsrv := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.URL.Query().Get("v"); v != "" && v == assetVersions[strings.TrimPrefix(r.URL.Path, "/")] {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		fsrv.ServeHTTP(w, r)
	})
}

// adminAllowed reports whether r comes from a network the admin UI
// answers.
func (s *Server) adminAllowed(r *http.Request) bool {
	return clientaddr.Contains(s.AdminFrom, clientaddr.From(r).Addr)
}

// admin wraps h so it runs only for administrators from the admin
// networks.
func (s *Server) admin(h sessionHandler) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		switch {
		case !s.adminAllowed(r):
			s.Log.Warn("admin UI refused outside the admin networks", "user", sess.User.Name,
				"remote", clientaddr.From(r).String())
			s.render(w, r, http.StatusForbidden, "error", sess, map[string]any{"Message": "adminNetwork"})
		case !sess.User.IsAdmin:
			s.render(w, r, http.StatusForbidden, "error", sess, map[string]any{"Message": "adminOnly"})
		default:
			h(w, r, sess)
		}
	}
}
