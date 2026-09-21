package core

import (
	"testing"
	"time"
)

func TestChannelForFrequency(t *testing.T) {
	cases := []struct {
		mhz  int
		want int
		band Band
	}{
		{2412, 1, Band24},
		{2437, 6, Band24},
		{2472, 13, Band24},
		{2484, 14, Band24},
		{5180, 36, Band5},
		{5745, 149, Band5},
		{5885, 177, Band5},
		{5935, 2, Band6},
		{5955, 1, Band6},
		{7115, 233, Band6},
		{1000, 0, BandUnknown},
	}
	for _, c := range cases {
		if got := ChannelForFrequency(c.mhz); got != c.want {
			t.Errorf("ChannelForFrequency(%d) = %d, want %d", c.mhz, got, c.want)
		}
		if got := BandForFrequency(c.mhz); got != c.band {
			t.Errorf("BandForFrequency(%d) = %q, want %q", c.mhz, got, c.band)
		}
	}
}

func TestFrequencyForChannelRoundTrip(t *testing.T) {
	cases := []struct {
		ch   int
		band Band
	}{
		{1, Band24}, {6, Band24}, {13, Band24}, {14, Band24},
		{36, Band5}, {149, Band5},
		{1, Band6}, {2, Band6}, {233, Band6},
	}
	for _, c := range cases {
		mhz := FrequencyForChannel(c.ch, c.band)
		if mhz == 0 {
			t.Fatalf("FrequencyForChannel(%d, %q) returned 0", c.ch, c.band)
		}
		if got := ChannelForFrequency(mhz); got != c.ch {
			t.Errorf("round trip ch %d band %q via %d MHz gave %d", c.ch, c.band, mhz, got)
		}
	}
}

func TestNormalizeAddr(t *testing.T) {
	want := "aabbccddeeff"
	for _, in := range []string{"AA:BB:CC:DD:EE:FF", "aa-bb-cc-dd-ee-ff", "aabb.ccdd.eeff", "AABBCCDDEEFF"} {
		if got := NormalizeAddr(in); got != want {
			t.Errorf("NormalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRingWrapAround(t *testing.T) {
	r := newRing(3)
	if got := r.samples(); got != nil {
		t.Fatalf("empty ring returned %v", got)
	}
	base := time.Unix(0, 0)
	for i := 1; i <= 5; i++ {
		r.add(Sample{At: base.Add(time.Duration(i) * time.Second), RSSI: -i})
	}
	got := r.samples()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// Oldest to newest, keeping only the last three additions.
	for i, want := range []int{-3, -4, -5} {
		if got[i].RSSI != want {
			t.Errorf("sample %d RSSI = %d, want %d", i, got[i].RSSI, want)
		}
	}
}

func TestObserveMergeKeepsExistingIdentity(t *testing.T) {
	reg := NewRegistry(Config{}, NewBus())
	now := time.Unix(1000, 0)
	reg.now = func() time.Time { return now }

	reg.Observe(Observation{
		Kind: KindWiFiAP, Addr: "AA:BB:CC:00:11:22",
		Name: "HomeNet", Security: "WPA3", Frequency: 2437,
		RSSI: -40, HasRSSI: true, Source: "wifi", Seen: now,
	})
	// A later sighting from a different source that cannot see the SSID must
	// not erase the name already recorded.
	reg.Observe(Observation{
		Kind: KindWiFiAP, Addr: "aa-bb-cc-00-11-22",
		RSSI: -55, HasRSSI: true, Source: "monitor", Seen: now.Add(time.Second),
	})

	devices := reg.Snapshot()
	if len(devices) != 1 {
		t.Fatalf("expected addresses to normalize into 1 device, got %d", len(devices))
	}
	d := devices[0]
	if d.Name != "HomeNet" {
		t.Errorf("Name = %q, want HomeNet", d.Name)
	}
	if d.Security != "WPA3" {
		t.Errorf("Security = %q, want WPA3", d.Security)
	}
	if d.RSSI != -55 {
		t.Errorf("RSSI = %d, want -55", d.RSSI)
	}
	if d.Channel != 6 {
		t.Errorf("Channel = %d, want 6", d.Channel)
	}
	if len(d.Sources) != 2 {
		t.Errorf("Sources = %v, want both scanners", d.Sources)
	}
	if len(d.History) != 2 {
		t.Errorf("History len = %d, want 2", len(d.History))
	}
}

func TestObserveRejectsUnidentifiable(t *testing.T) {
	reg := NewRegistry(Config{}, NewBus())
	reg.Observe(Observation{Kind: KindWiFiAP, RSSI: -50, HasRSSI: true})
	if got := len(reg.Snapshot()); got != 0 {
		t.Fatalf("expected observation without Addr or Key to be dropped, got %d devices", got)
	}
}

func TestExpire(t *testing.T) {
	reg := NewRegistry(Config{TTL: 30 * time.Second}, NewBus())
	now := time.Unix(1000, 0)
	reg.now = func() time.Time { return now }

	reg.Observe(Observation{Kind: KindBLE, Addr: "11:22:33:44:55:66", Seen: now})
	reg.Observe(Observation{Kind: KindBLE, Addr: "77:88:99:aa:bb:cc", Seen: now.Add(25 * time.Second)})

	now = now.Add(40 * time.Second)
	if got := reg.Expire(); got != 1 {
		t.Fatalf("Expire removed %d, want 1", got)
	}
	remaining := reg.Snapshot()
	if len(remaining) != 1 || remaining[0].Addr != "77:88:99:aa:bb:cc" {
		t.Fatalf("wrong device expired: %+v", remaining)
	}
}

func TestMaxDevicesEviction(t *testing.T) {
	reg := NewRegistry(Config{MaxDevices: 2}, NewBus())
	now := time.Unix(1000, 0)
	reg.now = func() time.Time { return now }

	reg.Observe(Observation{Kind: KindBLE, Addr: "00:00:00:00:00:01", Seen: now})
	reg.Observe(Observation{Kind: KindBLE, Addr: "00:00:00:00:00:02", Seen: now.Add(time.Second)})
	reg.Observe(Observation{Kind: KindBLE, Addr: "00:00:00:00:00:03", Seen: now.Add(2 * time.Second)})

	devices := reg.Snapshot()
	if len(devices) != 2 {
		t.Fatalf("len = %d, want 2 after eviction", len(devices))
	}
	for _, d := range devices {
		if d.Addr == "00:00:00:00:00:01" {
			t.Error("least recently seen device should have been evicted")
		}
	}
}

func TestBusDropsWhenSubscriberIsFull(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe(1)
	defer cancel()

	// More publishes than buffer space must not block.
	for i := 0; i < 100; i++ {
		bus.Publish(Event{Type: EventUpsert})
	}
	if len(ch) != 1 {
		t.Fatalf("buffered %d events, want 1", len(ch))
	}
}

func TestBusUnsubscribeIsIdempotent(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe(1)
	cancel()
	cancel()
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after cancel")
	}
	bus.Publish(Event{Type: EventUpsert})
}
