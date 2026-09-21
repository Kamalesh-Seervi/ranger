package geo

import (
	"math"
	"testing"

	"github.com/kd14/ranger/internal/core"
)

func TestDistanceAtReferencePowerIsOneMetre(t *testing.T) {
	m := Model{RefRSSI: -40, Exponent: 3.0}
	if got := m.Distance(-40); math.Abs(got-1.0) > 0.001 {
		t.Errorf("Distance(-40) = %v, want 1.0", got)
	}
}

func TestDistanceIncreasesAsSignalWeakens(t *testing.T) {
	m := DefaultModel(core.KindWiFiAP)
	near, far := m.Distance(-40), m.Distance(-80)
	if far <= near {
		t.Errorf("weaker signal gave %v m, stronger gave %v m", far, near)
	}
}

func TestDistanceIsClamped(t *testing.T) {
	m := DefaultModel(core.KindWiFiAP)
	if got := m.Distance(20); got < MinDistance {
		t.Errorf("Distance(20) = %v, want >= %v", got, MinDistance)
	}
	if got := m.Distance(-200); got > MaxDistance {
		t.Errorf("Distance(-200) = %v, want <= %v", got, MaxDistance)
	}
}

func TestZeroExponentFallsBackToDefault(t *testing.T) {
	m := Model{RefRSSI: -40}
	if got := m.Distance(-70); math.IsInf(got, 0) || math.IsNaN(got) {
		t.Errorf("Distance with zero exponent = %v", got)
	}
}

func TestQualityBounds(t *testing.T) {
	if got := Quality(-10); got != 1 {
		t.Errorf("Quality(-10) = %v, want 1", got)
	}
	if got := Quality(-120); got != 0 {
		t.Errorf("Quality(-120) = %v, want 0", got)
	}
	mid := Quality(-62)
	if mid <= 0 || mid >= 1 {
		t.Errorf("Quality(-62) = %v, want strictly between 0 and 1", mid)
	}
}
