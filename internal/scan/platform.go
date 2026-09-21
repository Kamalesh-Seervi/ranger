//go:build darwin || linux

package scan

// Options selects which optional backends to enable.
type Options struct {
	// Monitor enables raw 802.11 capture, which observes devices that have
	// not associated with any network.
	Monitor bool
	// MonitorInterface overrides the capture device.
	MonitorInterface string
	// AnonymizeMACs pseudonymises captured client addresses.
	AnonymizeMACs bool
}

// Platform returns the scanner backends available for this operating system.
// Backends that cannot run are still returned so the dashboard can explain the
// missing permission or hardware rather than silently omitting a radio.
func Platform(opts Options) []Scanner {
	scanners := []Scanner{
		NewWiFi(),
		NewBLE(),
		NewMDNS(),
	}
	if opts.Monitor {
		scanners = append(scanners, NewMonitor(opts.MonitorInterface, opts.AnonymizeMACs))
	}
	return scanners
}
