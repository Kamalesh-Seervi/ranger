package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kd14/ranger/internal/core"
)

const testToken = "0123456789abcdef"

func newTestServer(t *testing.T) (*Server, *core.Registry) {
	t.Helper()

	registry := core.NewRegistry(core.Config{}, core.NewBus())
	assets := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>ranger</title>")},
		"app.js":     &fstest.MapFile{Data: []byte("export const x = 1;")},
	}

	srv, err := New(Config{Addr: "127.0.0.1:0", Token: testToken, Assets: assets}, registry, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, registry
}

func do(t *testing.T, srv *Server, req *http.Request) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

// newRequest builds a request with a loopback Host, since httptest defaults to
// example.com which the rebinding guard correctly rejects.
func newRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "127.0.0.1:8787"
	return req
}

func authedRequest(method, target string) *http.Request {
	req := newRequest(method, target)
	req.Header.Set("X-Ranger-Token", testToken)
	return req
}

func TestRejectsNonLoopbackWithoutOptIn(t *testing.T) {
	registry := core.NewRegistry(core.Config{}, core.NewBus())
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("x")}}

	_, err := New(Config{Addr: "0.0.0.0:8787", Assets: assets}, registry, nil, nil)
	if err == nil {
		t.Fatal("expected refusal to bind a non-loopback address without AllowRemote")
	}
	if !strings.Contains(err.Error(), "allow-remote") {
		t.Errorf("error should name the opt-in flag, got: %v", err)
	}
}

func TestSnapshotRequiresToken(t *testing.T) {
	srv, _ := newTestServer(t)

	resp := do(t, srv, newRequest(http.MethodGet, "/api/snapshot"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing token gave %d, want 401", resp.StatusCode)
	}

	bad := newRequest(http.MethodGet, "/api/snapshot")
	bad.Header.Set("X-Ranger-Token", "wrong-token-value")
	if resp := do(t, srv, bad); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token gave %d, want 401", resp.StatusCode)
	}

	if resp := do(t, srv, authedRequest(http.MethodGet, "/api/snapshot")); resp.StatusCode != http.StatusOK {
		t.Errorf("valid token gave %d, want 200", resp.StatusCode)
	}
}

func TestRejectsUnexpectedHost(t *testing.T) {
	srv, _ := newTestServer(t)

	req := authedRequest(http.MethodGet, "/api/snapshot")
	req.Host = "attacker.example.com"

	if resp := do(t, srv, req); resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("rebinding host gave %d, want 421", resp.StatusCode)
	}
}

func TestRejectsCrossOrigin(t *testing.T) {
	srv, _ := newTestServer(t)

	cases := []struct {
		name   string
		header string
		value  string
	}{
		{"foreign origin", "Origin", "http://evil.example"},
		{"cross-site fetch", "Sec-Fetch-Site", "cross-site"},
		{"same-site fetch", "Sec-Fetch-Site", "same-site"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := authedRequest(http.MethodGet, "/api/snapshot")
			req.Header.Set(c.header, c.value)
			if resp := do(t, srv, req); resp.StatusCode != http.StatusForbidden {
				t.Errorf("got %d, want 403", resp.StatusCode)
			}
		})
	}
}

func TestSameOriginIsAllowed(t *testing.T) {
	srv, _ := newTestServer(t)

	req := authedRequest(http.MethodGet, "/api/snapshot")
	req.Header.Set("Origin", "http://"+req.Host)
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	if resp := do(t, srv, req); resp.StatusCode != http.StatusOK {
		t.Errorf("same-origin request gave %d, want 200", resp.StatusCode)
	}
}

func TestTokenInQueryBecomesCookie(t *testing.T) {
	srv, _ := newTestServer(t)

	resp := do(t, srv, newRequest(http.MethodGet, "/?token="+testToken))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("bootstrap gave %d, want 303", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); strings.Contains(location, "token") {
		t.Errorf("redirect target still carries the token: %q", location)
	}

	var found *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == TokenCookie {
			found = c
		}
	}
	if found == nil {
		t.Fatal("no session cookie was set")
	}
	if !found.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if found.SameSite != http.SameSiteStrictMode {
		t.Error("session cookie must be SameSite=Strict")
	}

	// The cookie alone must now authenticate.
	req := newRequest(http.MethodGet, "/api/snapshot")
	req.AddCookie(found)
	if resp := do(t, srv, req); resp.StatusCode != http.StatusOK {
		t.Errorf("cookie auth gave %d, want 200", resp.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := do(t, srv, authedRequest(http.MethodGet, "/"))

	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q, got %q", want, csp)
		}
	}
	// Inline scripts must stay forbidden; the dashboard relies on separate files.
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP must not relax script execution: %q", csp)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestSnapshotReportsDevices(t *testing.T) {
	srv, registry := newTestServer(t)
	registry.Observe(core.Observation{
		Kind: core.KindWiFiAP, Addr: "aa:bb:cc:dd:ee:ff", Name: "Net",
		Frequency: 2437, RSSI: -50, HasRSSI: true, Source: "test",
	})

	resp := do(t, srv, authedRequest(http.MethodGet, "/api/snapshot"))
	var payload snapshotResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(payload.Devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(payload.Devices))
	}
	if payload.Devices[0].Channel != 6 {
		t.Errorf("Channel = %d, want 6", payload.Devices[0].Channel)
	}
}

func TestStreamSendsSnapshotThenUpdates(t *testing.T) {
	srv, registry := newTestServer(t)

	// A real server is required here: httptest.ResponseRecorder is not safe to
	// read while a streaming handler is still writing to it.
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Ranger-Token", testToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream gave %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	lines := streamLines(resp)
	waitForLine(t, lines, "event: snapshot")

	registry.Observe(core.Observation{
		Kind: core.KindBLE, Addr: "11:22:33:44:55:66", Name: "Tracker",
		RSSI: -60, HasRSSI: true, Source: "test",
	})
	waitForLine(t, lines, "event: upsert")
}

// streamLines reads an SSE body into a channel so the test can apply a timeout.
func streamLines(resp *http.Response) <-chan string {
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if trimmed := strings.TrimRight(line, "\r\n"); trimmed != "" {
				lines <- trimmed
			}
			if err != nil {
				return
			}
		}
	}()
	return lines
}

func waitForLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatalf("stream closed before %q arrived", want)
			}
			if line == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestAssetsAreServedAndPathTraversalBlocked(t *testing.T) {
	srv, _ := newTestServer(t)

	resp := do(t, srv, authedRequest(http.MethodGet, "/app.js"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("app.js gave %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript", got)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "export const x") {
		t.Errorf("unexpected asset body: %q", body)
	}

	// Traversal attempts must never yield file content. The mux normalizes
	// "/../x" to "/x" with a redirect, and the asset lookup then rejects it,
	// so accept either a redirect that stays on-site or a 404.
	for _, target := range []string{"/../go.mod", "/../../etc/passwd", "/go.mod", "/does-not-exist"} {
		resp := do(t, srv, authedRequest(http.MethodGet, target))
		switch resp.StatusCode {
		case http.StatusNotFound:
		case http.StatusMovedPermanently, http.StatusTemporaryRedirect:
			if location := resp.Header.Get("Location"); !strings.HasPrefix(location, "/") {
				t.Errorf("%s redirected off-site to %q", target, location)
			}
		default:
			t.Errorf("%s gave %d; traversal must not serve content", target, resp.StatusCode)
		}
	}
}

// A live SSE stream must not block shutdown. Handlers only return when their
// request context ends, so the server has to release them explicitly.
func TestShutdownCompletesWithActiveStream(t *testing.T) {
	registry := core.NewRegistry(core.Config{}, core.NewBus())
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("x")}}

	srv, err := New(Config{Addr: "127.0.0.1:0", Token: testToken, Assets: assets}, registry, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe(ctx) }()

	var addr string
	for i := 0; i < 200 && addr == ""; i++ {
		addr = srv.BoundAddr()
		if addr == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if addr == "" {
		t.Fatal("server never bound a port")
	}

	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Ranger-Token", testToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatalf("stream produced nothing: %v", err)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("shutdown returned %v, want nil", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("shutdown hung with an active stream")
	}
}

func TestGenerateTokenIsRandom(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	b, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if a == b {
		t.Fatal("tokens must not repeat")
	}
	if len(a) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(a))
	}
}
