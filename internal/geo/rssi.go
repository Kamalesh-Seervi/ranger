// Package geo turns signal strength into rough physical distance.
package geo

import (
	"math"

	"github.com/kd14/ranger/internal/core"
)

// Model is a log-distance path loss model.
//
// Distance estimates from RSSI are approximate at best: transmit power varies
// per device, and walls, bodies and reflections routinely shift readings by
// 10 dB or more. Treat the output as an ordering hint, not a measurement.
type Model struct {
	// RefRSSI is the expected signal in dBm at one metre.
	RefRSSI int
	// Exponent is the path loss exponent: 2.0 in free space, 2.7-3.5 indoors.
	Exponent float64
}

// MinDistance and MaxDistance clamp the output to a physically plausible range.
const (
	MinDistance = 0.3
	MaxDistance = 250.0
)

// DefaultModel returns typical reference values for a device kind.
func DefaultModel(kind core.Kind) Model {
	switch kind {
	case core.KindBLE:
		// -59 dBm at 1 m is the Bluetooth SIG convention for beacon calibration.
		return Model{RefRSSI: -59, Exponent: 2.5}
	case core.KindWiFiClient:
		return Model{RefRSSI: -45, Exponent: 3.0}
	default:
		return Model{RefRSSI: -40, Exponent: 3.0}
	}
}

// Distance estimates metres from an RSSI reading in dBm.
func (m Model) Distance(rssi int) float64 {
	exponent := m.Exponent
	if exponent <= 0 {
		exponent = 3.0
	}
	d := math.Pow(10, float64(m.RefRSSI-rssi)/(10*exponent))
	return math.Min(math.Max(d, MinDistance), MaxDistance)
}

// Quality maps RSSI onto 0..1 for signal bars. -30 dBm and better is full
// strength; -95 dBm and worse is empty.
func Quality(rssi int) float64 {
	const best, worst = -30.0, -95.0
	q := (float64(rssi) - worst) / (best - worst)
	return math.Min(math.Max(q, 0), 1)
}
