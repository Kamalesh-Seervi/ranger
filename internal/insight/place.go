// Package insight derives higher-level meaning from the device registry.
package insight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kd14/ranger/internal/classify"
	"github.com/kd14/ranger/internal/core"
	"github.com/kd14/ranger/internal/motion"
	"github.com/kd14/ranger/internal/place"
)

// State is the derived picture handed to the dashboard.
type State struct {
	// Active is false while too few stable anchors are visible to judge.
	Active     bool              `json:"active"`
	PlaceID    string            `json:"placeId,omitempty"`
	Name       string            `json:"name,omitempty"`
	Score      float64           `json:"score"`
	Anchors    int               `json:"anchors"`
	Dwell      time.Duration     `json:"dwellNs"`
	Reason     string            `json:"reason,omitempty"`
	Candidates []place.Candidate `json:"candidates,omitempty"`
	Places     []Summary         `json:"places"`
	Motion     motion.State      `json:"motion"`
	Follows    []Alert           `json:"follows,omitempty"`
}

// Summary is one learned place as the dashboard lists it.
type Summary struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Anchors  int           `json:"anchors"`
	Samples  int           `json:"samples"`
	Dwell    time.Duration `json:"dwellNs"`
	LastSeen time.Time     `json:"lastSeen"`
	Current  bool          `json:"current"`
	Top      []string      `json:"top,omitempty"`
}

// PlaceService samples the registry on a ticker and keeps the place engine fed.
type PlaceService struct {
	registry  *core.Registry
	engine    *place.Engine
	detector  *motion.Detector
	follower  *follower
	log       *slog.Logger
	interval  time.Duration
	startedAt time.Time

	mu    sync.RWMutex
	state State
}

const (
	defaultInterval = 5 * time.Second
	saveInterval    = 60 * time.Second
	// warmup holds off learning until the scanners have filled the registry.
	// Sampling immediately would commit a half-built signature as a place, and
	// the full picture would then fail to match it.
	warmup = 25 * time.Second
)

func NewPlaceService(registry *core.Registry, engine *place.Engine, log *slog.Logger) *PlaceService {
	if log == nil {
		log = slog.Default()
	}
	return &PlaceService{
		registry:  registry,
		engine:    engine,
		detector:  motion.NewDetector(motion.Config{}),
		follower:  newFollower(FollowConfig{}),
		log:       log,
		interval:  defaultInterval,
		startedAt: time.Now(),
		state:     State{Reason: "warming up", Places: []Summary{}},
	}
}

// State returns the current place picture.
func (s *PlaceService) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Run samples until the context is cancelled, then persists what was learned.
func (s *PlaceService) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	saver := time.NewTicker(saveInterval)
	defer saver.Stop()

	for {
		select {
		case <-ctx.Done():
			if err := s.engine.Save(); err != nil {
				s.log.Error("save places", "err", err)
			}
			return
		case <-ticker.C:
			s.sample()
		case <-saver.C:
			if err := s.engine.Save(); err != nil {
				s.log.Error("save places", "err", err)
			}
		}
	}
}

// sample builds one fingerprint and folds it into the engine.
func (s *PlaceService) sample() {
	now := time.Now()
	devices := s.registry.Snapshot()

	// Motion needs no learned places, so it runs from the first sample.
	motionState := s.detector.Observe(s.motionAnchors(devices), now)

	if remaining := warmup - time.Since(s.startedAt); remaining > 0 {
		s.setState(State{
			Active: false,
			Reason: fmt.Sprintf("listening for %ds before learning this place",
				int(remaining.Seconds())+1),
			Places: s.summaries(""),
			Motion: motionState,
		})
		return
	}

	fingerprint := s.fingerprint(devices)

	match, ok := s.engine.Update(fingerprint, now)
	if !ok {
		s.setState(State{
			Active:  false,
			Anchors: len(fingerprint.Anchors),
			Reason: fmt.Sprintf("only %d stable anchors visible; need at least %d",
				len(fingerprint.Anchors), s.engine.MinAnchors()),
			Places: s.summaries(""),
			Motion: motionState,
		})
		return
	}

	if match.Changed {
		s.log.Info("place changed", "place", match.Name, "score", match.Score, "new", match.IsNew)
	}

	s.follower.observe(devices, match.PlaceID, match.Name, now)
	alerts := s.follower.alerts()
	for _, alert := range alerts {
		s.log.Warn("device seen across multiple places",
			"device", alert.DeviceID, "category", alert.Category, "places", len(alert.Places))
	}

	dwell := time.Duration(0)
	if current, found := s.engine.Current(); found {
		dwell = current.Dwell
	}

	s.setState(State{
		Active:     true,
		PlaceID:    match.PlaceID,
		Name:       match.Name,
		Score:      match.Score,
		Anchors:    match.Anchors,
		Dwell:      dwell,
		Motion:     motionState,
		Follows:    alerts,
		Candidates: match.Candidates,
		Places:     s.summaries(match.PlaceID),
	})
}

// minAnchorLifetime keeps a device that has only just appeared out of the
// motion baseline, since its history is too short to mean anything.
const minAnchorLifetime = 30 * time.Second

// motionAnchors collects the signal history of devices that are plausibly
// standing still.
//
// It deliberately does not require classify's "fixed" label. Mobility is
// judged from spread across a device's whole history, and a stationary beacon
// in a room where people walk about shows exactly that spread. Gating on
// "fixed" would therefore discard the very anchors that reveal movement.
// Excluding only the clearly portable ones keeps the useful signal.
func (s *PlaceService) motionAnchors(devices []core.Device) []motion.Anchor {
	anchors := make([]motion.Anchor, 0, len(devices))
	for _, d := range devices {
		if !d.HasRSSI || len(d.History) < 2 {
			continue
		}
		if d.Mobility == string(classify.MobilityPortable) {
			continue
		}
		if d.LastSeen.Sub(d.FirstSeen) < minAnchorLifetime {
			continue
		}

		samples := make([]motion.Sample, 0, len(d.History))
		for _, h := range d.History {
			samples = append(samples, motion.Sample{At: h.At, RSSI: h.RSSI})
		}
		anchors = append(anchors, motion.Anchor{ID: d.ID, Samples: samples})
	}
	return anchors
}

// fingerprint keeps only anchors that do not move. Including a phone would
// make the signature follow its owner from room to room.
func (s *PlaceService) fingerprint(devices []core.Device) place.Fingerprint {
	observations := make([]place.Observation, 0, len(devices))
	for _, d := range devices {
		if !d.HasRSSI {
			continue
		}
		stable := d.Kind == core.KindWiFiAP || d.Mobility == string(classify.MobilityFixed)
		observations = append(observations, place.Observation{
			ID:     d.ID,
			Label:  labelFor(d),
			RSSI:   d.RSSI,
			Stable: stable,
		})
	}
	return place.Build(observations, s.engine.MinRSSI())
}

func labelFor(d core.Device) string {
	if d.Name != "" {
		return d.Name
	}
	if d.Channel > 0 {
		return fmt.Sprintf("ch %d", d.Channel)
	}
	return d.Addr
}

func (s *PlaceService) setState(next State) {
	s.mu.Lock()
	s.state = next
	s.mu.Unlock()

	payload, err := json.Marshal(next)
	if err != nil {
		s.log.Error("encode place state", "err", err)
		return
	}
	s.registry.Bus().Publish(core.Event{Type: core.EventInsight, Data: payload})
}

func (s *PlaceService) summaries(currentID string) []Summary {
	places := s.engine.Places()
	out := make([]Summary, 0, len(places))
	for _, p := range places {
		out = append(out, Summary{
			ID:       p.ID,
			Name:     p.Name,
			Anchors:  len(p.Anchors),
			Samples:  p.Samples,
			Dwell:    p.Dwell,
			LastSeen: p.LastSeen,
			Current:  p.ID == currentID,
			Top:      topAnchors(p, 3),
		})
	}
	return out
}

// topAnchors names the strongest anchors so a place is recognisable before it
// has been given a name.
func topAnchors(p place.Place, limit int) []string {
	type entry struct {
		label string
		rssi  int
	}
	entries := make([]entry, 0, len(p.Anchors))
	for id, rssi := range p.Anchors {
		label := p.Labels[id]
		if label == "" {
			label = id
		}
		entries = append(entries, entry{label: label, rssi: rssi})
	}
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].rssi > entries[j-1].rssi; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}

	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%s (%d dBm)", e.label, e.rssi))
	}
	return out
}

// Rename gives the named place a human label and republishes the state.
func (s *PlaceService) Rename(id, name string) error {
	if err := s.engine.Rename(id, name); err != nil {
		return err
	}
	s.refresh()
	return nil
}

// Forget deletes a learned place and republishes the state.
func (s *PlaceService) Forget(id string) error {
	if err := s.engine.Forget(id); err != nil {
		return err
	}
	s.refresh()
	return nil
}

// refresh republishes the state after an edit, without waiting for the ticker.
func (s *PlaceService) refresh() {
	current := s.State()
	if name, ok := s.currentName(); ok {
		current.Name = name
	}
	current.Places = s.summaries(current.PlaceID)
	s.setState(current)

	if err := s.engine.Save(); err != nil {
		s.log.Error("save places", "err", err)
	}
}

func (s *PlaceService) currentName() (string, bool) {
	p, ok := s.engine.Current()
	if !ok {
		return "", false
	}
	return p.Name, true
}
