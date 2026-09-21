//go:build !pcap

package scan

import (
	"context"
	"errors"

	"github.com/kd14/ranger/internal/core"
)

// MonitorName is the scanner name reported by the monitor-mode backend.
const MonitorName = "monitor"

// Monitor is the inert stand-in used when the binary is built without libpcap.
// Keeping the backend registered means the dashboard explains how to enable it
// instead of silently omitting Wi-Fi client detection.
type Monitor struct{}

func NewMonitor(string, bool) *Monitor { return &Monitor{} }

func (m *Monitor) Name() string { return MonitorName }

func (m *Monitor) Check(context.Context) Availability {
	return Unavailable("built without packet capture support",
		"Rebuild with libpcap: go build -tags pcap ./cmd/ranger")
}

func (m *Monitor) Run(context.Context, chan<- core.Observation) error {
	return errors.New("monitor mode requires a build with -tags pcap")
}
