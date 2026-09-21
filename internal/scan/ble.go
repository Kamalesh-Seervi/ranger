package scan

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kd14/ranger/internal/beacon"
	"github.com/kd14/ranger/internal/core"
	"github.com/kd14/ranger/internal/oui"
	"tinygo.org/x/bluetooth"
)

// BLEName is the scanner name reported by the Bluetooth Low Energy backend.
const BLEName = "ble"

// enableTimeout bounds how long we wait for the adapter to power on. On macOS
// a denied Bluetooth permission never resolves, so without this the scanner
// would hang forever instead of reporting a fixable problem.
const enableTimeout = 6 * time.Second

// BLE listens for Bluetooth Low Energy advertisements.
//
// Address semantics differ by platform: Linux/BlueZ reports the hardware
// address (often a rotating random one), while macOS/CoreBluetooth reports a
// per-host UUID that has no vendor prefix at all. Vendor identification
// therefore leans on the advertised company ID rather than the address.
type BLE struct {
	adapter *bluetooth.Adapter
	enabled bool
}

func NewBLE() *BLE {
	return &BLE{adapter: bluetooth.DefaultAdapter}
}

func (b *BLE) Name() string { return BLEName }

func (b *BLE) Check(ctx context.Context) Availability {
	if b.enabled {
		return Available()
	}

	// Enable can block indefinitely when the adapter never powers on, so it
	// runs detached. The goroutine ends on its own once the radio settles.
	done := make(chan error, 1)
	go func() { done <- b.adapter.Enable() }()

	select {
	case err := <-done:
		if err != nil {
			return Unavailable("cannot enable Bluetooth adapter: "+err.Error(), bluetoothHint())
		}
		b.enabled = true
		return Available()
	case <-time.After(enableTimeout):
		return Unavailable("Bluetooth adapter did not become ready", bluetoothHint())
	case <-ctx.Done():
		return Unavailable("startup cancelled", "")
	}
}

func (b *BLE) Run(ctx context.Context, out chan<- core.Observation) error {
	if !b.enabled {
		if err := b.adapter.Enable(); err != nil {
			return fmt.Errorf("enable bluetooth adapter: %w", err)
		}
		b.enabled = true
	}

	// Scan blocks until StopScan is called, so cancellation is bridged here.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			_ = b.adapter.StopScan()
		case <-finished:
		}
	}()

	err := b.adapter.Scan(func(_ *bluetooth.Adapter, result bluetooth.ScanResult) {
		obs := resultToObservation(result)
		// The callback runs on the adapter's own goroutine and must not block.
		// Advertisements repeat every few hundred milliseconds, so dropping one
		// under backpressure costs nothing.
		select {
		case out <- obs:
		default:
		}
	})

	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ble scan: %w", err)
	}
	return nil
}

// serviceBlob is one service-data element reduced to primitives, so the
// interpretation below can be exercised without platform Bluetooth types.
type serviceBlob struct {
	UUID string
	Data []byte
}

func resultToObservation(result bluetooth.ScanResult) core.Observation {
	addr := result.Address.String()

	var companyID uint16
	var companyData []byte
	if mfr := result.ManufacturerData(); len(mfr) > 0 {
		companyID, companyData = mfr[0].CompanyID, mfr[0].Data
	}

	blobs := make([]serviceBlob, 0, len(result.ServiceData()))
	for _, element := range result.ServiceData() {
		blobs = append(blobs, serviceBlob{UUID: element.UUID.String(), Data: element.Data})
	}

	uuids := result.ServiceUUIDs()
	labels := make([]string, 0, 4)
	for i, u := range uuids {
		if i == 4 {
			break
		}
		labels = append(labels, u.String())
	}

	meta, vendor := interpretAdvertisement(addr, companyID, companyData, blobs, labels)

	return core.Observation{
		Kind:    core.KindBLE,
		Addr:    addr,
		Name:    sanitizeText(result.LocalName()),
		Vendor:  vendor,
		RSSI:    int(result.RSSI),
		HasRSSI: true,
		Source:  BLEName,
		Meta:    meta,
		Seen:    time.Now(),
	}
}

// interpretAdvertisement turns the parts of an advertisement into metadata and
// a best guess at the vendor.
func interpretAdvertisement(addr string, companyID uint16, companyData []byte, services []serviceBlob, uuids []string) (map[string]string, string) {
	meta := make(map[string]string, 6)
	vendor := oui.Lookup(addr)

	if companyID != 0 || len(companyData) > 0 {
		meta["companyId"] = fmt.Sprintf("0x%04X", companyID)
		if company := oui.LookupCompany(companyID); company != "" {
			// The advertised company is more informative than a randomized
			// address, so it wins.
			vendor = company
		}
		if kind := appleAdvertisementKind(companyID, companyData); kind != "" {
			meta["apple"] = kind
		}
		addReading(meta, beacon.DecodeManufacturer(companyID, companyData))
	}

	for _, blob := range services {
		if addReading(meta, beacon.DecodeService(shortServiceUUID(blob.UUID), blob.Data)) {
			break
		}
	}

	if len(uuids) > 0 {
		meta["services"] = strings.Join(uuids, ", ")
	}
	return meta, vendor
}

// addReading folds a decoded beacon payload into device metadata, reporting
// whether anything was found.
func addReading(meta map[string]string, reading *beacon.Reading) bool {
	if reading == nil {
		return false
	}
	meta["payload"] = reading.Format
	for key, value := range reading.Fields {
		meta[key] = sanitizeText(value)
	}
	return true
}

// shortServiceUUID reduces a 128-bit UUID to its 16-bit assigned form.
func shortServiceUUID(raw string) string {
	u := strings.ToLower(strings.TrimSpace(raw))
	if len(u) == 36 && strings.HasPrefix(u, "0000") {
		return u[4:8]
	}
	return u
}

// appleAdvertisementKind names Apple's continuity advertisement subtypes,
// which identify what an unnamed Apple device is actually doing.
func appleAdvertisementKind(companyID uint16, data []byte) string {
	if companyID != 0x004C || len(data) == 0 {
		return ""
	}
	switch data[0] {
	case 0x02:
		return "iBeacon"
	case 0x05:
		return "AirDrop"
	case 0x07:
		return "Proximity Pairing"
	case 0x09:
		return "AirPlay"
	case 0x0C:
		return "Handoff"
	case 0x10:
		return "Nearby"
	case 0x12:
		return "Find My"
	default:
		return ""
	}
}

func bluetoothHint() string {
	return "Turn Bluetooth on, then grant your terminal app Bluetooth access under " +
		"System Settings → Privacy & Security → Bluetooth (macOS) or ensure bluetoothd is running (Linux)."
}
