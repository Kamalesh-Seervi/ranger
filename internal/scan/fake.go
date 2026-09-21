package scan

import (
	"context"
	"math"
	"math/rand"
	"time"

	"github.com/kd14/ranger/internal/core"
)

// FakeName is the scanner name reported by the synthetic backend.
const FakeName = "fake"

// Fake emits synthetic devices so the dashboard can be developed and demoed
// without radios, permissions, or nearby hardware.
type Fake struct {
	Interval time.Duration
	Seed     int64

	devices []*fakeDevice
	rng     *rand.Rand
}

type fakeDevice struct {
	kind      core.Kind
	addr      string
	name      string
	vendor    string
	security  string
	frequency int
	width     int
	baseRSSI  float64
	rssi      float64
	// drift gives each device its own slow sinusoidal movement so the radar
	// and sparklines show something other than white noise.
	phase float64
	speed float64
}

func NewFake() *Fake {
	return &Fake{Interval: time.Second, Seed: 1}
}

func (f *Fake) Name() string { return FakeName }

func (f *Fake) Check(context.Context) Availability { return Available() }

func (f *Fake) Run(ctx context.Context, out chan<- core.Observation) error {
	if f.Interval <= 0 {
		f.Interval = time.Second
	}
	seed := f.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	f.rng = rand.New(rand.NewSource(seed))
	f.devices = buildFakeDevices(f.rng)

	ticker := time.NewTicker(f.Interval)
	defer ticker.Stop()

	for {
		if !f.sweep(ctx, out) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// sweep advances every synthetic device and emits one observation each.
func (f *Fake) sweep(ctx context.Context, out chan<- core.Observation) bool {
	now := time.Now()
	for _, d := range f.devices {
		d.advance(f.rng)

		obs := core.Observation{
			Kind:         d.kind,
			Addr:         d.addr,
			Name:         d.name,
			Vendor:       d.vendor,
			Security:     d.security,
			Frequency:    d.frequency,
			ChannelWidth: d.width,
			RSSI:         int(math.Round(d.rssi)),
			HasRSSI:      d.kind != core.KindService,
			Source:       FakeName,
			Seen:         now,
			Meta:         map[string]string{"synthetic": "true"},
		}
		if !emit(ctx, out, obs) {
			return false
		}
	}
	return true
}

// advance moves a device's signal with a slow drift plus small noise, clamped
// to a plausible band around its baseline.
func (d *fakeDevice) advance(rng *rand.Rand) {
	d.phase += d.speed
	drift := math.Sin(d.phase) * 9
	noise := rng.NormFloat64() * 1.2
	d.rssi = d.baseRSSI + drift + noise

	if d.rssi > -20 {
		d.rssi = -20
	}
	if d.rssi < -95 {
		d.rssi = -95
	}
}

func buildFakeDevices(rng *rand.Rand) []*fakeDevice {
	specs := []struct {
		kind      core.Kind
		addr      string
		name      string
		vendor    string
		security  string
		frequency int
		width     int
		rssi      float64
	}{
		{core.KindWiFiAP, "3c:37:86:11:22:01", "Fern Hollow", "Netgear", "WPA3", 2412, 20, -42},
		{core.KindWiFiAP, "3c:37:86:11:22:02", "Fern Hollow 5G", "Netgear", "WPA3", 5180, 80, -49},
		{core.KindWiFiAP, "b8:27:eb:44:55:01", "picoberry", "Raspberry Pi", "WPA2", 2437, 20, -67},
		{core.KindWiFiAP, "00:1a:1e:77:88:01", "CorpGuest", "Aruba", "WPA2-Enterprise", 2462, 20, -71},
		{core.KindWiFiAP, "00:1a:1e:77:88:02", "CorpGuest", "Aruba", "WPA2-Enterprise", 5745, 80, -63},
		{core.KindWiFiAP, "f0:9f:c2:aa:bb:01", "Attic Mesh 6E", "Ubiquiti", "WPA3", 6115, 160, -58},
		{core.KindWiFiAP, "e4:95:6e:00:00:01", "", "", "WPA2", 5220, 40, -80},
		{core.KindWiFiClient, "8a:3f:11:99:00:01", "", "", "", 2412, 20, -74},
		{core.KindWiFiClient, "5e:c2:07:33:00:02", "", "", "", 5180, 20, -69},
		{core.KindBLE, "6f:1c:b4:e2:00:01", "AirPods Pro", "Apple", "", 0, 0, -55},
		{core.KindBLE, "4a:88:d0:71:00:02", "Galaxy Watch", "Samsung", "", 0, 0, -73},
		{core.KindBLE, "7c:2f:80:16:00:03", "Tile Mate", "Tile", "", 0, 0, -81},
		{core.KindBLE, "2d:91:ee:c5:00:04", "", "(randomized)", "", 0, 0, -88},
		{core.KindBLE, "39:04:aa:bc:00:05", "Ruuvi 8C21", "Ruuvi", "", 0, 0, -64},
		{core.KindBLE, "58:d3:91:20:00:06", "SoundLink", "Bose", "", 0, 0, -70},
		{core.KindService, "living-room-tv._googlecast._tcp", "Living Room TV", "Google", "", 0, 0, 0},
		{core.KindService, "office-hp._ipp._tcp", "Office HP LaserJet", "HP", "", 0, 0, 0},
		{core.KindService, "studio._airplay._tcp", "Studio HomePod", "Apple", "", 0, 0, 0},
	}

	devices := make([]*fakeDevice, 0, len(specs))
	for _, s := range specs {
		devices = append(devices, &fakeDevice{
			kind:      s.kind,
			addr:      s.addr,
			name:      s.name,
			vendor:    s.vendor,
			security:  s.security,
			frequency: s.frequency,
			width:     s.width,
			baseRSSI:  s.rssi,
			rssi:      s.rssi,
			phase:     rng.Float64() * 2 * math.Pi,
			speed:     0.04 + rng.Float64()*0.10,
		})
	}
	return devices
}
