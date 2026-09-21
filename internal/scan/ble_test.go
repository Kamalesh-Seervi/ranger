package scan

import "testing"

func TestInterpretAdvertisementDecodesIBeacon(t *testing.T) {
	payload := []byte{
		0x02, 0x15,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x00, 0x2a,
		0x01, 0x00,
		0xc5,
	}
	meta, vendor := interpretAdvertisement("aa:bb:cc:dd:ee:ff", 0x004C, payload, nil, nil)

	if meta["payload"] != "iBeacon" {
		t.Errorf("payload = %q, want iBeacon", meta["payload"])
	}
	if meta["major"] != "42" {
		t.Errorf("major = %q, want 42", meta["major"])
	}
	if meta["apple"] != "iBeacon" {
		t.Errorf("apple = %q, want the continuity subtype too", meta["apple"])
	}
	if vendor != "Apple" {
		t.Errorf("vendor = %q, want Apple from the company ID", vendor)
	}
}

func TestInterpretAdvertisementDecodesServiceData(t *testing.T) {
	// Eddystone telemetry carried in service data rather than manufacturer data.
	tlm := []byte{
		0x20, 0x00,
		0x0B, 0xB8,
		0x18, 0x00,
		0x00, 0x00, 0x01, 0x00,
		0x00, 0x00, 0x03, 0xE8,
	}
	services := []serviceBlob{
		{UUID: "0000180f-0000-1000-8000-00805f9b34fb", Data: []byte{0x64}},
		{UUID: "0000feaa-0000-1000-8000-00805f9b34fb", Data: tlm},
	}
	meta, _ := interpretAdvertisement("11:22:33:44:55:66", 0, nil, services, nil)

	if meta["payload"] != "Eddystone-TLM" {
		t.Fatalf("payload = %q, want Eddystone-TLM", meta["payload"])
	}
	if meta["battery"] != "3000 mV" {
		t.Errorf("battery = %q", meta["battery"])
	}
	if meta["temperature"] != "24.0 °C" {
		t.Errorf("temperature = %q", meta["temperature"])
	}
}

func TestInterpretAdvertisementWithoutPayload(t *testing.T) {
	// Apple continuity is not a beacon format, so nothing should be decoded,
	// but the subtype must still be recorded.
	meta, vendor := interpretAdvertisement("02:11:22:33:44:55", 0x004C, []byte{0x12, 0x00}, nil, nil)

	if _, present := meta["payload"]; present {
		t.Errorf("unexpected payload decoded: %v", meta)
	}
	if meta["apple"] != "Find My" {
		t.Errorf("apple = %q, want Find My", meta["apple"])
	}
	if vendor != "Apple" {
		t.Errorf("vendor = %q, want Apple", vendor)
	}
}

func TestInterpretAdvertisementEmpty(t *testing.T) {
	meta, vendor := interpretAdvertisement("aa:bb:cc:dd:ee:ff", 0, nil, nil, nil)
	if len(meta) != 0 {
		t.Errorf("meta = %v, want empty", meta)
	}
	// A locally administered address cannot be traced to a vendor.
	if vendor != "(randomized)" {
		t.Errorf("vendor = %q", vendor)
	}
}

func TestInterpretAdvertisementRecordsServiceUUIDs(t *testing.T) {
	meta, _ := interpretAdvertisement("11:22:33:44:55:66", 0, nil, nil, []string{"180d", "1812"})
	if meta["services"] != "180d, 1812" {
		t.Errorf("services = %q", meta["services"])
	}
}

func TestShortServiceUUID(t *testing.T) {
	cases := map[string]string{
		"0000feaa-0000-1000-8000-00805f9b34fb": "feaa",
		"0000FEAA-0000-1000-8000-00805F9B34FB": "feaa",
		"feaa":                                 "feaa",
		"6e400001-b5a3-f393-e0a9-e50e24dcca9e": "6e400001-b5a3-f393-e0a9-e50e24dcca9e",
	}
	for in, want := range cases {
		if got := shortServiceUUID(in); got != want {
			t.Errorf("shortServiceUUID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAppleAdvertisementKind(t *testing.T) {
	if got := appleAdvertisementKind(0x004C, []byte{0x07, 0x19}); got != "Proximity Pairing" {
		t.Errorf("got %q, want Proximity Pairing", got)
	}
	// Another vendor reusing the same first byte must not be read as Apple.
	if got := appleAdvertisementKind(0x0075, []byte{0x07}); got != "" {
		t.Errorf("got %q for a non-Apple company, want empty", got)
	}
	if got := appleAdvertisementKind(0x004C, nil); got != "" {
		t.Errorf("got %q for empty data, want empty", got)
	}
}
