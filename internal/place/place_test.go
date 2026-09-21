package place

import (
	"path/filepath"
	"testing"
	"time"
)

func fingerprint(anchors map[string]int) Fingerprint {
	return Fingerprint{Anchors: anchors, Labels: map[string]string{}}
}

func TestSimilarityBounds(t *testing.T) {
	a := map[string]int{"ap1": -50, "ap2": -60, "ap3": -70}

	if got := Similarity(a, a); got != 1 {
		t.Errorf("identical signatures scored %v, want 1", got)
	}
	if got := Similarity(a, map[string]int{"other": -50}); got != 0 {
		t.Errorf("disjoint signatures scored %v, want 0", got)
	}
	if got := Similarity(a, nil); got != 0 {
		t.Errorf("empty signature scored %v, want 0", got)
	}
}

func TestSimilarityPenalisesSignalDisagreement(t *testing.T) {
	base := map[string]int{"ap1": -50, "ap2": -60}
	near := map[string]int{"ap1": -52, "ap2": -61}
	far := map[string]int{"ap1": -80, "ap2": -30}

	nearScore := Similarity(base, near)
	farScore := Similarity(base, far)

	if nearScore <= farScore {
		t.Errorf("same anchors at similar strength scored %v, at wildly different strength %v",
			nearScore, farScore)
	}
	if nearScore < 0.8 {
		t.Errorf("a couple of dB of drift dropped the score to %v", nearScore)
	}
}

func TestSimilarityPenalisesMissingAnchors(t *testing.T) {
	full := map[string]int{"ap1": -50, "ap2": -50, "ap3": -50, "ap4": -50}
	partial := map[string]int{"ap1": -50, "ap2": -50}

	if got := Similarity(full, partial); got >= 1 {
		t.Errorf("half the anchors scored %v, want below 1", got)
	}
}

func TestBuildKeepsOnlyStableStrongAnchors(t *testing.T) {
	fp := Build([]Observation{
		{ID: "ap1", RSSI: -50, Stable: true, Label: "Home"},
		{ID: "phone", RSSI: -40, Stable: false},
		{ID: "distant", RSSI: -99, Stable: true},
		{ID: "", RSSI: -50, Stable: true},
	}, -92)

	if len(fp.Anchors) != 1 {
		t.Fatalf("anchors = %v, want only the stable strong one", fp.Anchors)
	}
	if _, ok := fp.Anchors["ap1"]; !ok {
		t.Error("expected ap1 to be kept")
	}
	if fp.Labels["ap1"] != "Home" {
		t.Errorf("label = %q, want Home", fp.Labels["ap1"])
	}
}

func TestUpdateNeedsEnoughAnchors(t *testing.T) {
	engine := NewEngine(Config{MinAnchors: 3})
	if _, ok := engine.Update(fingerprint(map[string]int{"ap1": -50}), time.Now()); ok {
		t.Fatal("expected a sparse fingerprint to be rejected")
	}
	if len(engine.Places()) != 0 {
		t.Error("a rejected fingerprint must not create a place")
	}
}

func TestLearnsAndRecognisesAPlace(t *testing.T) {
	engine := NewEngine(Config{})
	now := time.Unix(1000, 0)
	kitchen := map[string]int{"ap1": -45, "ap2": -60, "ap3": -72, "ap4": -80}

	first, ok := engine.Update(fingerprint(kitchen), now)
	if !ok {
		t.Fatal("first fingerprint was rejected")
	}
	if !first.IsNew {
		t.Error("the first fingerprint should create a place")
	}

	// Returning to the same spot, with realistic drift, must match it again.
	drifted := map[string]int{"ap1": -47, "ap2": -58, "ap3": -74, "ap4": -78}
	second, ok := engine.Update(fingerprint(drifted), now.Add(10*time.Second))
	if !ok {
		t.Fatal("second fingerprint was rejected")
	}
	if second.IsNew {
		t.Error("returning to the same place should not invent a new one")
	}
	if second.PlaceID != first.PlaceID {
		t.Errorf("matched %q, want %q", second.PlaceID, first.PlaceID)
	}
	if len(engine.Places()) != 1 {
		t.Errorf("learned %d places, want 1", len(engine.Places()))
	}
}

func TestDistinctRoomsBecomeDistinctPlaces(t *testing.T) {
	engine := NewEngine(Config{})
	now := time.Unix(1000, 0)

	kitchen, _ := engine.Update(fingerprint(map[string]int{
		"ap1": -40, "ap2": -55, "ap3": -70, "ap4": -85,
	}), now)

	// A different room hears a different mix at different strengths.
	garage, ok := engine.Update(fingerprint(map[string]int{
		"ap5": -45, "ap6": -50, "ap7": -65, "ap8": -75,
	}), now.Add(time.Minute))
	if !ok {
		t.Fatal("second room was rejected")
	}

	if garage.PlaceID == kitchen.PlaceID {
		t.Error("two clearly different signatures collapsed into one place")
	}
	if !garage.Changed {
		t.Error("moving to a new place should report a change")
	}
	if len(engine.Places()) != 2 {
		t.Errorf("learned %d places, want 2", len(engine.Places()))
	}
}

func TestHysteresisPreventsFlapping(t *testing.T) {
	engine := NewEngine(Config{})
	now := time.Unix(1000, 0)

	anchors := map[string]int{"ap1": -50, "ap2": -60, "ap3": -70, "ap4": -80}
	first, _ := engine.Update(fingerprint(anchors), now)

	// Nudge the signature repeatedly by a couple of dB, the way a body moving
	// through a doorway would. The place must hold steady.
	for i := 0; i < 20; i++ {
		shifted := map[string]int{}
		for id, rssi := range anchors {
			if i%2 == 0 {
				shifted[id] = rssi - 2
			} else {
				shifted[id] = rssi + 2
			}
		}
		match, ok := engine.Update(fingerprint(shifted), now.Add(time.Duration(i+1)*time.Second))
		if !ok {
			t.Fatalf("iteration %d rejected", i)
		}
		if match.PlaceID != first.PlaceID {
			t.Fatalf("iteration %d flapped to %q", i, match.PlaceID)
		}
	}
	if len(engine.Places()) != 1 {
		t.Errorf("small drift created %d places, want 1", len(engine.Places()))
	}
}

func TestDwellAccumulates(t *testing.T) {
	engine := NewEngine(Config{})
	now := time.Unix(1000, 0)
	anchors := map[string]int{"ap1": -50, "ap2": -60, "ap3": -70}

	engine.Update(fingerprint(anchors), now)
	engine.Update(fingerprint(anchors), now.Add(30*time.Second))

	current, ok := engine.Current()
	if !ok {
		t.Fatal("no current place")
	}
	if current.Dwell != 30*time.Second {
		t.Errorf("dwell = %v, want 30s", current.Dwell)
	}
}

func TestDwellIgnoresLongGaps(t *testing.T) {
	engine := NewEngine(Config{})
	now := time.Unix(1000, 0)
	anchors := map[string]int{"ap1": -50, "ap2": -60, "ap3": -70}

	engine.Update(fingerprint(anchors), now)
	// A gap this long means ranger was not running, not that you stood still.
	engine.Update(fingerprint(anchors), now.Add(6*time.Hour))

	current, _ := engine.Current()
	if current.Dwell != 0 {
		t.Errorf("dwell = %v, want 0 across a long gap", current.Dwell)
	}
}

func TestPartialViewStillMatchesKnownPlace(t *testing.T) {
	// A scan that has only found some of a place's anchors, but agrees on all
	// of them, must still be recognised. Rejecting this is what makes a place
	// re-learn itself on every restart.
	full := map[string]int{"ap1": -40, "ap2": -55, "ap3": -70, "ap4": -80, "ap5": -85}
	partial := map[string]int{"ap1": -42, "ap2": -54, "ap3": -72}

	got := Similarity(partial, full)
	if got < 0.62 {
		t.Errorf("partial view of a known place scored %v, below the default threshold", got)
	}

	// Agreeing on nothing must still score zero, however contained the sets.
	disagreeing := map[string]int{"ap1": -95, "ap2": -10, "ap3": -95}
	if got := Similarity(disagreeing, full); got > 0.5 {
		t.Errorf("wildly different signal levels scored %v, want a low score", got)
	}
}

func TestSurvivesRestartWithAGrowingFingerprint(t *testing.T) {
	// Reproduces a real failure: a place learned from a short scan, then
	// revisited once more anchors are visible, must not become a second place.
	path := filepath.Join(t.TempDir(), "places.json")
	now := time.Unix(1000, 0)

	early := NewEngine(Config{Path: path})
	first, _ := early.Update(fingerprint(map[string]int{
		"ap1": -37, "ap2": -45, "ap3": -66, "ap4": -73, "ap5": -78,
	}), now)
	if err := early.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	later := NewEngine(Config{Path: path})
	if err := later.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	match, ok := later.Update(fingerprint(map[string]int{
		"ap1": -41, "ap2": -43, "ap3": -62, "ap4": -72, "ap5": -79,
		"ap6": -53, "ap7": -56, "ap8": -76, "ble1": -31, "ble2": -60,
	}), now.Add(time.Hour))

	if !ok {
		t.Fatal("fuller fingerprint was rejected")
	}
	if match.IsNew {
		t.Errorf("a fuller view of the same spot created a new place (score %v)", match.Score)
	}
	if match.PlaceID != first.PlaceID {
		t.Errorf("matched %q, want the remembered %q", match.PlaceID, first.PlaceID)
	}
}

func TestRenameAndForget(t *testing.T) {
	engine := NewEngine(Config{})
	match, _ := engine.Update(fingerprint(map[string]int{
		"ap1": -50, "ap2": -60, "ap3": -70,
	}), time.Unix(1000, 0))

	if err := engine.Rename(match.PlaceID, "Kitchen"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	current, _ := engine.Current()
	if current.Name != "Kitchen" {
		t.Errorf("name = %q, want Kitchen", current.Name)
	}

	if err := engine.Rename("nope", "x"); err == nil {
		t.Error("renaming an unknown place should fail")
	}
	if err := engine.Forget(match.PlaceID); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if len(engine.Places()) != 0 {
		t.Error("forgotten place is still present")
	}
	if _, ok := engine.Current(); ok {
		t.Error("forgetting the current place should clear it")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "places.json")
	now := time.Unix(1000, 0)
	anchors := map[string]int{"ap1": -50, "ap2": -60, "ap3": -70}

	original := NewEngine(Config{Path: path})
	match, _ := original.Update(fingerprint(anchors), now)
	if err := original.Rename(match.PlaceID, "Office"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := original.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	restored := NewEngine(Config{Path: path})
	if err := restored.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	places := restored.Places()
	if len(places) != 1 {
		t.Fatalf("loaded %d places, want 1", len(places))
	}
	if places[0].Name != "Office" {
		t.Errorf("name = %q, want Office", places[0].Name)
	}

	// The restored engine must recognise the same spot it learned before.
	again, ok := restored.Update(fingerprint(anchors), now.Add(time.Minute))
	if !ok {
		t.Fatal("restored engine rejected a known fingerprint")
	}
	if again.IsNew {
		t.Error("restored engine failed to recognise its own learned place")
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	engine := NewEngine(Config{Path: filepath.Join(t.TempDir(), "absent.json")})
	if err := engine.Load(); err != nil {
		t.Fatalf("Load of a missing file returned %v", err)
	}
}

func TestMaxPlacesEviction(t *testing.T) {
	engine := NewEngine(Config{MaxPlaces: 2})
	now := time.Unix(1000, 0)

	for i := 0; i < 5; i++ {
		anchors := map[string]int{}
		for j := 0; j < 4; j++ {
			anchors[string(rune('a'+i))+string(rune('0'+j))] = -50 - j
		}
		engine.Update(fingerprint(anchors), now.Add(time.Duration(i)*time.Minute))
	}

	if got := len(engine.Places()); got > 2 {
		t.Errorf("kept %d places, want at most 2", got)
	}
}

func TestAnchorPruningDropsVanishedRouters(t *testing.T) {
	engine := NewEngine(Config{})
	now := time.Unix(1000, 0)

	// One anchor appears only on the very first visit.
	engine.Update(fingerprint(map[string]int{
		"stable1": -50, "stable2": -60, "stable3": -70, "gone": -65,
	}), now)

	for i := 1; i <= 40; i++ {
		engine.Update(fingerprint(map[string]int{
			"stable1": -50, "stable2": -60, "stable3": -70,
		}), now.Add(time.Duration(i)*time.Second))
	}

	current, _ := engine.Current()
	if _, present := current.Anchors["gone"]; present {
		t.Error("an anchor seen once in 40 visits should have been pruned")
	}
	if len(current.Anchors) != 3 {
		t.Errorf("anchors = %v, want the three persistent ones", current.Anchors)
	}
}
