package classify

import "testing"

func TestServiceTypeIdentifiesObject(t *testing.T) {
	cases := []struct {
		name    string
		service string
		want    Category
	}{
		{"printer", "_ipp._tcp", Printer},
		{"chromecast", "_googlecast._tcp", TV},
		{"sonos", "_sonos._tcp", Speaker},
		{"homekit", "_hap._tcp", SmartHome},
		{"ssh host", "_ssh._tcp", Computer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(Signals{Kind: "service", Meta: map[string]string{"service": c.service}})
			if got.Category != c.want {
				t.Errorf("category = %q, want %q (evidence %v)", got.Category, c.want, got.Evidence)
			}
			if got.Confidence <= 0 {
				t.Errorf("confidence = %v, want > 0", got.Confidence)
			}
		})
	}
}

func TestAppleContinuityIdentifiesUnnamedDevices(t *testing.T) {
	cases := map[string]Category{
		"Proximity Pairing": Headphones,
		"Find My":           Tracker,
		"iBeacon":           Beacon,
	}
	for kind, want := range cases {
		got := Classify(Signals{Kind: "ble", Vendor: "Apple", Meta: map[string]string{"apple": kind}})
		if got.Category != want {
			t.Errorf("apple %q gave %q, want %q", kind, got.Category, want)
		}
	}
}

func TestBLEServiceUUIDIdentifiesObject(t *testing.T) {
	cases := []struct {
		services string
		want     Category
	}{
		{"0000180d-0000-1000-8000-00805f9b34fb", Wearable},
		{"00001812-0000-1000-8000-00805f9b34fb", Peripheral},
		{"0000feaa-0000-1000-8000-00805f9b34fb", Beacon},
		{"180d", Wearable},
	}
	for _, c := range cases {
		got := Classify(Signals{Kind: "ble", Meta: map[string]string{"services": c.services}})
		if got.Category != c.want {
			t.Errorf("services %q gave %q, want %q", c.services, got.Category, c.want)
		}
	}
}

func TestAccessPointIsRouter(t *testing.T) {
	got := Classify(Signals{Kind: "wifi-ap", Name: "Home Net", Security: "WPA3"})
	if got.Category != Router {
		t.Errorf("category = %q, want router", got.Category)
	}
	if got.Mobility != MobilityFixed {
		t.Errorf("mobility = %q, want fixed", got.Mobility)
	}
}

func TestNothingKnownStaysUnknown(t *testing.T) {
	got := Classify(Signals{Kind: "ble", Meta: map[string]string{}})
	if got.Category != Unknown {
		t.Errorf("category = %q, want unknown", got.Category)
	}
	if got.Confidence != 0 {
		t.Errorf("confidence = %v, want 0", got.Confidence)
	}
}

func TestConfidenceDropsWhenEvidenceConflicts(t *testing.T) {
	clear := Classify(Signals{Kind: "service", Meta: map[string]string{"service": "_ipp._tcp"}})
	// A name pulling hard toward a different category should reduce certainty.
	conflicted := Classify(Signals{
		Kind: "service",
		Name: "playstation",
		Meta: map[string]string{"service": "_ipp._tcp"},
	})
	if conflicted.Confidence >= clear.Confidence {
		t.Errorf("conflicting evidence gave %v, expected less than the clean %v",
			conflicted.Confidence, clear.Confidence)
	}
}

func TestMobilityFromSignalSpread(t *testing.T) {
	cases := []struct {
		name    string
		spread  int
		samples int
		want    Mobility
	}{
		{"steady beacon", 2, 40, MobilityFixed},
		{"carried phone", 20, 40, MobilityPortable},
		{"ambiguous", 8, 40, MobilityUnknown},
		{"too few samples", 2, 3, MobilityUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(Signals{Kind: "ble", RSSISpread: c.spread, Samples: c.samples})
			if got.Mobility != c.want {
				t.Errorf("mobility = %q, want %q", got.Mobility, c.want)
			}
		})
	}
}

func TestSpread(t *testing.T) {
	if got := Spread(nil); got != 0 {
		t.Errorf("Spread(nil) = %d, want 0", got)
	}
	if got := Spread([]int{-50}); got != 0 {
		t.Errorf("Spread(single) = %d, want 0", got)
	}

	steady := Spread([]int{-50, -51, -50, -49, -50, -51, -50, -50, -49, -50})
	roaming := Spread([]int{-30, -45, -60, -75, -90, -40, -55, -70, -35, -80})
	if steady >= roaming {
		t.Errorf("steady spread %d should be below roaming spread %d", steady, roaming)
	}

	// The p10/p90 trim should absorb a single wild outlier.
	withOutlier := Spread([]int{-50, -51, -50, -49, -50, -51, -50, -50, -49, 0})
	if withOutlier > steady+10 {
		t.Errorf("one outlier moved spread from %d to %d", steady, withOutlier)
	}
}

func TestEvidenceIsBounded(t *testing.T) {
	got := Classify(Signals{
		Kind:   "ble",
		Name:   "printer camera watch speaker keyboard thermostat xbox airpods",
		Vendor: "sonos bose garmin tile arlo ecobee nintendo logitech",
		Meta:   map[string]string{"apple": "Find My", "services": "180d,1812,feaa"},
	})
	if len(got.Evidence) > maxEvidence {
		t.Errorf("evidence length %d exceeds cap %d", len(got.Evidence), maxEvidence)
	}
}

func TestClassifyIsDeterministic(t *testing.T) {
	signals := Signals{
		Kind:   "ble",
		Name:   "Living Room",
		Vendor: "Sonos",
		Meta:   map[string]string{"service": "_raop._tcp"},
	}
	first := Classify(signals)
	for i := 0; i < 20; i++ {
		again := Classify(signals)
		if again.Category != first.Category || again.Confidence != first.Confidence {
			t.Fatalf("run %d gave %v/%v, first gave %v/%v",
				i, again.Category, again.Confidence, first.Category, first.Confidence)
		}
	}
}

func TestMobilityAloneNeverDecidesCategory(t *testing.T) {
	// A device that only sits still tells us almost nothing. The mobility
	// nudges must not be enough to claim it is a smart-home gadget.
	got := Classify(Signals{Kind: "ble", RSSISpread: 1, Samples: 40})
	if got.Category != Unknown {
		t.Errorf("category = %q, want unknown (evidence %v)", got.Category, got.Evidence)
	}
	if got.Mobility != MobilityFixed {
		t.Errorf("mobility = %q, want fixed", got.Mobility)
	}
}

func TestAmbiguousAppleSignalsStayTentative(t *testing.T) {
	// Find My is broadcast by AirTags and by idle iPhones and Macs, so it must
	// never be reported with high confidence.
	for _, kind := range []string{"Find My", "Nearby", "Handoff"} {
		got := Classify(Signals{Kind: "ble", Meta: map[string]string{"apple": kind}})
		if got.Confidence >= 0.5 {
			t.Errorf("%s gave %q at %v confidence; ambiguous evidence should stay below 0.5",
				kind, got.Category, got.Confidence)
		}
	}

	// An unambiguous signal should still be confident.
	pairing := Classify(Signals{Kind: "ble", Meta: map[string]string{"apple": "Proximity Pairing"}})
	if pairing.Confidence < 0.5 {
		t.Errorf("Proximity Pairing gave %v confidence, want a clear result", pairing.Confidence)
	}
}

func TestShortUUID(t *testing.T) {
	cases := map[string]string{
		"0000180d-0000-1000-8000-00805f9b34fb": "180d",
		"0000180D-0000-1000-8000-00805F9B34FB": "180d",
		"180d":                                 "180d",
		"":                                     "",
		"not-a-uuid":                           "",
		"12345678-0000-1000-8000-00805f9b34fb": "",
	}
	for in, want := range cases {
		if got := shortUUID(in); got != want {
			t.Errorf("shortUUID(%q) = %q, want %q", in, got, want)
		}
	}
}
