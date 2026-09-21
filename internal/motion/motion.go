// Package motion detects movement near the receiver without the moving thing
// carrying a radio.
//
// A body crossing the path between the receiver and a stationary transmitter
// absorbs and reflects the signal, so the reported strength wobbles. Watching
// how much every fixed anchor wobbles, relative to how much it wobbles when
// the room is still, reveals that something moved.
//
// This says only that the radio environment was disturbed. It cannot identify
// what moved, count people, or locate them. Sensitivity depends entirely on
// how often the anchors update: a beacon advertising several times a second is
// useful, a Wi-Fi access point scanned every eight seconds is barely so.
package motion

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Sample is one signal reading from an anchor.
type Sample struct {
	At   time.Time
	RSSI int
}

// Anchor is a stationary transmitter being watched.
type Anchor struct {
	ID      string
	Samples []Sample
}

// State is the current motion picture.
type State struct {
	Active      bool `json:"active"`
	Calibrating bool `json:"calibrating"`
	Detected    bool `json:"detected"`
	// Level is 0..1, reaching 0.5 at the trigger point.
	Level float64 `json:"level"`
	// Disturbance and Baseline are in dB of average signal wobble.
	Disturbance float64   `json:"disturbance"`
	Baseline    float64   `json:"baseline"`
	Anchors     int       `json:"anchors"`
	SampleRate  float64   `json:"sampleRate"`
	Since       time.Time `json:"since,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// Config tunes sensitivity.
type Config struct {
	// Window is how far back samples are considered.
	Window time.Duration
	// MinAnchors is the fewest usable anchors needed to judge the room.
	MinAnchors int
	// MinSamples is the fewest readings an anchor needs to contribute.
	MinSamples int
	// Calibration is how many quiet observations establish the baseline.
	Calibration int
	// TriggerRatio is how many times the baseline counts as movement.
	TriggerRatio float64
	// TriggerFloor is the smallest absolute rise, in dB, that counts. It stops
	// a very quiet baseline from making the detector fire on nothing.
	TriggerFloor float64
	// BaselineAlpha is how fast the quiet baseline adapts.
	BaselineAlpha float64
}

func (c Config) withDefaults() Config {
	if c.Window <= 0 {
		c.Window = 20 * time.Second
	}
	if c.MinAnchors <= 0 {
		c.MinAnchors = 3
	}
	if c.MinSamples <= 0 {
		c.MinSamples = 4
	}
	if c.Calibration <= 0 {
		c.Calibration = 10
	}
	if c.TriggerRatio <= 1 {
		c.TriggerRatio = 2.2
	}
	if c.TriggerFloor <= 0 {
		c.TriggerFloor = 1.0
	}
	if c.BaselineAlpha <= 0 || c.BaselineAlpha >= 1 {
		c.BaselineAlpha = 0.08
	}
	return c
}

// Detector tracks the baseline wobble of a still room and flags departures.
type Detector struct {
	cfg Config

	mu        sync.Mutex
	baseline  float64
	warmups   int
	detecting bool
	since     time.Time
}

func NewDetector(cfg Config) *Detector {
	return &Detector{cfg: cfg.withDefaults()}
}

// Observe folds one round of anchor readings into the detector.
func (d *Detector) Observe(anchors []Anchor, now time.Time) State {
	cutoff := now.Add(-d.cfg.Window)

	deviations := make([]float64, 0, len(anchors))
	readings := 0
	for _, anchor := range anchors {
		windowed := within(anchor.Samples, cutoff)
		if len(windowed) < d.cfg.MinSamples {
			continue
		}
		readings += len(windowed)
		deviations = append(deviations, deviation(windowed))
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if len(deviations) < d.cfg.MinAnchors {
		d.detecting = false
		return State{
			Anchors: len(deviations),
			Reason: "needs at least " + itoa(d.cfg.MinAnchors) +
				" stationary anchors reporting several times per window",
		}
	}

	disturbance := mean(deviations)
	rate := float64(readings) / d.cfg.Window.Seconds()

	if d.warmups < d.cfg.Calibration {
		d.warmups++
		// A plain running mean during calibration, so the very first reading
		// does not dominate the baseline.
		d.baseline += (disturbance - d.baseline) / float64(d.warmups)
		return State{
			Active:      true,
			Calibrating: true,
			Disturbance: round2(disturbance),
			Baseline:    round2(d.baseline),
			Anchors:     len(deviations),
			SampleRate:  round2(rate),
			Reason:      "learning what a still room looks like",
		}
	}

	span := math.Max(d.baseline*(d.cfg.TriggerRatio-1), d.cfg.TriggerFloor)
	riseAbove := d.baseline + span
	// A separate, lower release point stops the flag chattering while someone
	// moves around at the edge of detection.
	fallBelow := d.baseline + span*0.5

	switch {
	case !d.detecting && disturbance > riseAbove:
		d.detecting = true
		d.since = now
	case d.detecting && disturbance < fallBelow:
		d.detecting = false
	}

	// The baseline only learns while the room is quiet, otherwise sustained
	// movement would be absorbed and become the new normal.
	if !d.detecting {
		d.baseline += d.cfg.BaselineAlpha * (disturbance - d.baseline)
	}

	state := State{
		Active:      true,
		Detected:    d.detecting,
		Level:       round2(clamp((disturbance-d.baseline)/(span*2), 0, 1)),
		Disturbance: round2(disturbance),
		Baseline:    round2(d.baseline),
		Anchors:     len(deviations),
		SampleRate:  round2(rate),
	}
	if d.detecting {
		state.Since = d.since
	}
	return state
}

// Reset clears the learned baseline, for when the receiver itself has moved.
func (d *Detector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.baseline = 0
	d.warmups = 0
	d.detecting = false
}

func within(samples []Sample, cutoff time.Time) []Sample {
	out := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if s.At.After(cutoff) {
			out = append(out, s)
		}
	}
	return out
}

// deviation measures wobble as the mean absolute distance from the median,
// which ignores the single wild reflection that a plain variance would chase.
func deviation(samples []Sample) float64 {
	values := make([]int, len(samples))
	for i, s := range samples {
		values[i] = s.RSSI
	}
	sort.Ints(values)
	median := float64(values[len(values)/2])

	total := 0.0
	for _, v := range values {
		total += math.Abs(float64(v) - median)
	}
	return total / float64(len(values))
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total / float64(len(values))
}

func clamp(v, low, high float64) float64 {
	return math.Min(math.Max(v, low), high)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}
