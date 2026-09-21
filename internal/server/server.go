// Package server exposes the registry over a loopback HTTP API and serves the
// dashboard.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kd14/ranger/internal/core"
	"github.com/kd14/ranger/internal/csi"
	"github.com/kd14/ranger/internal/insight"
	"github.com/kd14/ranger/internal/scan"
)

// TokenCookie is the name of the session cookie holding the access token.
const TokenCookie = "ranger_token"

// Config configures the HTTP listener.
type Config struct {
	// Addr is the listen address. Non-loopback addresses require AllowRemote.
	Addr string
	// Token authenticates requests. Generated when empty.
	Token string
	// AllowRemote disables the loopback-only guard. Scan data reveals your
	// physical surroundings, so this is opt-in.
	AllowRemote bool
	// Assets serves the dashboard. Callers pass the embedded FS, or a
	// directory during development.
	Assets fs.FS
	Log    *slog.Logger
}

// Server wires the registry and scanner manager to HTTP handlers.
type Server struct {
	cfg       Config
	reg       *core.Registry
	mgr       *scan.Manager
	places    *insight.PlaceService
	csi       *csi.Service
	log       *slog.Logger
	startedAt time.Time
	handler   http.Handler

	mu    sync.RWMutex
	bound string

	ready     chan struct{}
	readyOnce sync.Once
}

// SetCSI attaches an optional CSI ingest service whose state is included in
// snapshots. It must be called before ListenAndServe.
func (s *Server) SetCSI(service *csi.Service) { s.csi = service }

// New builds a server. The scanner manager and place service are optional.
func New(cfg Config, reg *core.Registry, mgr *scan.Manager, places *insight.PlaceService) (*Server, error) {
	if reg == nil {
		return nil, errors.New("server: registry is required")
	}
	if cfg.Assets == nil {
		return nil, errors.New("server: assets filesystem is required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8787"
	}
	if cfg.Token == "" {
		token, err := GenerateToken()
		if err != nil {
			return nil, fmt.Errorf("server: generate token: %w", err)
		}
		cfg.Token = token
	}
	if !cfg.AllowRemote && !isLoopbackAddr(cfg.Addr) {
		return nil, fmt.Errorf("server: refusing to bind non-loopback address %q without --allow-remote", cfg.Addr)
	}

	s := &Server{cfg: cfg, reg: reg, mgr: mgr, places: places, log: cfg.Log, startedAt: time.Now(), ready: make(chan struct{})}
	s.handler = s.routes()
	return s, nil
}

// Ready is closed once the listener is bound and BoundAddr is populated.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Token returns the access token clients must present.
func (s *Server) Token() string { return s.cfg.Token }

// BoundAddr returns the address actually listened on, which differs from the
// configured one when port 0 requests an ephemeral port. It is empty until the
// listener is open.
func (s *Server) BoundAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bound
}

// URL is the address a browser should open, including the bootstrap token.
func (s *Server) URL() string {
	addr := s.cfg.Addr
	if bound := s.BoundAddr(); bound != "" {
		addr = bound
	}

	host := addr
	if h, port, err := net.SplitHostPort(addr); err == nil {
		if h == "" || h == "0.0.0.0" || h == "::" {
			h = "127.0.0.1"
		}
		host = net.JoinHostPort(h, port)
	}
	return fmt.Sprintf("http://%s/?token=%s", host, s.cfg.Token)
}

// Handler exposes the fully wrapped mux, for tests.
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe runs until the context is cancelled, then shuts down cleanly.
func (s *Server) ListenAndServe(ctx context.Context) error {
	// Request contexts derive from this one. Shutdown waits for handlers to
	// return, and an SSE handler only returns when its request context ends,
	// so cancelling the base context is what lets shutdown finish.
	baseCtx, releaseHandlers := context.WithCancel(context.Background())
	defer releaseHandlers()

	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: the SSE stream is intentionally long-lived.
		IdleTimeout: 120 * time.Second,
		ErrorLog:    slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}

	listener, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("server: listen on %s: %w", s.cfg.Addr, err)
	}

	s.mu.Lock()
	s.bound = listener.Addr().String()
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })

	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		releaseHandlers()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server: shutdown: %w", err)
		}
		return nil
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/snapshot", s.authed(http.HandlerFunc(s.handleSnapshot)))
	mux.Handle("GET /api/stream", s.authed(http.HandlerFunc(s.handleStream)))
	mux.Handle("POST /api/places/rename", s.authed(http.HandlerFunc(s.handlePlaceRename)))
	mux.Handle("POST /api/places/forget", s.authed(http.HandlerFunc(s.handlePlaceForget)))
	mux.Handle("GET /", s.authed(http.HandlerFunc(s.handleAssets)))
	return s.secureHeaders(s.checkHost(mux))
}

// GenerateToken returns a cryptographically random access token.
func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// secureHeaders applies a restrictive policy to every response. The CSP
// forbids inline scripts, so all dashboard JavaScript lives in separate files.
func (s *Server) secureHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self'; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"font-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'none'; " +
		"frame-ancestors 'none'"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// checkHost rejects requests whose Host header is not one this server expects.
// Without it, any web page could point a hostname it controls at 127.0.0.1 and
// read your scan results through the victim's browser (DNS rebinding).
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.AllowRemote && !isAllowedHost(r.Host) {
			s.log.Warn("rejected request with unexpected Host header", "host", r.Host, "path", r.URL.Path)
			http.Error(w, "invalid host", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authed enforces the access token and same-origin requests.
func (s *Server) authed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.sameOrigin(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}

		// A token in the query string bootstraps the session, then is
		// exchanged for a cookie and stripped from the URL so it does not
		// linger in history or referrers.
		if tok := r.URL.Query().Get("token"); tok != "" {
			if !s.validToken(tok) {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     TokenCookie,
				Value:    tok,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			target := *r.URL
			q := target.Query()
			q.Del("token")
			target.RawQuery = q.Encode()
			http.Redirect(w, r, target.RequestURI(), http.StatusSeeOther)
			return
		}

		if !s.validToken(s.requestToken(r)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestToken(r *http.Request) string {
	if c, err := r.Cookie(TokenCookie); err == nil {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.Header.Get("X-Ranger-Token")
}

func (s *Server) validToken(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) == 1
}

// sameOrigin rejects requests a different site initiated.
func (s *Server) sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := parseOrigin(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u, r.Host)
}

func isAllowedHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	switch strings.ToLower(h) {
	case "localhost", "127.0.0.1", "::1", "[::1]", "":
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func parseOrigin(origin string) (string, error) {
	for _, prefix := range []string{"http://", "https://"} {
		if strings.HasPrefix(origin, prefix) {
			return strings.TrimPrefix(origin, prefix), nil
		}
	}
	return "", fmt.Errorf("unsupported origin %q", origin)
}
