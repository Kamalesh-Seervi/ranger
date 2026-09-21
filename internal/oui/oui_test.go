package oui

import "testing"

func TestLookupKnownVendor(t *testing.T) {
	// 00:00:0C is Cisco, one of the oldest registered prefixes.
	if got := Lookup("00:00:0c:11:22:33"); got == "" {
		t.Fatal("expected a vendor for 00:00:0c prefix")
	}
}

func TestLookupDetectsRandomizedAddresses(t *testing.T) {
	// Bit 1 of the first octet set means locally administered.
	for _, addr := range []string{"02:11:22:33:44:55", "aa:bb:cc:dd:ee:ff", "06-11-22-33-44-55"} {
		if got := Lookup(addr); got != Randomized {
			t.Errorf("Lookup(%q) = %q, want %q", addr, got, Randomized)
		}
	}
}

func TestLookupAcceptsSeparatorVariants(t *testing.T) {
	want := Lookup("00:00:0c:11:22:33")
	for _, addr := range []string{"00000c112233", "00-00-0c-11-22-33", "0000.0c11.2233"} {
		if got := Lookup(addr); got != want {
			t.Errorf("Lookup(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestLookupRejectsGarbage(t *testing.T) {
	for _, addr := range []string{"", "zz:zz:zz", "00:00", "not-a-mac"} {
		if got := Lookup(addr); got != "" {
			t.Errorf("Lookup(%q) = %q, want empty", addr, got)
		}
	}
}

func TestLookupCompany(t *testing.T) {
	if got := LookupCompany(0x004C); got != "Apple" {
		t.Errorf("LookupCompany(0x004C) = %q, want Apple", got)
	}
	if got := LookupCompany(0xFFFF); got != "" {
		t.Errorf("LookupCompany(0xFFFF) = %q, want empty", got)
	}
}
