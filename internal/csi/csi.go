// Package csi turns Channel State Information streamed from an ESP32 into
// device-free presence, motion, and breathing estimates.
//
// A laptop Wi-Fi card reports one RSSI scalar per frame. An ESP32 exposes the
// full channel response: amplitude and phase for every OFDM subcarrier, tens
// of times per second. That resolution is what makes it possible to sense a
// still body — a breathing chest modulates the subcarriers by a fraction of a
// dB at roughly 0.2 Hz, invisible to RSSI but recoverable here.
//
// Everything in this file operates on floats parsed from an unauthenticated
// UDP packet, so Parse bounds-checks every field before use.
package csi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"sync"
	"time"
)

// Wire format, little-endian. The ESP32 firmware in firmware/esp32-csi emits
// exactly this. Header is fixed; the payload is one signed (imaginary, real)
// byte pair per subcarrier, straight from the radio's CSI buffer.
const (
	magic0, magic1, magic2, magic3 = 'R', 'C', 'S', 'I'
	wireVersion                    = 1
	headerLen                      = 24
	maxSubcarriers                 = 256
	// MaxPacket caps a single datagram, defending the parser against a spoofed
	// length. 24-byte header + 2 bytes per subcarrier, with headroom.
	MaxPacket = headerLen + 2*maxSubcarriers + 8
)

// Frame is one decoded CSI record.
type Frame struct {
	SensorID    string
	Seq         uint16
	RSSI        int
	Channel     int
	Rate        int
	TimestampUs uint32
	// Amplitude holds the magnitude of each subcarrier's complex response.
	Amplitude []float64
}

var (
	errShort   = errors.New("csi: packet shorter than header")
	errMagic   = errors.New("csi: bad magic")
	errVersion = errors.New("csi: unsupported version")
	errCount   = errors.New("csi: implausible subcarrier count")
	errLength  = errors.New("csi: payload length does not match subcarrier count")
)

// Parse decodes one datagram. It rejects anything malformed rather than
// trusting the declared length, because the source is the open network.
func Parse(data []byte) (Frame, error) {
	if len(data) < headerLen {
		return Frame{}, errShort
	}
	if data[0] != magic0 || data[1] != magic1 || data[2] != magic2 || data[3] != magic3 {
		return Frame{}, errMagic
	}
	if data[4] != wireVersion {
		return Frame{}, errVersion
	}

	subcarriers := int(binary.LittleEndian.Uint16(data[22:24]))
	if subcarriers < 1 || subcarriers > maxSubcarriers {
		return Frame{}, errCount
	}
	if len(data) != headerLen+2*subcarriers {
		return Frame{}, errLength
	}

	amp := make([]float64, subcarriers)
	for i := 0; i < subcarriers; i++ {
		im := int8(data[headerLen+2*i])
		re := int8(data[headerLen+2*i+1])
		amp[i] = math.Hypot(float64(re), float64(im))
	}

	mac := net.HardwareAddr(data[8:14])
	return Frame{
		SensorID:    mac.String(),
		Seq:         binary.LittleEndian.Uint16(data[6:8]),
		RSSI:        int(int8(data[14])),
		Channel:     int(data[15]),
		Rate:        int(data[16]),
		TimestampUs: binary.LittleEndian.Uint32(data[18:22]),
		Amplitude:   amp,
	}, nil
}

// Breathing is a recovered respiration estimate.
type Breathing struct {
	BPM        float64 `json:"bpm"`
	Confidence float64 `json:"confidence"`
}

// Reading is the current interpretation of one sensor's stream.
type Reading struct {
	Presence    bool       `json:"presence"`
	Calibrating bool       `json:"calibrating"`
	MotionLevel float64    `json:"motionLevel"`
	Disturbance float64    `json:"disturbance"`
	Baseline    float64    `json:"baseline"`
	Breathing   *Breathing `json:"breathing,omitempty"`
	Amplitude   []float64  `json:"amplitude,omitempty"`
	Subcarriers int        `json:"subcarriers"`
}

// Config tunes the analyzer.
type Config struct {
	// Window is how much recent history is retained for breathing analysis.
	Window time.Duration
	// MaxFrames caps stored frames per sensor, bounding memory when a sensor
	// streams fast.
	MaxFrames int
	// Calibration is how many frames establish the still baseline.
	Calibration int
	// MotionWindow is the short span used to measure instantaneous activity.
	MotionWindow time.Duration
	// TriggerRatio and TriggerFloor decide when activity counts as motion.
	TriggerRatio  float64
	TriggerFloor  float64
	BaselineAlpha float64
	// Breathing search band, in breaths per minute.
	MinBPM, MaxBPM float64
	// MinBreathWindow is how much history breathing needs before it reports.
	MinBreathWindow time.Duration
	// MotionCeiling suppresses breathing while the subject is clearly moving,
	// since gross motion swamps the millimetre chest signal.
	MotionCeiling float64
}

func (c Config) withDefaults() Config {
	if c.Window <= 0 {
		c.Window = 40 * time.Second
	}
	if c.MaxFrames <= 0 {
		c.MaxFrames = 4000
	}
	if c.Calibration <= 0 {
		c.Calibration = 60
	}
	if c.MotionWindow <= 0 {
		c.MotionWindow = 2 * time.Second
	}
	if c.TriggerRatio <= 1 {
		c.TriggerRatio = 2.0
	}
	if c.TriggerFloor <= 0 {
		c.TriggerFloor = 0.4
	}
	if c.BaselineAlpha <= 0 || c.BaselineAlpha >= 1 {
		c.BaselineAlpha = 0.05
	}
	if c.MinBPM <= 0 {
		c.MinBPM = 6
	}
	if c.MaxBPM <= 0 {
		c.MaxBPM = 36
	}
	if c.MinBreathWindow <= 0 {
		c.MinBreathWindow = 18 * time.Second
	}
	if c.MotionCeiling <= 0 {
		c.MotionCeiling = 0.6
	}
	return c
}

type sample struct {
	t   float64 // seconds since the sensor's first frame
	amp []float64
}

// analyzer holds one sensor's rolling history and derived state.
type analyzer struct {
	cfg Config

	mu       sync.Mutex
	origin   time.Time
	samples  []sample
	subCount int

	baseline  float64
	warmups   int
	detecting bool
	lastAmp   []float64
}

func newAnalyzer(cfg Config) *analyzer {
	return &analyzer{cfg: cfg.withDefaults()}
}

// add folds one frame into the history and updates the motion state.
func (a *analyzer) add(f Frame, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.origin.IsZero() {
		a.origin = now
	}
	t := now.Sub(a.origin).Seconds()

	a.samples = append(a.samples, sample{t: t, amp: f.Amplitude})
	a.subCount = len(f.Amplitude)
	a.lastAmp = f.Amplitude
	a.trim(t)
	a.updateMotionLocked(t)
}

// trim drops samples outside the window and enforces the frame cap.
func (a *analyzer) trim(now float64) {
	cutoff := now - a.cfg.Window.Seconds()
	drop := 0
	for drop < len(a.samples) && a.samples[drop].t < cutoff {
		drop++
	}
	if over := len(a.samples) - drop - a.cfg.MaxFrames; over > 0 {
		drop += over
	}
	if drop > 0 {
		a.samples = append(a.samples[:0], a.samples[drop:]...)
	}
}

// updateMotionLocked measures activity as the mean per-subcarrier spread over
// the short motion window, tracked against a baseline learned while still.
func (a *analyzer) updateMotionLocked(now float64) {
	activity := a.activityLocked(now)

	if a.warmups < a.cfg.Calibration {
		a.warmups++
		a.baseline += (activity - a.baseline) / float64(a.warmups)
		return
	}

	span := math.Max(a.baseline*(a.cfg.TriggerRatio-1), a.cfg.TriggerFloor)
	rise := a.baseline + span
	fall := a.baseline + span*0.5

	switch {
	case !a.detecting && activity > rise:
		a.detecting = true
	case a.detecting && activity < fall:
		a.detecting = false
	}
	// The baseline only adapts while still, so sustained motion is not learned
	// away.
	if !a.detecting {
		a.baseline += a.cfg.BaselineAlpha * (activity - a.baseline)
	}
}

// activityLocked is the mean, across subcarriers, of each subcarrier's standard
// deviation over the motion window.
func (a *analyzer) activityLocked(now float64) float64 {
	cutoff := now - a.cfg.MotionWindow.Seconds()
	start := len(a.samples)
	for start > 0 && a.samples[start-1].t >= cutoff {
		start--
	}
	window := a.samples[start:]
	if len(window) < 2 || a.subCount == 0 {
		return 0
	}

	total := 0.0
	for sc := 0; sc < a.subCount; sc++ {
		mean, count := 0.0, 0.0
		for _, s := range window {
			if sc < len(s.amp) {
				mean += s.amp[sc]
				count++
			}
		}
		if count < 2 {
			continue
		}
		mean /= count
		vari := 0.0
		for _, s := range window {
			if sc < len(s.amp) {
				d := s.amp[sc] - mean
				vari += d * d
			}
		}
		total += math.Sqrt(vari / count)
	}
	return total / float64(a.subCount)
}

// reading produces the current interpretation, including a breathing estimate
// when the subject is present and still enough.
func (a *analyzer) reading() Reading {
	a.mu.Lock()
	defer a.mu.Unlock()

	calibrating := a.warmups < a.cfg.Calibration
	activity := 0.0
	if n := len(a.samples); n > 0 {
		activity = a.activityLocked(a.samples[n-1].t)
	}

	span := math.Max(a.baseline*(a.cfg.TriggerRatio-1), a.cfg.TriggerFloor)
	level := clamp((activity-a.baseline)/(span*2), 0, 1)

	r := Reading{
		Presence:    a.detecting,
		Calibrating: calibrating,
		MotionLevel: round2(level),
		Disturbance: round3(activity),
		Baseline:    round3(a.baseline),
		Amplitude:   append([]float64(nil), a.lastAmp...),
		Subcarriers: a.subCount,
	}
	// Breathing is the tiny periodic signal a still body leaves. It is only
	// recoverable when gross motion is low, so it runs below the ceiling
	// regardless of the motion flag. Finding it is itself evidence of presence.
	if !calibrating && level <= a.cfg.MotionCeiling {
		if b := a.breathingLocked(); b != nil {
			r.Breathing = b
			r.Presence = true
		}
	}
	return r
}

// breathingLocked searches the most active subcarriers for a periodic
// component in the respiration band, using real timestamps so packet jitter
// does not smear the frequency estimate.
func (a *analyzer) breathingLocked() *Breathing {
	if len(a.samples) < 32 {
		return nil
	}
	span := a.samples[len(a.samples)-1].t - a.samples[0].t
	if span < a.cfg.MinBreathWindow.Seconds() {
		return nil
	}

	candidates := a.topSubcarriersLocked(6)
	if len(candidates) == 0 {
		return nil
	}

	minHz := a.cfg.MinBPM / 60
	maxHz := a.cfg.MaxBPM / 60
	// ~0.4 BPM resolution keeps the scan cheap while resolving normal rates.
	steps := int((a.cfg.MaxBPM-a.cfg.MinBPM)/0.4) + 1

	bestPeak, bestMean, bestHz := 0.0, 0.0, 0.0
	for _, sc := range candidates {
		series, times := a.seriesLocked(sc)
		detrend(series)

		peak, mean, hz := scanBand(series, times, minHz, maxHz, steps)
		// The subcarrier with the sharpest peak relative to its band wins.
		if mean > 0 && peak/mean > safeRatio(bestPeak, bestMean) {
			bestPeak, bestMean, bestHz = peak, mean, hz
		}
	}
	if bestMean <= 0 {
		return nil
	}

	snr := bestPeak / bestMean
	// A band-limited periodogram of pure noise has a max-to-mean ratio around
	// ln(bins) ≈ 3-4, so the gate sits well above that. Better to report no
	// reading than to invent a breathing rate from noise.
	if snr < 6 {
		return nil
	}
	confidence := clamp((snr-6)/8, 0, 1)
	return &Breathing{
		BPM:        round1(bestHz * 60),
		Confidence: round2(confidence),
	}
}

// topSubcarriersLocked returns the indices of the subcarriers whose amplitude
// varies the most, since a still-but-breathing subject shows up only there.
func (a *analyzer) topSubcarriersLocked(n int) []int {
	if a.subCount == 0 {
		return nil
	}
	type scored struct {
		idx int
		v   float64
	}
	scores := make([]scored, 0, a.subCount)
	for sc := 0; sc < a.subCount; sc++ {
		mean, count := 0.0, 0.0
		for _, s := range a.samples {
			if sc < len(s.amp) {
				mean += s.amp[sc]
				count++
			}
		}
		if count < 2 {
			continue
		}
		mean /= count
		vari := 0.0
		for _, s := range a.samples {
			if sc < len(s.amp) {
				d := s.amp[sc] - mean
				vari += d * d
			}
		}
		scores = append(scores, scored{sc, vari / count})
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].v > scores[j].v })
	if len(scores) > n {
		scores = scores[:n]
	}
	out := make([]int, len(scores))
	for i, s := range scores {
		out[i] = s.idx
	}
	return out
}

func (a *analyzer) seriesLocked(sc int) (values, times []float64) {
	values = make([]float64, 0, len(a.samples))
	times = make([]float64, 0, len(a.samples))
	for _, s := range a.samples {
		if sc < len(s.amp) {
			values = append(values, s.amp[sc])
			times = append(times, s.t)
		}
	}
	return values, times
}

// scanBand evaluates the DFT magnitude at each candidate frequency and returns
// the peak, the band mean, and the peak's frequency. Timestamps are used
// directly, so uneven sampling is handled correctly.
func scanBand(values, times []float64, minHz, maxHz float64, steps int) (peak, mean, peakHz float64) {
	if steps < 1 || len(values) == 0 {
		return 0, 0, 0
	}
	sum := 0.0
	for i := 0; i < steps; i++ {
		f := minHz + (maxHz-minHz)*float64(i)/float64(steps-1)
		re, im := 0.0, 0.0
		for k, x := range values {
			ang := 2 * math.Pi * f * times[k]
			re += x * math.Cos(ang)
			im += x * math.Sin(ang)
		}
		mag := math.Hypot(re, im) / float64(len(values))
		sum += mag
		if mag > peak {
			peak, peakHz = mag, f
		}
	}
	return peak, sum / float64(steps), peakHz
}

// detrend removes DC and slow linear drift by subtracting the least-squares
// best-fit line. Unlike a short moving-average high-pass, this leaves the
// breathing oscillation itself untouched — a moving average whose window is
// near the breathing period would cancel most of the signal.
func detrend(series []float64) {
	n := len(series)
	if n < 2 {
		return
	}
	// Fit y = a + b*x with x = sample index; the exact spacing does not matter
	// because only DC and slope are being removed.
	var sx, sy, sxx, sxy float64
	for i, y := range series {
		x := float64(i)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	fn := float64(n)
	denom := fn*sxx - sx*sx
	if denom == 0 {
		return
	}
	b := (fn*sxy - sx*sy) / denom
	a := (sy - b*sx) / fn
	for i := range series {
		series[i] -= a + b*float64(i)
	}
}

func safeRatio(peak, mean float64) float64 {
	if mean <= 0 {
		return 0
	}
	return peak / mean
}

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }
func round1(v float64) float64        { return math.Round(v*10) / 10 }
func round2(v float64) float64        { return math.Round(v*100) / 100 }
func round3(v float64) float64        { return math.Round(v*1000) / 1000 }

// String renders a frame compactly for debug logging.
func (f Frame) String() string {
	return fmt.Sprintf("csi %s seq=%d rssi=%d ch=%d sub=%d", f.SensorID, f.Seq, f.RSSI, f.Channel, len(f.Amplitude))
}
