package beacon

import (
	"strings"
	"testing"
)

func TestDecodeIBeacon(t *testing.T) {
	data := []byte{
		0x02, 0x15,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x00, 0x2a, // major 42
		0x01, 0x00, // minor 256
		0xc5, // -59 dBm
	}
	got := DecodeManufacturer(companyApple, data)
	if got == nil {
		t.Fatal("expected an iBeacon reading")
	}
	if got.Format != "iBeacon" {
		t.Errorf("format = %q", got.Format)
	}
	if got.Fields["major"] != "42" {
		t.Errorf("major = %q, want 42", got.Fields["major"])
	}
	if got.Fields["minor"] != "256" {
		t.Errorf("minor = %q, want 256", got.Fields["minor"])
	}
	if !strings.Contains(got.Fields["power"], "-59") {
		t.Errorf("power = %q, want -59 dBm", got.Fields["power"])
	}
}

func TestDecodeRuuvi(t *testing.T) {
	// RAWv2: 24.30 °C, 53.49 %, 1000.44 hPa, 2977 mV.
	data := []byte{
		0x05,
		0x12, 0xFC, // temperature
		0x53, 0x94, // humidity
		0xC3, 0x7C, // pressure
		0x00, 0x04, 0xFF, 0xFC, 0x04, 0x0C, // acceleration
		0xAC, 0x36, // power
		0x42,       // movement counter
		0x00, 0xCD, // sequence
	}
	got := DecodeManufacturer(companyRuuvi, data)
	if got == nil {
		t.Fatal("expected a Ruuvi reading")
	}
	if !strings.HasPrefix(got.Fields["temperature"], "24.3") {
		t.Errorf("temperature = %q, want about 24.3 °C", got.Fields["temperature"])
	}
	if !strings.HasPrefix(got.Fields["humidity"], "53.4") {
		t.Errorf("humidity = %q, want about 53.4 %%", got.Fields["humidity"])
	}
	if !strings.HasPrefix(got.Fields["pressure"], "1000.4") {
		t.Errorf("pressure = %q, want about 1000.4 hPa", got.Fields["pressure"])
	}
	if got.Fields["movements"] != "66" {
		t.Errorf("movements = %q, want 66", got.Fields["movements"])
	}
}

func TestRuuviOmitsInvalidReadings(t *testing.T) {
	// Every sensor field carries its reserved "no reading" value.
	data := []byte{
		0x05,
		0x80, 0x00, // temperature invalid
		0xFF, 0xFF, // humidity invalid
		0xFF, 0xFF, // pressure invalid
		0x80, 0x00, 0x80, 0x00, 0x80, 0x00,
		0xFF, 0xFF, // power invalid
		0x00,
		0x00, 0x00,
	}
	got := DecodeManufacturer(companyRuuvi, data)
	if got == nil {
		t.Fatal("expected a reading with the movement counter at least")
	}
	for _, key := range []string{"temperature", "humidity", "pressure", "battery"} {
		if value, present := got.Fields[key]; present {
			t.Errorf("invalid %s was reported as %q", key, value)
		}
	}
}

func TestDecodeEddystoneURL(t *testing.T) {
	// scheme 3 = https://, then "example" + 0x07 (".com") + "/x"
	data := append([]byte{0x10, 0xEB, 0x03}, []byte("example")...)
	data = append(data, 0x00) // ".com/"
	data = append(data, []byte("x")...)

	got := DecodeService(uuidEddystone, data)
	if got == nil {
		t.Fatal("expected an Eddystone-URL reading")
	}
	if got.Fields["url"] != "https://example.com/x" {
		t.Errorf("url = %q, want https://example.com/x", got.Fields["url"])
	}
}

func TestDecodeEddystoneTLM(t *testing.T) {
	data := []byte{
		0x20, 0x00,
		0x0B, 0xB8, // 3000 mV
		0x18, 0x00, // 24.0 °C
		0x00, 0x00, 0x01, 0x00, // 256 advertisements
		0x00, 0x00, 0x03, 0xE8, // 1000 tenths = 100 s
	}
	got := DecodeService(uuidEddystone, data)
	if got == nil {
		t.Fatal("expected an Eddystone-TLM reading")
	}
	if got.Fields["battery"] != "3000 mV" {
		t.Errorf("battery = %q", got.Fields["battery"])
	}
	if got.Fields["temperature"] != "24.0 °C" {
		t.Errorf("temperature = %q", got.Fields["temperature"])
	}
	if got.Fields["uptime"] != "100 s" {
		t.Errorf("uptime = %q", got.Fields["uptime"])
	}
}

func TestDecodeThermometers(t *testing.T) {
	atc := []byte{
		0xA4, 0xC1, 0x38, 0x11, 0x22, 0x33,
		0x00, 0xE6, // 23.0 °C
		0x37,       // 55 %
		0x64,       // 100 %
		0x0B, 0xB8, // 3000 mV
		0x01,
	}
	got := DecodeService(uuidEnvironment, atc)
	if got == nil || got.Format != "ATC thermometer" {
		t.Fatalf("ATC decode failed: %+v", got)
	}
	if got.Fields["temperature"] != "23.0 °C" {
		t.Errorf("temperature = %q", got.Fields["temperature"])
	}
	if got.Fields["humidity"] != "55 %" {
		t.Errorf("humidity = %q", got.Fields["humidity"])
	}

	pvvx := []byte{
		0x33, 0x22, 0x11, 0x38, 0xC1, 0xA4,
		0xFA, 0x08, // 22.98 °C little-endian
		0xA0, 0x15, // 55.04 %
		0xB8, 0x0B, // 3000 mV
		0x64, 0x01, 0x04,
	}
	got = DecodeService(uuidEnvironment, pvvx)
	if got == nil || got.Format != "pvvx thermometer" {
		t.Fatalf("pvvx decode failed: %+v", got)
	}
	if !strings.HasPrefix(got.Fields["temperature"], "22.9") {
		t.Errorf("temperature = %q", got.Fields["temperature"])
	}
}

// Radio payloads are unauthenticated, so no input may panic or produce a
// value the dashboard would render badly.
func TestDecodersRejectMalformedInput(t *testing.T) {
	companies := []uint16{companyApple, companyRuuvi, 0x1234}
	services := []string{uuidEddystone, uuidEnvironment, "abcd"}

	payloads := [][]byte{
		nil, {}, {0x00}, {0x02}, {0x02, 0x15}, {0x05},
		{0x10, 0x00}, {0x20}, {0xFF, 0xFF, 0xFF},
		make([]byte, 1), make([]byte, 300),
	}
	for _, data := range payloads {
		for _, company := range companies {
			DecodeManufacturer(company, data)
		}
		for _, service := range services {
			DecodeService(service, data)
		}
	}

	// Truncation at every length must also be safe.
	full := []byte{
		0x05, 0x12, 0xFC, 0x53, 0x94, 0xC3, 0x7C,
		0x00, 0x04, 0xFF, 0xFC, 0x04, 0x0C, 0xAC, 0x36, 0x42, 0x00, 0xCD,
	}
	for i := 0; i <= len(full); i++ {
		DecodeManufacturer(companyRuuvi, full[:i])
		DecodeService(uuidEddystone, full[:i])
		DecodeService(uuidEnvironment, full[:i])
	}
}

func TestEddystoneURLRejectsControlCharacters(t *testing.T) {
	// A hostile beacon embedding an escape sequence must be discarded rather
	// than passed through to the dashboard.
	data := append([]byte{0x10, 0xEB, 0x02}, []byte("evil\x1b[31m")...)
	if got := DecodeService(uuidEddystone, data); got != nil {
		t.Errorf("expected a URL with control characters to be rejected, got %q", got.Fields["url"])
	}

	if got := expandURL(9, []byte("x")); got != "" {
		t.Errorf("unknown scheme gave %q, want empty", got)
	}
}
