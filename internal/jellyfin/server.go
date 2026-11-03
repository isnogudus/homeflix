// Package jellyfin implements the subset of the Jellyfin server HTTP API that
// homeflix supports.
package jellyfin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"homeflix/internal/store"
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
	// RequestScan asks for a library scan without waiting for it.
	RequestScan func()

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
	return s.recoverPanics(s.logRequests(rt))
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
			"remote", r.RemoteAddr,
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
