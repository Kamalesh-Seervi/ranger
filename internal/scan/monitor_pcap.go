//go:build pcap

package scan

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcap"
	"github.com/kd14/ranger/internal/core"
	"github.com/kd14/ranger/internal/oui"
)

// MonitorName is the scanner name reported by the monitor-mode backend.
const MonitorName = "monitor"

const (
	snapLen       = 512
	readTimeout   = 200 * time.Millisecond
	defaultHopGap = 350 * time.Millisecond
)

// hopChannels covers the non-overlapping 2.4 GHz channels plus the common
// 5 GHz ones. Dwelling on every channel would make each revisit too slow to
// catch short-lived probe requests.
var hopChannels = []int{1, 6, 11, 36, 40, 44, 48, 149, 153, 157, 161}

// Monitor captures raw 802.11 frames to discover devices that never associate
// with a network, such as phones sending probe requests.
//
// This observes traffic from devices that have not consented to being tracked.
// It is opt-in, MAC addresses are pseudonymised by default, and no frame
// payloads are ever read or stored.
type Monitor struct {
	// Interface is the capture device. Empty picks a sensible default.
	Interface string
	// Anonymize replaces the device half of every client MAC with a salted
	// hash, keeping the vendor prefix but discarding identity.
	Anonymize bool
	// HopInterval is the dwell time per channel. Zero disables hopping.
	HopInterval time.Duration

	salt [16]byte
}

func NewMonitor(iface string, anonymize bool) *Monitor {
	m := &Monitor{Interface: iface, Anonymize: anonymize, HopInterval: defaultHopGap}
	// A fresh salt each run means pseudonyms cannot be correlated across
	// sessions or against a precomputed table of MAC addresses.
	if _, err := rand.Read(m.salt[:]); err != nil {
		binary.LittleEndian.PutUint64(m.salt[:8], uint64(time.Now().UnixNano()))
	}
	return m
}

func (m *Monitor) Name() string { return MonitorName }

func (m *Monitor) Check(context.Context) Availability {
	name, err := m.resolveInterface()
	if err != nil {
		return Unavailable(err.Error(), "Specify one with -monitor-interface.")
	}

	handle, err := openMonitor(name)
	if err != nil {
		return Unavailable(fmt.Sprintf("cannot capture on %s: %v", name, err), captureHint())
	}
	handle.Close()
	return Available()
}

func (m *Monitor) Run(ctx context.Context, out chan<- core.Observation) error {
	name, err := m.resolveInterface()
	if err != nil {
		return err
	}

	handle, err := openMonitor(name)
	if err != nil {
		return fmt.Errorf("open monitor capture on %s: %w", name, err)
	}
	defer handle.Close()

	if m.HopInterval > 0 && canHop() {
		go m.hop(ctx, name)
	}

	source := gopacket.NewPacketSource(handle, handle.LinkType())
	source.NoCopy = true
	packets := source.Packets()

	for {
		select {
		case <-ctx.Done():
			return nil
		case packet, open := <-packets:
			if !open {
				return nil
			}
			for _, obs := range m.observe(packet) {
				if !emit(ctx, out, obs) {
					return nil
				}
			}
		}
	}
}

// observe turns one captured frame into zero or more sightings.
func (m *Monitor) observe(packet gopacket.Packet) []core.Observation {
	dot11Layer := packet.Layer(layers.LayerTypeDot11)
	if dot11Layer == nil {
		return nil
	}
	dot11, ok := dot11Layer.(*layers.Dot11)
	if !ok {
		return nil
	}

	rssi, frequency, hasSignal := radioInfo(packet)
	now := time.Now()

	base := core.Observation{
		RSSI:      rssi,
		HasRSSI:   hasSignal,
		Frequency: frequency,
		Source:    MonitorName,
		Seen:      now,
	}

	switch dot11.Type {
	case layers.Dot11TypeMgmtBeacon, layers.Dot11TypeMgmtProbeResp:
		bssid := dot11.Address2.String()
		if isBlankMAC(bssid) {
			return nil
		}
		obs := base
		obs.Kind = core.KindWiFiAP
		obs.Addr = bssid
		obs.Name = sanitizeText(ssidOf(packet))
		obs.Vendor = oui.Lookup(bssid)
		return []core.Observation{obs}

	case layers.Dot11TypeMgmtProbeReq:
		client := dot11.Address2.String()
		if isBlankMAC(client) {
			return nil
		}
		obs := base
		obs.Kind = core.KindWiFiClient
		obs.Addr = m.maskMAC(client)
		obs.Vendor = oui.Lookup(client)
		// A probe request names the network a device is hunting for, which is
		// the single most revealing thing monitor mode exposes.
		if probed := sanitizeText(ssidOf(packet)); probed != "" {
			obs.Meta = map[string]string{"probing": probed}
		}
		return []core.Observation{obs}

	default:
		client, bssid := stationAndBSSID(dot11)
		if client == "" {
			return nil
		}
		obs := base
		obs.Kind = core.KindWiFiClient
		obs.Addr = m.maskMAC(client)
		obs.Vendor = oui.Lookup(client)
		if bssid != "" {
			obs.Meta = map[string]string{"bssid": bssid}
		}
		return []core.Observation{obs}
	}
}

// stationAndBSSID resolves which address is the client and which is the access
// point, using the to-DS and from-DS flags.
func stationAndBSSID(dot11 *layers.Dot11) (client, bssid string) {
	toDS := dot11.Flags.ToDS()
	fromDS := dot11.Flags.FromDS()

	switch {
	case toDS && !fromDS:
		client, bssid = dot11.Address2.String(), dot11.Address1.String()
	case !toDS && fromDS:
		client, bssid = dot11.Address1.String(), dot11.Address2.String()
	case !toDS && !fromDS:
		client, bssid = dot11.Address2.String(), dot11.Address3.String()
	default:
		// Both set means a wireless bridge; neither address is a client.
		return "", ""
	}

	if isBlankMAC(client) || isBroadcast(client) {
		return "", ""
	}
	if isBlankMAC(bssid) {
		bssid = ""
	}
	return client, bssid
}

// maskMAC keeps the vendor prefix and replaces the device identifier with a
// salted hash, so a device stays trackable within a session without its real
// address ever being recorded.
func (m *Monitor) maskMAC(addr string) string {
	if !m.Anonymize {
		return addr
	}
	hw, err := net.ParseMAC(addr)
	if err != nil || len(hw) < 6 {
		return addr
	}

	h := fnv.New64a()
	h.Write(m.salt[:])
	h.Write(hw)
	sum := h.Sum64()

	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		hw[0], hw[1], hw[2],
		byte(sum>>16), byte(sum>>8), byte(sum))
}

func radioInfo(packet gopacket.Packet) (rssi, frequency int, ok bool) {
	layer := packet.Layer(layers.LayerTypeRadioTap)
	if layer == nil {
		return 0, 0, false
	}
	tap, valid := layer.(*layers.RadioTap)
	if !valid {
		return 0, 0, false
	}

	// Present flags and value namespaces are parallel slices, one pair per
	// radiotap segment.
	for i, present := range tap.Present {
		if i >= len(tap.RadioTapValues) {
			break
		}
		values := tap.RadioTapValues[i]
		if frequency == 0 && present.Channel() {
			frequency = int(values.ChannelFrequency)
		}
		if !ok && present.DBMAntennaSignal() {
			rssi, ok = int(values.DBMAntennaSignal), true
		}
	}
	return rssi, frequency, ok
}

// ssidOf pulls the SSID element out of a management frame.
func ssidOf(packet gopacket.Packet) string {
	for _, layer := range packet.Layers() {
		element, ok := layer.(*layers.Dot11InformationElement)
		if !ok {
			continue
		}
		if element.ID == layers.Dot11InformationElementIDSSID {
			return string(element.Info)
		}
	}
	return ""
}

func openMonitor(name string) (*pcap.Handle, error) {
	inactive, err := pcap.NewInactiveHandle(name)
	if err != nil {
		return nil, err
	}
	defer inactive.CleanUp()

	if err := inactive.SetRFMon(true); err != nil {
		return nil, fmt.Errorf("enable monitor mode: %w", err)
	}
	if err := inactive.SetPromisc(true); err != nil {
		return nil, fmt.Errorf("enable promiscuous mode: %w", err)
	}
	// Headers are all that is needed; capturing payloads would collect other
	// people's traffic for no benefit.
	if err := inactive.SetSnapLen(snapLen); err != nil {
		return nil, err
	}
	if err := inactive.SetTimeout(readTimeout); err != nil {
		return nil, err
	}
	if err := inactive.SetImmediateMode(true); err != nil {
		return nil, err
	}
	return inactive.Activate()
}

// libpcap interface flags. These are PCAP_IF_* values and must not be confused
// with net.Flags, which uses entirely different bit positions.
const (
	pcapIfLoopback = 0x01
	pcapIfUp       = 0x02
	pcapIfRunning  = 0x04
	pcapIfWireless = 0x08
)

// virtualWireless are radios macOS exposes that cannot be used for a survey:
// AirDrop's peer-to-peer link, the hotspot interface, and low-latency WLAN.
var virtualWireless = map[string]bool{"awdl0": true, "ap1": true, "llw0": true}

// resolveInterface picks the capture device, preferring an explicit choice and
// otherwise the first real wireless interface that is up.
func (m *Monitor) resolveInterface() (string, error) {
	if m.Interface != "" {
		return m.Interface, nil
	}

	devices, err := pcap.FindAllDevs()
	if err != nil {
		return "", fmt.Errorf("list capture devices: %w", err)
	}

	var fallback string
	for _, device := range devices {
		if device.Flags&pcapIfLoopback != 0 || virtualWireless[device.Name] {
			continue
		}
		if device.Flags&pcapIfWireless == 0 {
			continue
		}
		if device.Flags&(pcapIfUp|pcapIfRunning) == (pcapIfUp | pcapIfRunning) {
			return device.Name, nil
		}
		if fallback == "" {
			fallback = device.Name
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errors.New("no wireless capture interface found")
}

// hop cycles the radio through channels so the capture is not stuck watching
// a single one.
func (m *Monitor) hop(ctx context.Context, iface string) {
	ticker := time.NewTicker(m.HopInterval)
	defer ticker.Stop()

	index := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			channel := hopChannels[index%len(hopChannels)]
			index++
			setChannel(ctx, iface, channel)
		}
	}
}

// setChannel retunes the radio. Arguments are fixed and numeric, never passed
// through a shell.
func setChannel(ctx context.Context, iface string, channel int) {
	if runtime.GOOS != "linux" {
		return
	}
	cmd := exec.CommandContext(ctx, "iw", "dev", iface, "set", "channel", strconv.Itoa(channel))
	_ = cmd.Run()
}

// canHop reports whether this platform can retune the radio. macOS removed the
// airport utility and exposes no supported channel-setting API, so capture
// there stays on whichever channel the interface already uses.
func canHop() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := exec.LookPath("iw")
	return err == nil
}

func captureHint() string {
	if runtime.GOOS == "darwin" {
		return "Monitor mode needs root: sudo ranger -monitor. It also disconnects Wi-Fi while active, " +
			"and macOS cannot change channels, so only the current channel is observed."
	}
	return "Monitor mode needs privileges: run with sudo, or grant them once with " +
		"sudo setcap cap_net_raw,cap_net_admin+eip $(which ranger)"
}

func isBlankMAC(addr string) bool {
	return addr == "" || addr == "00:00:00:00:00:00"
}

func isBroadcast(addr string) bool {
	return addr == "ff:ff:ff:ff:ff:ff"
}
