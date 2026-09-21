package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/kd14/ranger/internal/core"
	"github.com/kd14/ranger/internal/csi"
	"github.com/kd14/ranger/internal/insight"
	"github.com/kd14/ranger/internal/scan"
)

// snapshotResponse is the initial state a client needs to render everything.
type snapshotResponse struct {
	Devices  []core.Device  `json:"devices"`
	Scanners []scan.Status  `json:"scanners"`
	Insight  *insight.State `json:"insight,omitempty"`
	CSI      *csi.State     `json:"csi,omitempty"`
	Server   serverInfo     `json:"server"`
}

type serverInfo struct {
	StartedAt time.Time `json:"startedAt"`
	Now       time.Time `json:"now"`
}

// snapshot assembles the full current state, shared by the REST endpoint and
// the first frame of the event stream.
func (s *Server) snapshot() snapshotResponse {
	resp := snapshotResponse{
		Devices: s.reg.Snapshot(),
		Server:  serverInfo{StartedAt: s.startedAt, Now: time.Now()},
	}
	if s.mgr != nil {
		resp.Scanners = s.mgr.Statuses()
	}
	if s.places != nil {
		state := s.places.State()
		resp.Insight = &state
	}
	if s.csi != nil {
		state := s.csi.State()
		resp.CSI = &state
	}
	return resp
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshot())
}

// maxBodyBytes bounds request bodies; every endpoint here takes a tiny object.
const maxBodyBytes = 4 << 10

type placeEditRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) handlePlaceRename(w http.ResponseWriter, r *http.Request) {
	if s.places == nil {
		http.Error(w, "place recognition is disabled", http.StatusServiceUnavailable)
		return
	}
	req, ok := decodePlaceEdit(w, r)
	if !ok {
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(name) > 64 {
		name = name[:64]
	}
	// Place names are rendered in the browser, so strip anything that is not
	// printable before it is stored.
	name = sanitizeLabel(name)

	if err := s.places.Rename(req.ID, name); err != nil {
		http.Error(w, "unknown place", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.places.State())
}

func (s *Server) handlePlaceForget(w http.ResponseWriter, r *http.Request) {
	if s.places == nil {
		http.Error(w, "place recognition is disabled", http.StatusServiceUnavailable)
		return
	}
	req, ok := decodePlaceEdit(w, r)
	if !ok {
		return
	}
	if err := s.places.Forget(req.ID); err != nil {
		http.Error(w, "unknown place", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.places.State())
}

func decodePlaceEdit(w http.ResponseWriter, r *http.Request) (placeEditRequest, bool) {
	var req placeEditRequest
	body := io.LimitReader(r.Body, maxBodyBytes)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return req, false
	}
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// sanitizeLabel removes control characters from user-supplied text.
func sanitizeLabel(in string) string {
	var b strings.Builder
	b.Grow(len(in))
	for _, r := range in {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// handleStream pushes registry events to the browser over Server-Sent Events.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// Disable proxy buffering, which would otherwise hold events back.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	events, unsubscribe := s.reg.Bus().Subscribe(512)
	defer unsubscribe()

	// Send current state first so a reconnecting client is immediately whole.
	if err := writeSSE(w, "snapshot", s.snapshot()); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-events:
			if !open {
				return
			}
			if err := writeSSE(w, ev.Type, ev); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			// A comment frame keeps intermediaries from closing the stream.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleAssets serves the dashboard, defaulting unknown paths to index.html.
func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}

	data, err := readAsset(s.cfg.Assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", contentType(name))
	// The dashboard is served from memory and changes with the binary, so
	// revalidating every load keeps development honest.
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(data); err != nil {
		s.log.Debug("write asset", "name", name, "err", err)
	}
}

// readAsset reads a dashboard file, rejecting paths that escape the asset root.
func readAsset(fsys fs.FS, name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, fs.ErrNotExist
	}
	return fs.ReadFile(fsys, name)
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

// writeSSE emits one event frame. JSON is guaranteed newline-free by the
// encoder, so a single data line is always sufficient.
func writeSSE(w http.ResponseWriter, event string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
	return err
}
