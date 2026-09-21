package csi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand"
	"net"
	"testing"
	"time"

	"github.com/kd14/ranger/internal/core"
)

// encode builds a wire packet from complex subcarrier samples, matching the
// firmware layout so the tests exercise the real parser.
func encode(sensorID []byte, seq uint16, rssi int8, channel uint8, im, re []int8) []byte {
	n := len(im)
	buf := make([]byte, headerLen+2*n)
	buf[0], buf[1], buf[2], buf[3] = magic0, magic1, magic2, magic3
	buf[4] = wireVersion
	binary.LittleEndian.PutUint16(buf[6:8], seq)
	copy(buf[8:14], sensorID)
	buf[14] = byte(rssi)
	buf[15] = channel
	binary.LittleEndian.PutUint16(buf[22:24], uint16(n))
	for i := 0; i < n; i++ {
		buf[headerLen+2*i] = byte(im[i])
		buf[headerLen+2*i+1] = byte(re[i])
	}
	return buf
}

func TestParseRoundTrip(t *testing.T) {
	mac := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01}
	im := []int8{10, -20, 30, 0}
	re := []int8{5, 5, -5, 40}

	frame, err := Parse(encode(mac, 42, -55, 6, im, re))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if frame.SensorID != "de:ad:be:ef:00:01" {
		t.Errorf("SensorID = %q", frame.SensorID)
	}
	if frame.Seq != 42 || frame.RSSI != -55 || frame.Channel != 6 {
		t.Errorf("header mismatch: %+v", frame)
	}
	if len(frame.Amplitude) != 4 {
		t.Fatalf("subcarriers = %d, want 4", len(frame.Amplitude))
	}
	if got := frame.Amplitude[3]; math.Abs(got-40) > 0.001 {
		t.Errorf("amplitude[3] = %v, want 40", got)
	}
	if got := frame.Amplitude[0]; math.Abs(got-math.Hypot(5, 10)) > 0.001 {
		t.Errorf("amplitude[0] = %v", got)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	good := encode([]byte{1, 2, 3, 4, 5, 6}, 1, -50, 6, []int8{1, 2}, []int8{3, 4})

	cases := map[string][]byte{
		"nil":            nil,
		"short":          good[:10],
		"bad magic":      append([]byte{'X'}, good[1:]...),
		"truncated body": good[:len(good)-1],
	}
	for name, data := range cases {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	// Wrong version.
	bad := append([]byte(nil), good...)
	bad[4] = 9
	if _, err := Parse(bad); err == nil {
		t.Error("bad version: expected an error")
	}

	// Implausible subcarrier count.
	bad = append([]byte(nil), good...)
	binary.LittleEndian.PutUint16(bad[22:24], 60000)
	if _, err := Parse(bad); err == nil {
		t.Error("huge subcarrier count: expected an error")
	}
}

// TestParseNeverPanics throws random and truncated bytes at the parser, since
// its input is unauthenticated network data.
func TestParseNeverPanics(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 4000; i++ {
		b := make([]byte, rng.Intn(600))
		rng.Read(b)
		if rng.Intn(2) == 0 && len(b) >= 4 {
			b[0], b[1], b[2], b[3] = magic0, magic1, magic2, magic3
		}
		Parse(b)
	}

	valid := encode([]byte{1, 2, 3, 4, 5, 6}, 1, -50, 6,
		[]int8{1, 2, 3, 4}, []int8{5, 6, 7, 8})
	for i := 0; i <= len(valid); i++ {
		Parse(valid[:i])
	}
}

// synthFrame builds a frame whose active subcarriers carry a breathing
// oscillation plus noise, the way a still chest modulates the channel.
func synthFrame(t float64, bpm float64, depth float64, rng *rand.Rand) Frame {
	const sub = 32
	amp := make([]float64, sub)
	breath := math.Sin(2*math.Pi*(bpm/60)*t) * depth
	for i := range amp {
		base := 40.0 + float64(i%5)
		noise := rng.NormFloat64() * 0.15
		// Only some subcarriers respond to the body, as in reality.
		if i%4 == 0 {
			amp[i] = base + breath + noise
		} else {
			amp[i] = base + noise
		}
	}
	return Frame{Amplitude: amp}
}

func TestBreathingRecoveredFromSyntheticSignal(t *testing.T) {
	a := newAnalyzer(Config{})
	rng := rand.New(rand.NewSource(7))

	const (
		fps      = 50.0
		duration = 30.0
		wantBPM  = 15.0
	)
	origin := time.Unix(1000, 0)
	// Force the still baseline low so the gentle breathing counts as presence.
	for i := 0; i < int(fps*duration); i++ {
		t := float64(i) / fps
		now := origin.Add(time.Duration(t * float64(time.Second)))
		a.add(synthFrame(t, wantBPM, 1.2, rng), now)
	}

	r := a.reading()
	if r.Breathing == nil {
		t.Fatalf("no breathing recovered (presence=%v motion=%v)", r.Presence, r.MotionLevel)
	}
	if math.Abs(r.Breathing.BPM-wantBPM) > 1.5 {
		t.Errorf("BPM = %v, want within 1.5 of %v", r.Breathing.BPM, wantBPM)
	}
	if r.Breathing.Confidence <= 0 {
		t.Errorf("confidence = %v, want > 0", r.Breathing.Confidence)
	}
}

func TestStillRoomReportsNoBreathing(t *testing.T) {
	a := newAnalyzer(Config{})
	rng := rand.New(rand.NewSource(11))

	origin := time.Unix(1000, 0)
	for i := 0; i < 1500; i++ {
		t := float64(i) / 50
		now := origin.Add(time.Duration(t * float64(time.Second)))
		// Pure noise, no periodic component.
		amp := make([]float64, 32)
		for j := range amp {
			amp[j] = 40 + rng.NormFloat64()*0.1
		}
		a.add(Frame{Amplitude: amp}, now)
	}

	r := a.reading()
	if r.Presence {
		t.Errorf("still room reported presence (activity vs baseline)")
	}
	if r.Breathing != nil {
		t.Errorf("still room reported breathing %v BPM", r.Breathing.BPM)
	}
}

func TestMotionSuppressesBreathing(t *testing.T) {
	a := newAnalyzer(Config{})
	rng := rand.New(rand.NewSource(13))

	origin := time.Unix(1000, 0)
	// Calibrate on a still room so the baseline is low, then inject a strong
	// breathing-band oscillation buried under gross movement. The motion
	// ceiling, not noise, must be what suppresses the reading.
	for i := 0; i < 1800; i++ {
		t := float64(i) / 50
		now := origin.Add(time.Duration(t * float64(time.Second)))
		amp := make([]float64, 32)
		moving := t > 12
		for j := range amp {
			amp[j] = 40 + rng.NormFloat64()*0.1
			if j%4 == 0 {
				amp[j] += math.Sin(2*math.Pi*0.25*t) * 1.2
			}
			if moving {
				amp[j] += rng.NormFloat64() * 6
			}
		}
		a.add(Frame{Amplitude: amp}, now)
	}

	r := a.reading()
	// The default motion ceiling is 0.6; gross movement must push past it.
	if r.MotionLevel <= 0.6 {
		t.Fatalf("expected motion level above the ceiling, got %v", r.MotionLevel)
	}
	if r.Breathing != nil {
		t.Errorf("gross motion should suppress breathing, got %v BPM", r.Breathing.BPM)
	}
}

func TestScanBandFindsKnownFrequency(t *testing.T) {
	const f = 0.25 // 15 BPM
	n := 1500
	values := make([]float64, n)
	times := make([]float64, n)
	for i := 0; i < n; i++ {
		times[i] = float64(i) / 50
		values[i] = math.Sin(2 * math.Pi * f * times[i])
	}
	peak, mean, hz := scanBand(values, times, 0.1, 0.6, 60)
	if math.Abs(hz-f) > 0.01 {
		t.Errorf("peak at %v Hz, want %v", hz, f)
	}
	if peak <= mean {
		t.Errorf("peak %v not above band mean %v", peak, mean)
	}
}

func TestServiceIngestsOverUDP(t *testing.T) {
	bus := core.NewBus()
	events, cancelSub := bus.Subscribe(16)
	defer cancelSub()

	svc := NewService(ServiceConfig{
		Listen:          "127.0.0.1:0",
		PublishInterval: 100 * time.Millisecond,
	}, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)

	select {
	case <-svc.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("CSI service never bound its socket")
	}
	addr := svc.BoundAddr()

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	mac := []byte{0xaa, 0xbb, 0xcc, 0x00, 0x11, 0x22}
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	seq := uint16(0)
	for {
		select {
		case <-deadline:
			t.Fatal("no CSI sensor appeared in the published state")
		case <-tick.C:
			seq++
			im := []int8{int8(seq), 2, 3, 4}
			re := []int8{5, 6, 7, 8}
			conn.Write(encode(mac, seq, -60, 6, im, re))
		case ev := <-events:
			if ev.Type != core.EventCSI {
				continue
			}
			var state State
			if err := json.Unmarshal(ev.Data, &state); err != nil {
				t.Fatalf("decode state: %v", err)
			}
			if !state.Active {
				continue
			}
			for _, s := range state.Sensors {
				if s.ID == "aa:bb:cc:00:11:22" {
					return
				}
			}
		}
	}
}
