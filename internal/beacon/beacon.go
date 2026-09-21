// Package beacon decodes the payloads carried inside BLE advertisements.
//
// A bare advertisement is an anonymous blip. The bytes inside it often carry
// real information: a sensor tag's temperature, a beacon's identity, a
// thermometer's battery level. Decoding turns noise into readings.
//
// Every input here arrives over the air from an unauthenticated source, so
// each decoder length-checks before indexing and never trusts a declared size.
package beacon

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Company identifiers whose manufacturer data carries a known payload.
const (
	companyApple = 0x004C
	companyRuuvi = 0x0499
)

// Service UUIDs, in their 16-bit assigned form.
const (
	uuidEddystone   = "feaa"
	uuidEnvironment = "181a"
)

// Reading is a decoded payload.
type Reading struct {
	// Format names the encoding, such as "iBeacon" or "Ruuvi RAWv2".
	Format string
	// Fields holds display-ready values, already carrying their units.
	Fields map[string]string
}

// DecodeManufacturer decodes vendor-specific advertisement data.
func DecodeManufacturer(companyID uint16, data []byte) *Reading {
	switch companyID {
	case companyApple:
		return decodeIBeacon(data)
	case companyRuuvi:
		return decodeRuuvi(data)
	default:
		return nil
	}
}

// DecodeService decodes service data, keyed by the 16-bit service UUID.
func DecodeService(uuid string, data []byte) *Reading {
	switch strings.ToLower(uuid) {
	case uuidEddystone:
		return decodeEddystone(data)
	case uuidEnvironment:
		return decodeEnvironmental(data)
	default:
		return nil
	}
}

// decodeIBeacon reads Apple's proximity beacon layout: a 0x02 0x15 prefix
// followed by a UUID and a major/minor pair.
func decodeIBeacon(data []byte) *Reading {
	if len(data) < 23 || data[0] != 0x02 || data[1] != 0x15 {
		return nil
	}
	id := data[2:18]

	return &Reading{
		Format: "iBeacon",
		Fields: map[string]string{
			"uuid":  fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]),
			"major": fmt.Sprintf("%d", binary.BigEndian.Uint16(data[18:20])),
			"minor": fmt.Sprintf("%d", binary.BigEndian.Uint16(data[20:22])),
			"power": fmt.Sprintf("%d dBm at 1m", int8(data[22])),
		},
	}
}

// decodeRuuvi reads the RAWv2 sensor format used by Ruuvi environmental tags.
func decodeRuuvi(data []byte) *Reading {
	if len(data) < 18 || data[0] != 0x05 {
		return nil
	}

	fields := map[string]string{}

	// Each measurement has a reserved "invalid" value used when the sensor
	// could not produce a reading.
	if raw := int16(binary.BigEndian.Uint16(data[1:3])); uint16(raw) != 0x8000 {
		fields["temperature"] = fmt.Sprintf("%.2f °C", float64(raw)*0.005)
	}
	if raw := binary.BigEndian.Uint16(data[3:5]); raw != 0xFFFF {
		fields["humidity"] = fmt.Sprintf("%.2f %%", float64(raw)*0.0025)
	}
	if raw := binary.BigEndian.Uint16(data[5:7]); raw != 0xFFFF {
		fields["pressure"] = fmt.Sprintf("%.2f hPa", (float64(raw)+50000)/100)
	}

	power := binary.BigEndian.Uint16(data[13:15])
	if voltage := power >> 5; voltage != 0x7FF {
		fields["battery"] = fmt.Sprintf("%d mV", voltage+1600)
	}
	fields["movements"] = fmt.Sprintf("%d", data[15])

	if len(fields) == 0 {
		return nil
	}
	return &Reading{Format: "Ruuvi RAWv2", Fields: fields}
}

// decodeEddystone dispatches on the frame type in the first byte.
func decodeEddystone(data []byte) *Reading {
	if len(data) < 2 {
		return nil
	}
	switch data[0] {
	case 0x00:
		return decodeEddystoneUID(data)
	case 0x10:
		return decodeEddystoneURL(data)
	case 0x20:
		return decodeEddystoneTLM(data)
	default:
		return nil
	}
}

func decodeEddystoneUID(data []byte) *Reading {
	if len(data) < 18 {
		return nil
	}
	return &Reading{
		Format: "Eddystone-UID",
		Fields: map[string]string{
			"namespace": fmt.Sprintf("%x", data[2:12]),
			"instance":  fmt.Sprintf("%x", data[12:18]),
			"power":     fmt.Sprintf("%d dBm at 0m", int8(data[1])),
		},
	}
}

func decodeEddystoneURL(data []byte) *Reading {
	if len(data) < 3 {
		return nil
	}
	url := expandURL(data[2], data[3:])
	if url == "" {
		return nil
	}
	return &Reading{
		Format: "Eddystone-URL",
		Fields: map[string]string{
			"url":   url,
			"power": fmt.Sprintf("%d dBm at 0m", int8(data[1])),
		},
	}
}

func decodeEddystoneTLM(data []byte) *Reading {
	if len(data) < 14 {
		return nil
	}
	fields := map[string]string{
		"advertisements": fmt.Sprintf("%d", binary.BigEndian.Uint32(data[6:10])),
		// The uptime counter ticks in tenths of a second.
		"uptime": fmt.Sprintf("%.0f s", float64(binary.BigEndian.Uint32(data[10:14]))/10),
	}
	if battery := binary.BigEndian.Uint16(data[2:4]); battery != 0 {
		fields["battery"] = fmt.Sprintf("%d mV", battery)
	}
	if raw := int16(binary.BigEndian.Uint16(data[4:6])); uint16(raw) != 0x8000 {
		// Temperature is 8.8 fixed point.
		fields["temperature"] = fmt.Sprintf("%.1f °C", float64(raw)/256)
	}
	return &Reading{Format: "Eddystone-TLM", Fields: fields}
}

// decodeEnvironmental reads the two common custom thermometer firmwares that
// share the environmental sensing service UUID. They are told apart by length.
func decodeEnvironmental(data []byte) *Reading {
	switch len(data) {
	case 13:
		return &Reading{
			Format: "ATC thermometer",
			Fields: map[string]string{
				"temperature": fmt.Sprintf("%.1f °C", float64(int16(binary.BigEndian.Uint16(data[6:8])))/10),
				"humidity":    fmt.Sprintf("%d %%", data[8]),
				"battery":     fmt.Sprintf("%d %% (%d mV)", data[9], binary.BigEndian.Uint16(data[10:12])),
			},
		}
	case 15:
		return &Reading{
			Format: "pvvx thermometer",
			Fields: map[string]string{
				"temperature": fmt.Sprintf("%.2f °C", float64(int16(binary.LittleEndian.Uint16(data[6:8])))/100),
				"humidity":    fmt.Sprintf("%.2f %%", float64(binary.LittleEndian.Uint16(data[8:10]))/100),
				"battery":     fmt.Sprintf("%d %% (%d mV)", data[12], binary.LittleEndian.Uint16(data[10:12])),
			},
		}
	default:
		return nil
	}
}

var urlSchemes = []string{"http://www.", "https://www.", "http://", "https://"}

// urlExpansions are the single-byte substitutions Eddystone uses to fit a URL
// into an advertisement.
var urlExpansions = []string{
	".com/", ".org/", ".edu/", ".net/", ".info/", ".biz/", ".gov/",
	".com", ".org", ".edu", ".net", ".info", ".biz", ".gov",
}

// expandURL rebuilds a compressed Eddystone URL, dropping anything that is not
// printable so a hostile beacon cannot inject control characters.
func expandURL(scheme byte, encoded []byte) string {
	if int(scheme) >= len(urlSchemes) {
		return ""
	}

	var b strings.Builder
	b.WriteString(urlSchemes[scheme])
	for _, c := range encoded {
		switch {
		case int(c) < len(urlExpansions):
			b.WriteString(urlExpansions[c])
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			return ""
		}
		if b.Len() > 128 {
			break
		}
	}
	return b.String()
}
