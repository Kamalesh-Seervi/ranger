package insight

import (
	"testing"
	"time"

	"github.com/kd14/ranger/internal/core"
)

func tag(id string, rssi int) core.Device {
	return core.Device{
		ID: id, Kind: core.KindBLE, Addr: id, HasRSSI: true,
		RSSI: rssi, Category: "tracker", Vendor: "Apple",
	}
}

func TestFollowerNeedsSeveralPlaces(t *testing.T) {
	f := newFollower(FollowConfig{MinPlaces: 3, MinDuration: time.Minute})
	now := time.Unix(1000, 0)

	f.observe([]core.Device{tag("t1", -50)}, "p1", "Home", now)
	f.observe([]core.Device{tag("t1", -50)}, "p2", "Office", now.Add(30*time.Minute))

	if got := f.alerts(); len(got) != 0 {
		t.Fatalf("two places raised %d alerts, want none", len(got))
	}

	f.observe([]core.Device{tag("t1", -50)}, "p3", "Cafe", now.Add(time.Hour))
	got := f.alerts()
	if len(got) != 1 {
		t.Fatalf("three places raised %d alerts, want 1", len(got))
	}
	if len(got[0].Places) != 3 {
		t.Errorf("places = %v, want all three", got[0].Places)
	}
	if got[0].DeviceID != "t1" {
		t.Errorf("device = %q, want t1", got[0].DeviceID)
	}
}

func TestFollowerNeedsTime(t *testing.T) {
	f := newFollower(FollowConfig{MinPlaces: 2, MinDuration: time.Hour})
	now := time.Unix(1000, 0)

	// Three places in a minute is walking past a shop, not being followed.
	f.observe([]core.Device{tag("t1", -50)}, "p1", "Home", now)
	f.observe([]core.Device{tag("t1", -50)}, "p2", "Hall", now.Add(20*time.Second))
	f.observe([]core.Device{tag("t1", -50)}, "p3", "Street", now.Add(40*time.Second))

	if got := f.alerts(); len(got) != 0 {
		t.Fatalf("a brief encounter raised %d alerts, want none", len(got))
	}

	f.observe([]core.Device{tag("t1", -50)}, "p3", "Street", now.Add(2*time.Hour))
	if got := f.alerts(); len(got) != 1 {
		t.Fatalf("a sustained follow raised %d alerts, want 1", len(got))
	}
}

func TestFollowerIgnoresFixtures(t *testing.T) {
	f := newFollower(FollowConfig{MinPlaces: 2, MinDuration: time.Minute})
	now := time.Unix(1000, 0)

	fixtures := []core.Device{
		{ID: "ap", Kind: core.KindWiFiAP, HasRSSI: true, RSSI: -40, Category: "router"},
		{ID: "svc", Kind: core.KindService, Category: "tv"},
		{ID: "bulb", Kind: core.KindBLE, HasRSSI: true, RSSI: -40, Category: "smart-home"},
		{ID: "cam", Kind: core.KindBLE, HasRSSI: true, RSSI: -40, Category: "camera"},
		// Too far away to be travelling with anyone.
		{ID: "distant", Kind: core.KindBLE, HasRSSI: true, RSSI: -95, Category: "tracker"},
	}
	for i, placeID := range []string{"p1", "p2", "p3"} {
		f.observe(fixtures, placeID, placeID, now.Add(time.Duration(i)*time.Hour))
	}

	if got := f.alerts(); len(got) != 0 {
		t.Errorf("fixtures and distant devices raised alerts: %+v", got)
	}
}

func TestFollowerIgnoresUnknownPlace(t *testing.T) {
	f := newFollower(FollowConfig{MinPlaces: 1, MinDuration: 0})
	f.observe([]core.Device{tag("t1", -50)}, "", "", time.Unix(1000, 0))

	if got := f.alerts(); len(got) != 0 {
		t.Errorf("sightings without a known place should not count: %+v", got)
	}
}

func TestFollowerForgetsStaleDevices(t *testing.T) {
	f := newFollower(FollowConfig{MinPlaces: 2, MinDuration: time.Minute, Forget: time.Hour})
	now := time.Unix(1000, 0)

	f.observe([]core.Device{tag("old", -50)}, "p1", "Home", now)
	f.observe([]core.Device{tag("old", -50)}, "p2", "Office", now.Add(10*time.Minute))
	if len(f.alerts()) != 1 {
		t.Fatal("expected the device to alert before it went stale")
	}

	// A later sighting of something else prunes anything long gone.
	f.observe([]core.Device{tag("new", -50)}, "p1", "Home", now.Add(4*time.Hour))
	if got := f.alerts(); len(got) != 0 {
		t.Errorf("a device unseen for hours still alerts: %+v", got)
	}
}

func TestFollowerBoundsMemory(t *testing.T) {
	f := newFollower(FollowConfig{MaxTracked: 10})
	now := time.Unix(1000, 0)

	devices := make([]core.Device, 0, 100)
	for i := 0; i < 100; i++ {
		devices = append(devices, tag("dev"+itoa(i), -50))
	}
	f.observe(devices, "p1", "Home", now)

	f.mu.Lock()
	tracked := len(f.records)
	f.mu.Unlock()

	if tracked > 10 {
		t.Errorf("tracked %d devices, want at most 10", tracked)
	}
}

func TestFollowerRanksByPlaceCount(t *testing.T) {
	f := newFollower(FollowConfig{MinPlaces: 2, MinDuration: time.Minute})
	now := time.Unix(1000, 0)

	for i, placeID := range []string{"p1", "p2"} {
		f.observe([]core.Device{tag("two", -50)}, placeID, placeID, now.Add(time.Duration(i)*time.Hour))
	}
	for i, placeID := range []string{"p1", "p2", "p3", "p4"} {
		f.observe([]core.Device{tag("four", -50)}, placeID, placeID, now.Add(time.Duration(i)*time.Hour))
	}

	got := f.alerts()
	if len(got) != 2 {
		t.Fatalf("got %d alerts, want 2", len(got))
	}
	if got[0].DeviceID != "four" {
		t.Errorf("first alert is %q, want the device seen in most places", got[0].DeviceID)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	out := ""
	for v > 0 {
		out = string(rune('0'+v%10)) + out
		v /= 10
	}
	return out
}
