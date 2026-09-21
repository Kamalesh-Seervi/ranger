package insight

import (
	"sync"
	"time"

	"github.com/kd14/ranger/internal/core"
)

// Alert reports a device that has appeared alongside you in several distinct
// places, which is the signature of something travelling with you.
type Alert struct {
	DeviceID  string    `json:"deviceId"`
	Name      string    `json:"name"`
	Vendor    string    `json:"vendor"`
	Category  string    `json:"category"`
	Places    []string  `json:"places"`
	RSSI      int       `json:"rssi"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// FollowConfig tunes what counts as being followed.
type FollowConfig struct {
	// MinPlaces is how many distinct locations a device must appear in.
	MinPlaces int
	// MinDuration is how long it must have been following before alerting.
	MinDuration time.Duration
	// MinRSSI ignores devices too far away to actually be travelling with you.
	MinRSSI int
	// Forget drops devices not seen for this long.
	Forget time.Duration
	// MaxTracked bounds memory.
	MaxTracked int
}

func (c FollowConfig) withDefaults() FollowConfig {
	if c.MinPlaces <= 0 {
		c.MinPlaces = 3
	}
	if c.MinDuration <= 0 {
		c.MinDuration = 10 * time.Minute
	}
	if c.MinRSSI == 0 {
		c.MinRSSI = -85
	}
	if c.Forget <= 0 {
		c.Forget = 6 * time.Hour
	}
	if c.MaxTracked <= 0 {
		c.MaxTracked = 2048
	}
	return c
}

type followRecord struct {
	name      string
	vendor    string
	category  string
	rssi      int
	places    map[string]string
	firstSeen time.Time
	lastSeen  time.Time
}

// follower watches which devices reappear across different places.
//
// Bluetooth addresses usually rotate, which normally defeats this. The case
// that matters most is the exception: a tracker separated from its owner stops
// rotating and keeps one address for many hours, so the tag that is genuinely
// following a stranger is exactly the one this can see.
type follower struct {
	cfg FollowConfig

	mu      sync.Mutex
	records map[string]*followRecord
}

func newFollower(cfg FollowConfig) *follower {
	return &follower{
		cfg:     cfg.withDefaults(),
		records: make(map[string]*followRecord),
	}
}

// observe records which portable devices were present at the current place.
func (f *follower) observe(devices []core.Device, placeID, placeName string, now time.Time) {
	if placeID == "" {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, d := range devices {
		if !f.qualifies(d) {
			continue
		}

		record, ok := f.records[d.ID]
		if !ok {
			if len(f.records) >= f.cfg.MaxTracked {
				continue
			}
			record = &followRecord{places: make(map[string]string, 2), firstSeen: now}
			f.records[d.ID] = record
		}

		record.places[placeID] = placeName
		record.lastSeen = now
		record.rssi = d.RSSI
		record.category = d.Category
		if d.Name != "" {
			record.name = d.Name
		}
		if d.Vendor != "" {
			record.vendor = d.Vendor
		}
	}

	f.pruneLocked(now)
}

// qualifies keeps only nearby, carryable Bluetooth devices. Access points and
// network services are fixtures of a place, not things that follow anyone.
func (f *follower) qualifies(d core.Device) bool {
	if d.Kind != core.KindBLE || !d.HasRSSI || d.RSSI < f.cfg.MinRSSI {
		return false
	}
	switch d.Category {
	case "router", "printer", "tv", "smart-home", "console", "camera":
		return false
	default:
		return true
	}
}

func (f *follower) pruneLocked(now time.Time) {
	cutoff := now.Add(-f.cfg.Forget)
	for id, record := range f.records {
		if record.lastSeen.Before(cutoff) {
			delete(f.records, id)
		}
	}
}

// alerts returns every device that has followed you for long enough, across
// enough places, to be worth flagging.
func (f *follower) alerts() []Alert {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []Alert
	for id, record := range f.records {
		if len(record.places) < f.cfg.MinPlaces {
			continue
		}
		if record.lastSeen.Sub(record.firstSeen) < f.cfg.MinDuration {
			continue
		}

		places := make([]string, 0, len(record.places))
		for _, name := range record.places {
			places = append(places, name)
		}
		sortStrings(places)

		out = append(out, Alert{
			DeviceID:  id,
			Name:      record.name,
			Vendor:    record.vendor,
			Category:  record.category,
			Places:    places,
			RSSI:      record.rssi,
			FirstSeen: record.firstSeen,
			LastSeen:  record.lastSeen,
		})
	}

	// Most places first: the more locations, the stronger the signal.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j].Places) > len(out[j-1].Places); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
