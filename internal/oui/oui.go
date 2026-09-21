// Package oui resolves hardware addresses and Bluetooth company identifiers to
// vendor names.
package oui

import (
	"strings"

	"github.com/gopacket/gopacket/macs"
)

// Randomized is reported for locally administered addresses. Modern phones and
// BLE peripherals rotate these for privacy, so no vendor can be derived.
const Randomized = "(randomized)"

// Lookup resolves a MAC address to its registered vendor. It returns an empty
// string when the prefix is unknown, and Randomized for locally administered
// addresses.
func Lookup(addr string) string {
	b, ok := parsePrefix(addr)
	if !ok {
		return ""
	}
	// Bit 1 of the first octet marks a locally administered address.
	if b[0]&0x02 != 0 {
		return Randomized
	}
	if vendor, found := macs.ValidMACPrefixMap[b]; found {
		return vendor
	}
	return ""
}

// parsePrefix extracts the first three octets from a MAC address written with
// or without separators.
func parsePrefix(addr string) ([3]byte, bool) {
	var out [3]byte
	var nibbles int
	var cur byte

	for _, r := range addr {
		var v byte
		switch {
		case r >= '0' && r <= '9':
			v = byte(r - '0')
		case r >= 'a' && r <= 'f':
			v = byte(r-'a') + 10
		case r >= 'A' && r <= 'F':
			v = byte(r-'A') + 10
		case r == ':' || r == '-' || r == '.':
			continue
		default:
			return out, false
		}
		if nibbles%2 == 0 {
			cur = v << 4
		} else {
			out[nibbles/2] = cur | v
			if nibbles/2 == 2 {
				return out, true
			}
		}
		nibbles++
	}
	return out, false
}

// LookupCompany resolves a Bluetooth SIG company identifier, as found in BLE
// advertisement manufacturer data, to a company name.
func LookupCompany(id uint16) string {
	return bleCompanies[id]
}

// bleCompanies covers the identifiers that actually turn up in a typical scan.
// The full Bluetooth SIG registry has thousands of entries and is not worth
// embedding for a visualizer.
var bleCompanies = map[uint16]string{
	0x0001: "Nokia",
	0x0002: "Intel",
	0x0006: "Microsoft",
	0x000F: "Broadcom",
	0x001D: "Qualcomm",
	0x002D: "Sony",
	0x0046: "Sony Ericsson",
	0x004C: "Apple",
	0x0059: "Nordic Semiconductor",
	0x0075: "Samsung",
	0x0078: "Nike",
	0x0087: "Garmin",
	0x00C4: "LG",
	0x00D2: "Dialog Semiconductor",
	0x00E0: "Google",
	0x0117: "Espressif",
	0x0131: "Cypress",
	0x0154: "Xiaomi",
	0x0157: "Anhui Huami",
	0x0171: "Amazon",
	0x0180: "Bose",
	0x01D7: "Logitech",
	0x0201: "JBL",
	0x0224: "Sonos",
	0x02E5: "Fitbit",
	0x02FF: "Silicon Labs",
	0x0310: "SGL Italia",
	0x038F: "Xiaomi Communications",
	0x03DA: "Tile",
	0x0499: "Ruuvi",
	0x05A7: "Sonos",
	0x0644: "Shenzhen Tencent",
	0x0822: "Adafruit",
}

// Normalize trims whitespace and collapses an empty vendor to a stable label.
func Normalize(vendor string) string {
	v := strings.TrimSpace(vendor)
	if v == "" {
		return ""
	}
	return v
}
