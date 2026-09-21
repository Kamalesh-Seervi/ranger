//go:build linux

package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kd14/ranger/internal/core"
	"github.com/kd14/ranger/internal/oui"
	"github.com/mdlayher/wifi"
)

// WiFiName is the scanner name reported by the Wi-Fi backend on every platform.
const WiFiName = "wifi"

// A full scan takes seconds and briefly competes with normal traffic, so it is
// paced rather than run continuously.
const defaultWiFiInterval = 8 * time.Second

// WiFi scans for access points using nl80211.
//
// Triggering a scan needs CAP_NET_ADMIN. Without it the scanner falls back to
// reading the results of scans other processes (NetworkManager, wpa_supplicant)
// have already performed, which is stale but still useful.
type WiFi struct {
	Interval time.Duration

	mu        sync.Mutex
	passive   bool
	ifaceName string
}

func NewWiFi() *WiFi {
	return &WiFi{Interval: defaultWiFiInterval}
}

func (w *WiFi) Name() string { return WiFiName }

func (w *WiFi) Check(context.Context) Availability {
	client, err := wifi.New()
	if err != nil {
		return Unavailable("cannot open nl80211: "+err.Error(),
			"Ensure the kernel exposes cfg80211 and that a wireless driver is loaded.")
	}
	defer client.Close()

	iface, err := stationInterface(client)
	if err != nil {
		return Unavailable(err.Error(),
			"Connect a wireless adapter, or check that it is not rfkill-blocked.")
	}

	w.mu.Lock()
	w.ifaceName = iface.Name
	w.mu.Unlock()

	// Probe once so the permission problem is reported at startup rather than
	// showing up as a scan failure minutes later.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Scan(ctx, iface); err != nil && isPermissionError(err) {
		w.mu.Lock()
		w.passive = true
		w.mu.Unlock()
	}
	return Available()
}

// Notice reports the downgrade to passive mode.
func (w *WiFi) Notice() (string, string) {
	w.mu.Lock()
	passive := w.passive
	w.mu.Unlock()

	if !passive {
		return "", ""
	}
	return "passive mode: cannot trigger scans without CAP_NET_ADMIN",
		"Results come from scans other programs run, so they update slowly. Grant the " +
			"capability once with: sudo setcap cap_net_admin,cap_net_raw+eip $(which ranger)"
}

func (w *WiFi) Run(ctx context.Context, out chan<- core.Observation) error {
	if w.Interval <= 0 {
		w.Interval = defaultWiFiInterval
	}

	client, err := wifi.New()
	if err != nil {
		return fmt.Errorf("open nl80211: %w", err)
	}
	defer client.Close()

	for {
		iface, err := stationInterface(client)
		if err != nil {
			return err
		}

		if !w.isPassive() {
			scanCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := client.Scan(scanCtx, iface)
			cancel()

			if err != nil && ctx.Err() == nil {
				if isPermissionError(err) {
					w.setPassive()
				} else if !errors.Is(err, context.DeadlineExceeded) &&
					!errors.Is(err, wifi.ErrScanAborted) {
					return fmt.Errorf("trigger scan on %s: %w", iface.Name, err)
				}
			}
		}

		points, err := client.AccessPoints(iface)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read access points on %s: %w", iface.Name, err)
		}

		now := time.Now()
		for _, bss := range points {
			if !emit(ctx, out, bssToObservation(bss, now)) {
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

func (w *WiFi) isPassive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.passive
}

func (w *WiFi) setPassive() {
	w.mu.Lock()
	w.passive = true
	w.mu.Unlock()
}

func bssToObservation(bss *wifi.BSS, now time.Time) core.Observation {
	addr := bss.BSSID.String()

	// nl80211 reports signal in mBm.
	rssi := int(bss.Signal / 100)
	hasRSSI := bss.Signal != 0
	if !hasRSSI && bss.SignalUnspecified > 0 {
		// Percentage fallback mapped onto a plausible dBm range.
		rssi = int(bss.SignalUnspecified)/2 - 100
		hasRSSI = true
	}

	meta := map[string]string{}
	if bss.Load.StationCount > 0 {
		meta["stations"] = fmt.Sprintf("%d", bss.Load.StationCount)
	}
	if bss.Load.ChannelUtilization > 0 {
		meta["utilization"] = fmt.Sprintf("%d%%", int(bss.Load.ChannelUtilization)*100/255)
	}

	return core.Observation{
		Kind:      core.KindWiFiAP,
		Addr:      addr,
		Name:      sanitizeText(bss.SSID),
		Vendor:    oui.Lookup(addr),
		Security:  securityLabel(bss),
		RSSI:      rssi,
		HasRSSI:   hasRSSI,
		Frequency: bss.Frequency,
		Source:    WiFiName,
		Meta:      meta,
		Seen:      now,
	}
}

func securityLabel(bss *wifi.BSS) string {
	if !bss.RSN.IsInitialized() {
		return "Open"
	}

	var labels []string
	for _, akm := range bss.RSN.AKMs {
		switch akm {
		case wifi.RSNAkmSAE, wifi.RSNAkmFTSAE:
			labels = appendUnique(labels, "WPA3")
		case wifi.RSNAkmPSK, wifi.RSNAkmPSKSHA256, wifi.RSNAkmFTPSK,
			wifi.RSNAkmPSKSHA384, wifi.RSNAkmFTPSKSHA384:
			labels = appendUnique(labels, "WPA2")
		case wifi.RSNAkm8021X, wifi.RSNAkm8021XSHA256, wifi.RSNAkmFT8021X,
			wifi.RSNAkm8021XSuiteB, wifi.RSNAkm8021XCNSA, wifi.RSNAkmFT8021XSHA384:
			labels = appendUnique(labels, "WPA2-Enterprise")
		}
	}
	if len(labels) == 0 {
		return "WPA2"
	}
	return strings.Join(labels, "/")
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// stationInterface picks the first usable client-mode wireless interface.
func stationInterface(client *wifi.Client) (*wifi.Interface, error) {
	interfaces, err := client.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list wireless interfaces: %w", err)
	}
	for _, ifi := range interfaces {
		if ifi.Name == "" {
			continue
		}
		if ifi.Type == wifi.InterfaceTypeStation || ifi.Type == wifi.InterfaceTypeUnspecified {
			return ifi, nil
		}
	}
	return nil, errors.New("no wireless interface in station mode")
}

func isPermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, wifi.ErrNotSupported)
}
