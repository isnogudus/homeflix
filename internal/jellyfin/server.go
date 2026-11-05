// Package jellyfin implements the subset of the Jellyfin server HTTP API that
// homeflix supports.
package jellyfin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/clientaddr"
	"homeflix/internal/images"
	"homeflix/internal/lockout"
	"homeflix/internal/store"
	"homeflix/internal/subtitles"
)

// CompatVersion is the Jellyfin server version homeflix reports. Clients
// gate features on it.
const CompatVersion = "10.11.8"

// ProductName must contain "Jellyfin": clients use it to pick their Jellyfin
// code path.
const ProductName = "Jellyfin Server"

// Server holds what the API handlers need.
type Server struct {
	ServerID   string
	ServerName string
	Store      *store.Store
	Log        *slog.Logger
	// FailedLoginDelay slows down password guessing.
	FailedLoginDelay time.Duration
	// Logins refuses logins after too many failures; nil allows all.
	Logins *lockout.Guard
	// Clients resolves client addresses behind trusted proxies.
	Clients clientaddr.Resolver
	// AdminFrom are the networks administrative requests are allowed
	// from; nil allows them from anywhere.
	AdminFrom []netip.Prefix
	// Web, if set, serves the web client and the admin UI under /web/ and
	// /admin/.
	Web http.Handler
	// RequestScan asks for a library scan without waiting for it.
	RequestScan func()
	Catalog     *catalog.Catalog
	Images      *images.Service
	Subtitles   *subtitles.Service
	// Shutdown ends long-lived connections such as WebSockets, which
	// http.Server.Shutdown does not wait for.
	Shutdown context.Context

	unknownMu   sync.Mutex
	unknownSeen map[string]struct{}
}

// maxUnknownPaths bounds the memory used to log each unknown path once.
const maxUnknownPaths = 1000

// Handler returns the complete HTTP handler with logging and panic recovery.
func (s *Server) Handler() http.Handler {
	rt := &Router{NotFound: http.HandlerFunc(s.notFound)}
	s.registerSystem(rt)
	s.registerUsers(rt)
	s.registerDisplayPreferences(rt)
	s.registerSessions(rt)
	s.registerLibrary(rt)
	s.registerItems(rt)
	s.registerImages(rt)
	s.registerPlayback(rt)
	s.registerUserData(rt)
	s.registerSocket(rt)
	s.registerSubtitles(rt)
	var h http.Handler = rt
	if s.Web != nil {
		h = s.withWeb(rt)
	}
	return s.Clients.Middleware(s.recoverPanics(s.logRequests(h)))
}

// withWeb sends the web client's and the admin UI's paths to s.Web, and
// the bare server address to the web client, as Jellyfin does.
func (s *Server) withWeb(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.ToLower(r.URL.Path)
		switch {
		case p == "/" && r.Method == http.MethodGet:
			http.Redirect(w, r, "/web/", http.StatusFound)
		case p == "/web" || strings.HasPrefix(p, "/web/") || p == "/admin" || strings.HasPrefix(p, "/admin/") ||
			p == "/favicon.ico":
			s.Web.ServeHTTP(w, r)
		default:
			api.ServeHTTP(w, r)
		}
	})
}

// adminAllowed reports whether administrative requests are allowed from
// the client of r.
func (s *Server) adminAllowed(r *http.Request) bool {
	return s.AdminFrom == nil || clientaddr.Contains(s.AdminFrom, clientaddr.From(r).Addr)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.unknownMu.Lock()
	if s.unknownSeen == nil {
		s.unknownSeen = make(map[string]struct{})
	}
	key := r.Method + " " + r.URL.Path
	_, seen := s.unknownSeen[key]
	if !seen && len(s.unknownSeen) < maxUnknownPaths {
		s.unknownSeen[key] = struct{}{}
	}
	s.unknownMu.Unlock()
	if !seen {
		s.Log.Warn("unsupported endpoint", "method", r.Method, "path", r.URL.Path, "client", clientName(r))
	}
	http.NotFound(w, r)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack lets WebSocket connections take over the connection.
func (w *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("connection cannot be taken over")
	}
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

// Flush sends buffered data to the client.
func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom keeps the underlying writer's ReadFrom reachable, so files are
// sent with sendfile(2) instead of being copied through user space.
func (w *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	var n int64
	var err error
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(r)
	} else {
		n, err = io.Copy(w.ResponseWriter, r)
	}
	w.bytes += n
	return n, err
}

// logRequests logs every request at debug level. The query string is left
// out because clients put access tokens there.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		s.Log.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", rec.bytes,
			"duration", time.Since(start).Round(time.Microsecond),
			"remote", clientaddr.From(r).String(),
			"client", clientName(r))
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Log.Error("handler panic", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// clientName returns the client application named in the authorization
// header, or the user agent.
func clientName(r *http.Request) string {
	if c := parseAuthHeader(r).Client; c != "" {
		return c
	}
	return r.UserAgent()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}
