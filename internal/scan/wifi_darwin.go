//go:build darwin

package scan

/*
#cgo LDFLAGS: -framework CoreWLAN -framework CoreLocation -framework Foundation
#include <stdlib.h>
#include "wifi_darwin.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/kd14/ranger/internal/core"
	"github.com/kd14/ranger/internal/oui"
)

// WiFiName is the scanner name reported by the Wi-Fi backend on every platform.
const WiFiName = "wifi"

// CoreLocation authorization statuses.
const (
	locationNotDetermined = 0
	locationRestricted    = 1
	locationDenied        = 2
)

// CWChannelBand values.
const (
	bandUnknown = 0
	band2GHz    = 1
	band5GHz    = 2
	band6GHz    = 3
)

// A full scan takes seconds and briefly competes with normal traffic, so it is
// paced rather than run continuously.
const defaultWiFiInterval = 8 * time.Second

// darwinNetwork mirrors the JSON emitted by the CoreWLAN bridge.
type darwinNetwork struct {
	SSID     string `json:"ssid"`
	BSSID    string `json:"bssid"`
	RSSI     int    `json:"rssi"`
	Noise    int    `json:"noise"`
	Country  string `json:"country"`
	Security string `json:"security"`
	Channel  int    `json:"channel"`
	Band     int    `json:"band"`
	Width    int    `json:"width"`
}

// WiFi scans for access points using CoreWLAN.
//
// macOS only reveals SSIDs and BSSIDs to a process that holds Location
// Services authorization. Without it CoreWLAN still reports signal strength
// and channel for every nearby network, so the scanner degrades to anonymous
// per-channel aggregates instead of failing.
type WiFi struct {
	Interval time.Duration

	mu       sync.Mutex
	degraded bool
	asked    bool
}

func NewWiFi() *WiFi {
	return &WiFi{Interval: defaultWiFiInterval}
}

func (w *WiFi) Name() string { return WiFiName }

func (w *WiFi) Check(context.Context) Availability {
	name := interfaceName()
	if name == "" {
		return Unavailable("no Wi-Fi interface found",
			"This machine has no Wi-Fi hardware, or it is disabled in System Settings.")
	}

	status := int(C.ranger_location_status())
	if status == locationNotDetermined {
		// Asking is harmless and may produce the prompt straight away.
		w.mu.Lock()
		if !w.asked {
			w.asked = true
			C.ranger_location_request()
		}
		w.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
		C.ranger_location_stop()
		status = int(C.ranger_location_status())
	}

	w.mu.Lock()
	w.degraded = status == locationDenied || status == locationRestricted || status == locationNotDetermined
	w.mu.Unlock()

	return Available()
}

// Notice reports the loss of network identity when Location Services is off.
func (w *WiFi) Notice() (string, string) {
	w.mu.Lock()
	degraded := w.degraded
	w.mu.Unlock()

	if !degraded {
		return "", ""
	}
	return "network names hidden: Location Services not authorized",
		"macOS reveals SSIDs and BSSIDs only to authorized apps, so nearby networks are " +
			"grouped by channel instead. Ranger has already registered itself, so open " +
			"System Settings → Privacy & Security → Location Services, enable your terminal " +
			"app, and restart ranger. Signal strength and channel occupancy work without it."
}

func (w *WiFi) Run(ctx context.Context, out chan<- core.Observation) error {
	if w.Interval <= 0 {
		w.Interval = defaultWiFiInterval
	}

	for {
		networks, err := darwinScan()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("corewlan scan: %w", err)
		}

		for _, obs := range buildObservations(networks) {
			if !emit(ctx, out, obs) {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(w.Interval):
		}
	}
}

// buildObservations converts a scan result, aggregating networks the system
// refused to identify into one anonymous entry per channel.
func buildObservations(networks []darwinNetwork) []core.Observation {
	now := time.Now()
	out := make([]core.Observation, 0, len(networks))
	anonymous := make(map[string]*core.Observation)

	for _, n := range networks {
		band := bandOf(n.Band, n.Channel)
		frequency := core.FrequencyForChannel(n.Channel, band)
		width := widthMHz(n.Width)

		if n.BSSID == "" && n.SSID == "" {
			key := fmt.Sprintf("anon@%s-%d", band, n.Channel)
			if existing, ok := anonymous[key]; ok {
				existing.Meta["count"] = incr(existing.Meta["count"])
				if n.RSSI > existing.RSSI {
					existing.RSSI = n.RSSI
				}
				continue
			}
			anonymous[key] = &core.Observation{
				Kind:         core.KindWiFiAP,
				Key:          key,
				Frequency:    frequency,
				ChannelWidth: width,
				RSSI:         n.RSSI,
				Noise:        n.Noise,
				HasRSSI:      true,
				Security:     n.Security,
				Source:       WiFiName,
				Seen:         now,
				Meta:         map[string]string{"anonymous": "true", "count": "1"},
			}
			continue
		}

		meta := map[string]string{}
		if n.Country != "" {
			meta["country"] = n.Country
		}

		out = append(out, core.Observation{
			Kind:         core.KindWiFiAP,
			Addr:         n.BSSID,
			Name:         sanitizeText(n.SSID),
			Vendor:       oui.Lookup(n.BSSID),
			Security:     n.Security,
			RSSI:         n.RSSI,
			Noise:        n.Noise,
			HasRSSI:      true,
			Frequency:    frequency,
			ChannelWidth: width,
			Source:       WiFiName,
			Seen:         now,
			Meta:         meta,
		})
	}

	for _, obs := range anonymous {
		out = append(out, *obs)
	}
	return out
}

func bandOf(raw, channel int) core.Band {
	switch raw {
	case band2GHz:
		return core.Band24
	case band5GHz:
		return core.Band5
	case band6GHz:
		return core.Band6
	}
	// Older systems report an unknown band; infer it from the channel number.
	if channel > 0 && channel <= 14 {
		return core.Band24
	}
	return core.Band5
}

// widthMHz maps the CWChannelWidth enum to megahertz.
func widthMHz(raw int) int {
	switch raw {
	case 1:
		return 20
	case 2:
		return 40
	case 3:
		return 80
	case 4:
		return 160
	default:
		return 0
	}
}

func incr(value string) string {
	n := 0
	fmt.Sscanf(value, "%d", &n)
	return fmt.Sprintf("%d", n+1)
}

func darwinScan() ([]darwinNetwork, error) {
	var cErr *C.char
	cJSON := C.ranger_wifi_scan(&cErr)
	if cJSON == nil {
		message := "scan failed"
		if cErr != nil {
			message = C.GoString(cErr)
			C.free(unsafe.Pointer(cErr))
		}
		return nil, errors.New(message)
	}
	defer C.free(unsafe.Pointer(cJSON))

	var networks []darwinNetwork
	if err := json.Unmarshal([]byte(C.GoString(cJSON)), &networks); err != nil {
		return nil, fmt.Errorf("decode scan results: %w", err)
	}
	return networks, nil
}

func interfaceName() string {
	cName := C.ranger_wifi_interface()
	if cName == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cName))
	return C.GoString(cName)
}
