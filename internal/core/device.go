// Package core holds the radio-agnostic device model shared by every scanner
// backend and the HTTP layer.
package core

import (
	"strings"
	"time"
)

// Kind identifies which radio or protocol a device was observed on.
type Kind string

const (
	KindWiFiAP     Kind = "wifi-ap"
	KindWiFiClient Kind = "wifi-client"
	KindBLE        Kind = "ble"
	KindService    Kind = "service"
)

// Band groups a frequency into the human-facing radio bands.
type Band string

const (
	BandUnknown Band = "unknown"
	Band24      Band = "2.4GHz"
	Band5       Band = "5GHz"
	Band6       Band = "6GHz"
	BandBLE     Band = "BLE"
	BandIP      Band = "IP"
)

// Sample is one signal-strength reading at a point in time.
type Sample struct {
	At   time.Time `json:"t"`
	RSSI int       `json:"r"`
}

// Observation is a single sighting reported by one scanner. Scanners fill in
// only the fields their radio can actually supply; the registry merges
// successive observations into a Device.
type Observation struct {
	Kind Kind
	// Addr is the stable identifier: BSSID, BLE address, or service key. An
	// empty Addr means the scanner could not identify the device, and Key must
	// be supplied instead.
	Addr string
	// Key overrides the identity derived from Addr. Used for aggregated or
	// anonymous entries where no hardware address is available.
	Key string

	Name         string
	Vendor       string
	Security     string
	RSSI         int
	Noise        int
	Frequency    int // MHz; 0 when unknown
	ChannelWidth int // MHz; 0 when unknown

	// HasRSSI distinguishes "signal is 0 dBm" from "this radio reports no
	// signal at all", which is the case for mDNS.
	HasRSSI bool

	Source string
	Meta   map[string]string
	Seen   time.Time
}

// ID returns the registry key for the observation.
func (o Observation) ID() string {
	if o.Key != "" {
		return string(o.Kind) + "/" + o.Key
	}
	return string(o.Kind) + "/" + NormalizeAddr(o.Addr)
}

// Device is the merged, current view of one physical device or network.
type Device struct {
	ID           string            `json:"id"`
	Kind         Kind              `json:"kind"`
	Addr         string            `json:"addr"`
	Name         string            `json:"name"`
	Vendor       string            `json:"vendor"`
	Security     string            `json:"security"`
	RSSI         int               `json:"rssi"`
	Noise        int               `json:"noise"`
	HasRSSI      bool              `json:"hasRssi"`
	Frequency    int               `json:"frequency"`
	Channel      int               `json:"channel"`
	ChannelWidth int               `json:"channelWidth"`
	Band         Band              `json:"band"`
	Sources      []string          `json:"sources"`
	Meta         map[string]string `json:"meta,omitempty"`
	FirstSeen    time.Time         `json:"firstSeen"`
	LastSeen     time.Time         `json:"lastSeen"`
	History      []Sample          `json:"history,omitempty"`

	// Category is the inferred physical object type, with the confidence and
	// the hints that led there. Mobility says whether the device stays put.
	Category           string   `json:"category"`
	CategoryConfidence float64  `json:"categoryConfidence"`
	Mobility           string   `json:"mobility"`
	Evidence           []string `json:"evidence,omitempty"`
}

// NormalizeAddr lowercases a hardware address and strips separators so that
// "AA:BB:CC:DD:EE:FF" and "aa-bb-cc-dd-ee-ff" resolve to the same device.
func NormalizeAddr(addr string) string {
	var b strings.Builder
	b.Grow(len(addr))
	for _, r := range addr {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == ':' || r == '-' || r == '.' || r == ' ':
			// separator, drop it
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ChannelForFrequency converts a centre frequency in MHz to its 802.11 channel
// number, returning 0 when the frequency falls outside the known plans.
func ChannelForFrequency(mhz int) int {
	switch {
	case mhz == 2484:
		return 14
	case mhz == 5935:
		return 2
	case mhz >= 2412 && mhz <= 2472:
		return (mhz - 2407) / 5
	case mhz >= 5160 && mhz <= 5885:
		return (mhz - 5000) / 5
	case mhz >= 5955 && mhz <= 7115:
		return (mhz - 5950) / 5
	default:
		return 0
	}
}

// FrequencyForChannel is the inverse of ChannelForFrequency. Channel numbers
// are ambiguous across bands, so the caller supplies the intended band.
func FrequencyForChannel(channel int, band Band) int {
	switch band {
	case Band24:
		if channel == 14 {
			return 2484
		}
		if channel >= 1 && channel <= 13 {
			return 2407 + channel*5
		}
	case Band5:
		if channel >= 32 && channel <= 177 {
			return 5000 + channel*5
		}
	case Band6:
		if channel == 2 {
			return 5935
		}
		if channel >= 1 && channel <= 233 {
			return 5950 + channel*5
		}
	}
	return 0
}

// BandForFrequency maps a centre frequency in MHz to its band.
func BandForFrequency(mhz int) Band {
	switch {
	case mhz >= 2400 && mhz < 2500:
		return Band24
	case mhz >= 4900 && mhz < 5925:
		return Band5
	case mhz >= 5925 && mhz <= 7125:
		return Band6
	default:
		return BandUnknown
	}
}
