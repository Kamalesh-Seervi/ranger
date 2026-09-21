package scan

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/kd14/ranger/internal/core"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubScanner is a programmable backend for exercising the manager.
type stubScanner struct {
	name   string
	avail  Availability
	notice [2]string
	runs   chan struct{}
	err    error
	emits  int
}

func (s *stubScanner) Name() string                       { return s.name }
func (s *stubScanner) Check(context.Context) Availability { return s.avail }
func (s *stubScanner) Notice() (string, string)           { return s.notice[0], s.notice[1] }

func (s *stubScanner) Run(ctx context.Context, out chan<- core.Observation) error {
	if s.runs != nil {
		select {
		case s.runs <- struct{}{}:
		default:
		}
	}
	for i := 0; i < s.emits; i++ {
		if !emit(ctx, out, core.Observation{
			Kind: core.KindBLE, Addr: "aa:bb:cc:dd:ee:0" + string(rune('0'+i)),
			Source: s.name, RSSI: -50, HasRSSI: true,
		}) {
			return nil
		}
	}
	if s.err != nil {
		return s.err
	}
	<-ctx.Done()
	return nil
}

func TestManagerFeedsRegistryAndCountsObservations(t *testing.T) {
	registry := core.NewRegistry(core.Config{}, core.NewBus())
	scanner := &stubScanner{name: "stub", avail: Available(), emits: 3}
	manager := NewManager(registry, quietLogger(), scanner)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Run(ctx)
	}()

	waitFor(t, time.Second, func() bool { return len(registry.Snapshot()) == 3 })

	statuses := manager.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].State != StateRunning {
		t.Errorf("State = %q, want running", statuses[0].State)
	}
	if statuses[0].Observations != 3 {
		t.Errorf("Observations = %d, want 3", statuses[0].Observations)
	}

	cancel()
	<-done
}

func TestManagerReportsUnavailableWithoutRunning(t *testing.T) {
	registry := core.NewRegistry(core.Config{}, core.NewBus())
	scanner := &stubScanner{
		name:  "blocked",
		avail: Unavailable("no radio", "plug one in"),
		runs:  make(chan struct{}, 1),
		emits: 1,
	}
	manager := NewManager(registry, quietLogger(), scanner)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	manager.Run(ctx)

	select {
	case <-scanner.runs:
		t.Fatal("an unavailable scanner must never be run")
	default:
	}

	status := manager.Statuses()[0]
	if status.State != StateUnavailable {
		t.Errorf("State = %q, want unavailable", status.State)
	}
	if status.Reason != "no radio" || status.Hint != "plug one in" {
		t.Errorf("reason/hint not surfaced: %+v", status)
	}
}

func TestManagerSurfacesNoticeWhileRunning(t *testing.T) {
	registry := core.NewRegistry(core.Config{}, core.NewBus())
	scanner := &stubScanner{
		name:   "degraded",
		avail:  Available(),
		notice: [2]string{"names hidden", "grant permission"},
	}
	manager := NewManager(registry, quietLogger(), scanner)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Run(ctx)
	}()

	waitFor(t, time.Second, func() bool { return manager.Statuses()[0].State == StateRunning })

	status := manager.Statuses()[0]
	if status.Reason != "names hidden" {
		t.Errorf("Reason = %q, want the live notice", status.Reason)
	}
	if status.Hint != "grant permission" {
		t.Errorf("Hint = %q, want the live notice", status.Hint)
	}

	cancel()
	<-done
}

func TestManagerRestartsFailedScanner(t *testing.T) {
	registry := core.NewRegistry(core.Config{}, core.NewBus())
	scanner := &stubScanner{
		name:  "flaky",
		avail: Available(),
		err:   errors.New("radio hiccup"),
		runs:  make(chan struct{}, 4),
	}
	manager := NewManager(registry, quietLogger(), scanner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go manager.Run(ctx)

	// First run is immediate; the retry follows after the backoff delay.
	select {
	case <-scanner.runs:
	case <-time.After(time.Second):
		t.Fatal("scanner never started")
	}
	select {
	case <-scanner.runs:
	case <-time.After(3 * time.Second):
		t.Fatal("failed scanner was not restarted")
	}
}

func TestFakeScannerProducesEveryKind(t *testing.T) {
	registry := core.NewRegistry(core.Config{}, core.NewBus())
	fake := NewFake()
	fake.Interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan core.Observation, 128)
	go func() { _ = fake.Run(ctx, out) }()

	seen := make(map[core.Kind]bool)
	deadline := time.After(2 * time.Second)
	for len(seen) < 4 {
		select {
		case obs := <-out:
			seen[obs.Kind] = true
			registry.Observe(obs)
		case <-deadline:
			t.Fatalf("only saw kinds %v", seen)
		}
	}
	for _, kind := range []core.Kind{core.KindWiFiAP, core.KindWiFiClient, core.KindBLE, core.KindService} {
		if !seen[kind] {
			t.Errorf("fake scanner never produced %q", kind)
		}
	}
}

func TestUnescapeDNSSD(t *testing.T) {
	cases := map[string]string{
		`HFV7WKPQ6L`:           "HFV7WKPQ6L",
		`Kitchen\ Speaker`:     "Kitchen Speaker",
		`F27DDA4CEB6E\@Studio`: "F27DDA4CEB6E@Studio",
		`Office\.Printer`:      "Office.Printer",
		`Back\\slash`:          `Back\slash`,
		`caf\233`:              "caf\xe9",
	}
	for in, want := range cases {
		if got := unescapeDNSSD(in); got != want {
			t.Errorf("unescapeDNSSD(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeTextStripsControlCharacters(t *testing.T) {
	if got := sanitizeText("nor\x00mal\x1b[31m"); got != "normal[31m" {
		t.Errorf("sanitizeText = %q", got)
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	if got := sanitizeText(string(long)); len(got) > 96 {
		t.Errorf("sanitizeText returned %d bytes, want it bounded", len(got))
	}
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(limit)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition not met in time")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
