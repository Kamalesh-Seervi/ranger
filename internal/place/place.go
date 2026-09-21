// Package place recognises where you are from the radio environment alone.
//
// The set of nearby access points, together with how strongly each is heard,
// is effectively a signature for a location. Comparing the live signature
// against ones seen before identifies a place without GPS, a map, or any
// training data: places are discovered by clustering as you move around.
package place

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Observation is one anchor candidate drawn from the device registry.
type Observation struct {
	ID    string
	Label string
	RSSI  int
	// Stable marks devices that do not move. Including a phone would make the
	// signature follow its owner from room to room, so only stable devices
	// are ever used as anchors.
	Stable bool
}

// Fingerprint is the radio signature of a single moment.
type Fingerprint struct {
	Anchors map[string]int
	Labels  map[string]string
}

// Place is one learned location.
type Place struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Anchors   map[string]int    `json:"anchors"`
	Hits      map[string]int    `json:"hits"`
	Labels    map[string]string `json:"labels,omitempty"`
	Samples   int               `json:"samples"`
	FirstSeen time.Time         `json:"firstSeen"`
	LastSeen  time.Time         `json:"lastSeen"`
	Dwell     time.Duration     `json:"dwellNs"`
}

// Candidate is a runner-up place and how well it matched.
type Candidate struct {
	PlaceID string  `json:"placeId"`
	Name    string  `json:"name"`
	Score   float64 `json:"score"`
}

// Match is the outcome of one fingerprint update.
type Match struct {
	PlaceID    string      `json:"placeId"`
	Name       string      `json:"name"`
	Score      float64     `json:"score"`
	IsNew      bool        `json:"isNew"`
	Changed    bool        `json:"changed"`
	Anchors    int         `json:"anchors"`
	Candidates []Candidate `json:"candidates,omitempty"`
}

// Config tunes recognition sensitivity.
type Config struct {
	// Threshold is the similarity above which a fingerprint is considered the
	// same place. Lower merges neighbouring rooms; higher invents new places
	// every time someone shuts a door.
	Threshold float64
	// SwitchMargin is how much better a rival must score before we accept
	// having moved. Without it the current place flaps between two similar
	// signatures.
	SwitchMargin float64
	// MinAnchors is the fewest anchors needed to judge a location at all.
	MinAnchors int
	// MinRSSI drops anchors too weak to be reliable.
	MinRSSI int
	// MaxPlaces bounds the learned set.
	MaxPlaces int
	// Path is where places persist. Empty disables saving.
	Path string
}

func (c Config) withDefaults() Config {
	if c.Threshold <= 0 {
		c.Threshold = 0.62
	}
	if c.SwitchMargin <= 0 {
		c.SwitchMargin = 0.08
	}
	if c.MinAnchors <= 0 {
		c.MinAnchors = 3
	}
	if c.MinRSSI == 0 {
		c.MinRSSI = -92
	}
	if c.MaxPlaces <= 0 {
		c.MaxPlaces = 200
	}
	return c
}

// anchorWeightCap limits how fast an established place adapts, so one odd
// reading cannot drag a well-learned signature away.
const anchorWeightCap = 30

// Engine learns and recognises places.
type Engine struct {
	cfg Config

	mu         sync.Mutex
	places     map[string]*Place
	currentID  string
	lastUpdate time.Time
	dirty      bool
}

func NewEngine(cfg Config) *Engine {
	return &Engine{
		cfg:    cfg.withDefaults(),
		places: make(map[string]*Place),
	}
}

// Build turns registry observations into a fingerprint, keeping only anchors
// that are stable and strong enough to be trusted.
func Build(observations []Observation, minRSSI int) Fingerprint {
	fp := Fingerprint{
		Anchors: make(map[string]int, len(observations)),
		Labels:  make(map[string]string, len(observations)),
	}
	for _, obs := range observations {
		if !obs.Stable || obs.RSSI < minRSSI || obs.ID == "" {
			continue
		}
		fp.Anchors[obs.ID] = obs.RSSI
		if obs.Label != "" {
			fp.Labels[obs.ID] = obs.Label
		}
	}
	return fp
}

// MinRSSI exposes the configured signal floor so callers can build matching
// fingerprints.
func (e *Engine) MinRSSI() int { return e.cfg.MinRSSI }

// MinAnchors is the fewest anchors needed before a location can be judged.
func (e *Engine) MinAnchors() int { return e.cfg.MinAnchors }

// Similarity scores two signatures between 0 and 1.
//
// Anchor overlap is blended two ways before being scaled by how closely the
// shared anchors agree on signal strength. Dice alone punishes a size
// mismatch, which wrongly rejects a known place seen through a partial scan;
// the overlap coefficient alone would call any small signature a match for
// everything. Averaging them tolerates partial views without being credulous,
// and signal agreement is what ultimately separates two rooms that hear the
// same routers.
func Similarity(a, b map[string]int) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	shared := 0
	agreement := 0.0
	for id, left := range a {
		right, ok := b[id]
		if !ok {
			continue
		}
		shared++
		// 20 dB apart counts as total disagreement.
		agreement += math.Max(0, 1-math.Abs(float64(left-right))/20)
	}
	if shared == 0 {
		return 0
	}

	smaller := len(a)
	if len(b) < smaller {
		smaller = len(b)
	}
	dice := 2 * float64(shared) / float64(len(a)+len(b))
	containment := float64(shared) / float64(smaller)

	return (dice + containment) / 2 * (agreement / float64(shared))
}

// Update matches a fingerprint against the learned set, creating a new place
// when nothing is close enough. It reports false when the fingerprint is too
// sparse to judge.
func (e *Engine) Update(fp Fingerprint, now time.Time) (Match, bool) {
	if len(fp.Anchors) < e.cfg.MinAnchors {
		return Match{}, false
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.accrueDwellLocked(now)

	scored := e.scoreAllLocked(fp)
	bestID, bestScore := "", 0.0
	if len(scored) > 0 {
		bestID, bestScore = scored[0].PlaceID, scored[0].Score
	}

	// Staying put is the default. Only switch when a rival is clearly better,
	// otherwise a doorway makes the place oscillate.
	if e.currentID != "" && bestID != e.currentID {
		if current, ok := e.places[e.currentID]; ok {
			currentScore := Similarity(fp.Anchors, current.Anchors)
			if currentScore >= e.cfg.Threshold && bestScore-currentScore < e.cfg.SwitchMargin {
				bestID, bestScore = e.currentID, currentScore
			}
		}
	}

	isNew := false
	if bestID == "" || bestScore < e.cfg.Threshold {
		bestID = e.createLocked(fp, now)
		bestScore = 1
		isNew = true
	}

	changed := bestID != e.currentID
	e.currentID = bestID
	e.lastUpdate = now
	e.dirty = true

	target := e.places[bestID]
	if !isNew {
		target.absorb(fp, now)
	}

	candidates := scored
	if len(candidates) > 4 {
		candidates = candidates[:4]
	}

	return Match{
		PlaceID:    bestID,
		Name:       target.Name,
		Score:      round2(bestScore),
		IsNew:      isNew,
		Changed:    changed,
		Anchors:    len(fp.Anchors),
		Candidates: candidates,
	}, true
}

// scoreAllLocked ranks every known place against a fingerprint.
func (e *Engine) scoreAllLocked(fp Fingerprint) []Candidate {
	out := make([]Candidate, 0, len(e.places))
	for id, p := range e.places {
		score := Similarity(fp.Anchors, p.Anchors)
		if score <= 0 {
			continue
		}
		out = append(out, Candidate{PlaceID: id, Name: p.Name, Score: round2(score)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].PlaceID < out[j].PlaceID
	})
	return out
}

// accrueDwellLocked credits time spent in the current place.
func (e *Engine) accrueDwellLocked(now time.Time) {
	if e.currentID == "" || e.lastUpdate.IsZero() {
		return
	}
	current, ok := e.places[e.currentID]
	if !ok {
		return
	}
	elapsed := now.Sub(e.lastUpdate)
	// A long gap means ranger was asleep, not that you stood still for hours.
	if elapsed > 0 && elapsed < 5*time.Minute {
		current.Dwell += elapsed
	}
}

func (e *Engine) createLocked(fp Fingerprint, now time.Time) string {
	e.evictIfFullLocked()

	p := &Place{
		ID:        newID(),
		Name:      fmt.Sprintf("Place %d", len(e.places)+1),
		Anchors:   make(map[string]int, len(fp.Anchors)),
		Hits:      make(map[string]int, len(fp.Anchors)),
		Labels:    make(map[string]string, len(fp.Labels)),
		Samples:   1,
		FirstSeen: now,
		LastSeen:  now,
	}
	for id, rssi := range fp.Anchors {
		p.Anchors[id] = rssi
		p.Hits[id] = 1
	}
	for id, label := range fp.Labels {
		p.Labels[id] = label
	}
	e.places[p.ID] = p
	return p.ID
}

// absorb folds a new sighting into a place's running signature.
func (p *Place) absorb(fp Fingerprint, now time.Time) {
	weight := p.Samples
	if weight > anchorWeightCap {
		weight = anchorWeightCap
	}

	for id, rssi := range fp.Anchors {
		if existing, ok := p.Anchors[id]; ok {
			p.Anchors[id] = (existing*weight + rssi) / (weight + 1)
		} else {
			p.Anchors[id] = rssi
		}
		p.Hits[id]++
	}
	for id, label := range fp.Labels {
		p.Labels[id] = label
	}

	p.Samples++
	p.LastSeen = now
	p.prune()
}

// prune drops anchors that rarely show up, which is how a signature recovers
// when a neighbour replaces their router.
func (p *Place) prune() {
	if p.Samples < 20 || p.Samples%10 != 0 {
		return
	}
	floor := p.Samples / 4
	for id, hits := range p.Hits {
		if hits < floor {
			delete(p.Hits, id)
			delete(p.Anchors, id)
			delete(p.Labels, id)
		}
	}
}

func (e *Engine) evictIfFullLocked() {
	if len(e.places) < e.cfg.MaxPlaces {
		return
	}
	oldestID := ""
	var oldest time.Time
	for id, p := range e.places {
		if id == e.currentID {
			continue
		}
		if oldestID == "" || p.LastSeen.Before(oldest) {
			oldestID, oldest = id, p.LastSeen
		}
	}
	if oldestID != "" {
		delete(e.places, oldestID)
	}
}

// Places returns every learned place, most recently visited first.
func (e *Engine) Places() []Place {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]Place, 0, len(e.places))
	for _, p := range e.places {
		out = append(out, *p.clone())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out
}

// Current returns the place we believe we are in.
func (e *Engine) Current() (Place, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	p, ok := e.places[e.currentID]
	if !ok {
		return Place{}, false
	}
	return *p.clone(), true
}

// Rename gives a learned place a human name.
func (e *Engine) Rename(id, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	p, ok := e.places[id]
	if !ok {
		return errors.New("place: unknown id")
	}
	p.Name = name
	e.dirty = true
	return nil
}

// Forget deletes a learned place.
func (e *Engine) Forget(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, ok := e.places[id]; !ok {
		return errors.New("place: unknown id")
	}
	delete(e.places, id)
	if e.currentID == id {
		e.currentID = ""
	}
	e.dirty = true
	return nil
}

func (p *Place) clone() *Place {
	out := *p
	out.Anchors = make(map[string]int, len(p.Anchors))
	for k, v := range p.Anchors {
		out.Anchors[k] = v
	}
	out.Hits = make(map[string]int, len(p.Hits))
	for k, v := range p.Hits {
		out.Hits[k] = v
	}
	out.Labels = make(map[string]string, len(p.Labels))
	for k, v := range p.Labels {
		out.Labels[k] = v
	}
	return &out
}

type persisted struct {
	Version int      `json:"version"`
	Places  []*Place `json:"places"`
}

// Load reads previously learned places. A missing file is not an error.
func (e *Engine) Load() error {
	if e.cfg.Path == "" {
		return nil
	}
	data, err := os.ReadFile(e.cfg.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("place: read %s: %w", e.cfg.Path, err)
	}

	var file persisted
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("place: parse %s: %w", e.cfg.Path, err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range file.Places {
		if p == nil || p.ID == "" {
			continue
		}
		if p.Anchors == nil {
			p.Anchors = map[string]int{}
		}
		if p.Hits == nil {
			p.Hits = map[string]int{}
		}
		if p.Labels == nil {
			p.Labels = map[string]string{}
		}
		e.places[p.ID] = p
	}
	return nil
}

// Save writes learned places, atomically so a crash cannot truncate the file.
func (e *Engine) Save() error {
	if e.cfg.Path == "" {
		return nil
	}

	e.mu.Lock()
	if !e.dirty {
		e.mu.Unlock()
		return nil
	}
	file := persisted{Version: 1, Places: make([]*Place, 0, len(e.places))}
	for _, p := range e.places {
		file.Places = append(file.Places, p.clone())
	}
	e.dirty = false
	e.mu.Unlock()

	sort.Slice(file.Places, func(i, j int) bool { return file.Places[i].ID < file.Places[j].ID })

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("place: encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(e.cfg.Path), 0o755); err != nil {
		return fmt.Errorf("place: create directory: %w", err)
	}

	temp := e.cfg.Path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return fmt.Errorf("place: write: %w", err)
	}
	if err := os.Rename(temp, e.cfg.Path); err != nil {
		return fmt.Errorf("place: replace: %w", err)
	}
	return nil
}

// DefaultPath is where places live when no override is given.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ranger", "places.json")
}

func newID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("p%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
