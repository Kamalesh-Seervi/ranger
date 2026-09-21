package motion

import (
	"math/rand"
	"testing"
	"time"
)

// anchors builds n anchors whose readings wobble by roughly the given amount.
func anchors(n int, base int, wobble float64, at time.Time, rng *rand.Rand) []Anchor {
	out := make([]Anchor, 0, n)
	for i := 0; i < n; i++ {
		samples := make([]Sample, 0, 10)
		for j := 0; j < 10; j++ {
			jitter := int(rng.NormFloat64() * wobble)
			samples = append(samples, Sample{
				At:   at.Add(time.Duration(j) * time.Second),
				RSSI: base - i*5 + jitter,
			})
		}
		out = append(out, Anchor{ID: string(rune('a' + i)), Samples: samples})
	}
	return out
}

// settle runs enough quiet observations to finish calibration.
func settle(d *Detector, rng *rand.Rand, now time.Time) (State, time.Time) {
	var state State
	for i := 0; i < 30; i++ {
		now = now.Add(time.Second)
		state = d.Observe(anchors(4, -50, 0.4, now.Add(-10*time.Second), rng), now)
	}
	return state, now
}

func TestNeedsEnoughAnchors(t *testing.T) {
	d := NewDetector(Config{MinAnchors: 3})
	rng := rand.New(rand.NewSource(1))
	now := time.Unix(1000, 0)

	state := d.Observe(anchors(1, -50, 0.5, now.Add(-10*time.Second), rng), now)
	if state.Active {
		t.Error("one anchor should not be enough to judge the room")
	}
	if state.Reason == "" {
		t.Error("an inactive detector should explain why")
	}
}

func TestCalibratesBeforeReporting(t *testing.T) {
	d := NewDetector(Config{Calibration: 10})
	rng := rand.New(rand.NewSource(1))
	now := time.Unix(1000, 0)

	first := d.Observe(anchors(4, -50, 0.4, now.Add(-10*time.Second), rng), now)
	if !first.Calibrating {
		t.Error("the detector should calibrate before judging")
	}
	if first.Detected {
		t.Error("nothing should be detected while calibrating")
	}

	settled, _ := settle(d, rng, now)
	if settled.Calibrating {
		t.Error("calibration should have finished")
	}
	if settled.Baseline <= 0 {
		t.Errorf("baseline = %v, want a positive quiet level", settled.Baseline)
	}
}

func TestQuietRoomDoesNotTrigger(t *testing.T) {
	d := NewDetector(Config{})
	rng := rand.New(rand.NewSource(2))
	now := time.Unix(1000, 0)

	_, now = settle(d, rng, now)
	for i := 0; i < 40; i++ {
		now = now.Add(time.Second)
		state := d.Observe(anchors(4, -50, 0.4, now.Add(-10*time.Second), rng), now)
		if state.Detected {
			t.Fatalf("a still room triggered at iteration %d (disturbance %v, baseline %v)",
				i, state.Disturbance, state.Baseline)
		}
	}
}

func TestDisturbanceIsDetected(t *testing.T) {
	d := NewDetector(Config{})
	rng := rand.New(rand.NewSource(3))
	now := time.Unix(1000, 0)

	_, now = settle(d, rng, now)

	// Something crosses the paths: the wobble jumps sharply.
	now = now.Add(time.Second)
	state := d.Observe(anchors(4, -50, 6.0, now.Add(-10*time.Second), rng), now)
	if !state.Detected {
		t.Fatalf("movement was missed (disturbance %v, baseline %v)",
			state.Disturbance, state.Baseline)
	}
	if state.Level <= 0.5 {
		t.Errorf("level = %v, want above 0.5 once triggered", state.Level)
	}
	if state.Since.IsZero() {
		t.Error("a detection should record when it started")
	}
}

func TestBaselineDoesNotAbsorbSustainedMovement(t *testing.T) {
	d := NewDetector(Config{})
	rng := rand.New(rand.NewSource(4))
	now := time.Unix(1000, 0)

	quiet, now := settle(d, rng, now)

	// Keep disturbing the room. If the baseline learned during detection, the
	// flag would quietly switch off and never return.
	for i := 0; i < 30; i++ {
		now = now.Add(time.Second)
		state := d.Observe(anchors(4, -50, 6.0, now.Add(-10*time.Second), rng), now)
		if i > 2 && !state.Detected {
			t.Fatalf("sustained movement stopped registering at iteration %d "+
				"(disturbance %v, baseline %v)", i, state.Disturbance, state.Baseline)
		}
	}

	final := d.Observe(anchors(4, -50, 6.0, now.Add(-10*time.Second), rng), now.Add(time.Second))
	if final.Baseline > quiet.Baseline*1.5 {
		t.Errorf("baseline drifted from %v to %v during movement", quiet.Baseline, final.Baseline)
	}
}

func TestReturnsToQuietAfterMovementStops(t *testing.T) {
	d := NewDetector(Config{})
	rng := rand.New(rand.NewSource(5))
	now := time.Unix(1000, 0)

	_, now = settle(d, rng, now)

	now = now.Add(time.Second)
	if state := d.Observe(anchors(4, -50, 6.0, now.Add(-10*time.Second), rng), now); !state.Detected {
		t.Fatal("movement was not detected")
	}

	for i := 0; i < 10; i++ {
		now = now.Add(time.Second)
		state := d.Observe(anchors(4, -50, 0.4, now.Add(-10*time.Second), rng), now)
		if !state.Detected {
			return
		}
	}
	t.Error("the detector stayed triggered after the room went quiet")
}

func TestResetClearsBaseline(t *testing.T) {
	d := NewDetector(Config{})
	rng := rand.New(rand.NewSource(6))
	now := time.Unix(1000, 0)

	settled, now := settle(d, rng, now)
	if settled.Calibrating {
		t.Fatal("expected calibration to finish")
	}

	d.Reset()
	after := d.Observe(anchors(4, -50, 0.4, now.Add(-9*time.Second), rng), now.Add(time.Second))
	if !after.Calibrating {
		t.Error("reset should send the detector back to calibrating")
	}
}

func TestStaleSamplesAreIgnored(t *testing.T) {
	d := NewDetector(Config{Window: 5 * time.Second})
	now := time.Unix(1000, 0)

	stale := []Anchor{{
		ID: "old",
		Samples: []Sample{
			{At: now.Add(-time.Hour), RSSI: -50},
			{At: now.Add(-time.Hour), RSSI: -80},
		},
	}}
	if state := d.Observe(stale, now); state.Active {
		t.Error("samples older than the window should not count")
	}
}

func TestDeviationIgnoresASingleOutlier(t *testing.T) {
	at := time.Unix(1000, 0)
	steady := []Sample{
		{at, -50}, {at, -50}, {at, -51}, {at, -50}, {at, -49},
		{at, -50}, {at, -50}, {at, -51}, {at, -50},
	}
	withSpike := append(append([]Sample{}, steady...), Sample{at, -10})

	if deviation(withSpike) > deviation(steady)+6 {
		t.Errorf("one reflection moved deviation from %v to %v",
			deviation(steady), deviation(withSpike))
	}
}
