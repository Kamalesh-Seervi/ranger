package core

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/kd14/ranger/internal/classify"
)

// Config tunes registry retention and event volume.
type Config struct {
	// TTL is how long a device survives without being seen again.
	TTL time.Duration
	// HistorySize is the number of signal samples retained per device.
	HistorySize int
	// MaxDevices bounds memory. When exceeded the least recently seen device
	// is evicted, which matters for monitor mode where MAC addresses are
	// effectively unbounded.
	MaxDevices int
	// PublishInterval throttles repeat upsert events for a device that is
	// being re-observed rapidly. Identity changes always publish immediately.
	PublishInterval time.Duration
	// SweepInterval is how often expired devices are reaped.
	SweepInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.TTL <= 0 {
		c.TTL = 90 * time.Second
	}
	if c.HistorySize <= 0 {
		c.HistorySize = 120
	}
	if c.MaxDevices <= 0 {
		c.MaxDevices = 4096
	}
	if c.PublishInterval < 0 {
		c.PublishInterval = 0
	}
	if c.PublishInterval == 0 {
		c.PublishInterval = 500 * time.Millisecond
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = 5 * time.Second
	}
	return c
}

type record struct {
	dev          Device
	history      *ring
	sources      map[string]struct{}
	lastPublish  time.Time
	observations int
}

// snapshot returns a deep copy safe to hand outside the registry lock.
func (rec *record) snapshot() Device {
	d := rec.dev
	d.History = rec.history.samples()
	d.Sources = sortedKeys(rec.sources)
	d.Meta = copyMeta(rec.dev.Meta)
	d.Evidence = copyStrings(rec.dev.Evidence)
	return d
}

// Registry is the merged, concurrency-safe view of every device seen so far.
type Registry struct {
	cfg Config
	bus *Bus

	mu      sync.RWMutex
	devices map[string]*record

	// now is injectable so tests can control expiry without sleeping.
	now func() time.Time
}

func NewRegistry(cfg Config, bus *Bus) *Registry {
	if bus == nil {
		bus = NewBus()
	}
	return &Registry{
		cfg:     cfg.withDefaults(),
		bus:     bus,
		devices: make(map[string]*record),
		now:     time.Now,
	}
}

// Bus exposes the event bus so the HTTP layer can subscribe.
func (r *Registry) Bus() *Bus { return r.bus }

// Observe merges a sighting into the registry, publishing an upsert when the
// device is new, its identity changed, or the publish interval has elapsed.
func (r *Registry) Observe(obs Observation) {
	if obs.Kind == "" {
		return
	}
	if obs.Addr == "" && obs.Key == "" {
		return
	}
	if obs.Seen.IsZero() {
		obs.Seen = r.now()
	}

	id := obs.ID()

	r.mu.Lock()
	rec, existed := r.devices[id]
	if !existed {
		r.evictIfFullLocked()
		rec = &record{
			dev: Device{
				ID:        id,
				Kind:      obs.Kind,
				Addr:      obs.Addr,
				FirstSeen: obs.Seen,
			},
			history: newRing(r.cfg.HistorySize),
			sources: make(map[string]struct{}),
		}
		r.devices[id] = rec
	}

	changed := r.mergeLocked(rec, obs)

	// Classification is stable between sightings, so it is recomputed on
	// identity changes and then only occasionally, to keep monitor mode cheap.
	rec.observations++
	if !existed || changed || rec.observations%32 == 0 {
		r.classifyLocked(rec)
	}

	snapshot := rec.snapshot()

	shouldPublish := !existed || changed ||
		obs.Seen.Sub(rec.lastPublish) >= r.cfg.PublishInterval
	if shouldPublish {
		rec.lastPublish = obs.Seen
	}
	r.mu.Unlock()

	if shouldPublish {
		r.bus.Publish(Event{Type: EventUpsert, Device: &snapshot})
	}
}

// mergeLocked folds an observation into a record and reports whether any
// identity-bearing field changed. Callers must hold r.mu.
func (r *Registry) mergeLocked(rec *record, obs Observation) bool {
	d := &rec.dev
	changed := false

	// Identity fields use non-empty-wins: a probe-request sighting carries no
	// SSID, and must not erase the SSID a beacon scan already supplied.
	if obs.Name != "" && obs.Name != d.Name {
		d.Name = obs.Name
		changed = true
	}
	if obs.Vendor != "" && obs.Vendor != d.Vendor {
		d.Vendor = obs.Vendor
		changed = true
	}
	if obs.Security != "" && obs.Security != d.Security {
		d.Security = obs.Security
		changed = true
	}
	if obs.Addr != "" && obs.Addr != d.Addr {
		d.Addr = obs.Addr
		changed = true
	}
	if obs.Frequency > 0 && obs.Frequency != d.Frequency {
		d.Frequency = obs.Frequency
		d.Channel = ChannelForFrequency(obs.Frequency)
		d.Band = BandForFrequency(obs.Frequency)
		changed = true
	}
	if obs.ChannelWidth > 0 && obs.ChannelWidth != d.ChannelWidth {
		d.ChannelWidth = obs.ChannelWidth
		changed = true
	}
	if d.Band == "" {
		switch obs.Kind {
		case KindBLE:
			d.Band = BandBLE
		case KindService:
			d.Band = BandIP
		default:
			d.Band = BandUnknown
		}
	}

	if obs.Source != "" {
		if _, ok := rec.sources[obs.Source]; !ok {
			rec.sources[obs.Source] = struct{}{}
			changed = true
		}
	}
	for k, v := range obs.Meta {
		if d.Meta == nil {
			d.Meta = make(map[string]string, len(obs.Meta))
		}
		if d.Meta[k] != v {
			d.Meta[k] = v
			changed = true
		}
	}

	if obs.HasRSSI {
		d.RSSI = obs.RSSI
		d.HasRSSI = true
		rec.history.add(Sample{At: obs.Seen, RSSI: obs.RSSI})
	}
	if obs.Noise != 0 {
		d.Noise = obs.Noise
	}
	if obs.Seen.After(d.LastSeen) {
		d.LastSeen = obs.Seen
	}
	return changed
}

// classifyLocked infers what the device physically is from everything merged
// so far. Callers must hold r.mu.
func (r *Registry) classifyLocked(rec *record) {
	samples := rec.history.samples()
	values := make([]int, len(samples))
	for i, s := range samples {
		values[i] = s.RSSI
	}

	result := classify.Classify(classify.Signals{
		Kind:       string(rec.dev.Kind),
		Name:       rec.dev.Name,
		Vendor:     rec.dev.Vendor,
		Security:   rec.dev.Security,
		Meta:       rec.dev.Meta,
		RSSISpread: classify.Spread(values),
		Samples:    len(values),
	})

	rec.dev.Category = string(result.Category)
	rec.dev.CategoryConfidence = result.Confidence
	rec.dev.Mobility = string(result.Mobility)
	rec.dev.Evidence = result.Evidence
}

// evictIfFullLocked drops the least recently seen device when at capacity.
func (r *Registry) evictIfFullLocked() {
	if len(r.devices) < r.cfg.MaxDevices {
		return
	}
	var oldestID string
	var oldest time.Time
	for id, rec := range r.devices {
		if oldestID == "" || rec.dev.LastSeen.Before(oldest) {
			oldestID, oldest = id, rec.dev.LastSeen
		}
	}
	if oldestID != "" {
		delete(r.devices, oldestID)
		r.bus.Publish(Event{Type: EventExpire, ID: oldestID})
	}
}

// Snapshot returns every live device, newest sighting first.
func (r *Registry) Snapshot() []Device {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Device, 0, len(r.devices))
	for _, rec := range r.devices {
		out = append(out, rec.snapshot())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out
}

// Get returns a single device by ID.
func (r *Registry) Get(id string) (Device, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.devices[id]
	if !ok {
		return Device{}, false
	}
	return rec.snapshot(), true
}

// Expire removes devices not seen within the TTL and returns how many went.
func (r *Registry) Expire() int {
	cutoff := r.now().Add(-r.cfg.TTL)

	r.mu.Lock()
	var gone []string
	for id, rec := range r.devices {
		if rec.dev.LastSeen.Before(cutoff) {
			delete(r.devices, id)
			gone = append(gone, id)
		}
	}
	r.mu.Unlock()

	for _, id := range gone {
		r.bus.Publish(Event{Type: EventExpire, ID: id})
	}
	return len(gone)
}

// Run sweeps expired devices until the context is cancelled.
func (r *Registry) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Expire()
		}
	}
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func copyMeta(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
