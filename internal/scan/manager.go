package scan

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kd14/ranger/internal/core"
)

// State is the lifecycle of one scanner backend.
type State string

const (
	StatePending     State = "pending"
	StateRunning     State = "running"
	StateUnavailable State = "unavailable"
	StateFailed      State = "failed"
	StateStopped     State = "stopped"
)

// Status is the externally visible health of a scanner.
type Status struct {
	Name         string     `json:"name"`
	State        State      `json:"state"`
	Reason       string     `json:"reason,omitempty"`
	Hint         string     `json:"hint,omitempty"`
	Error        string     `json:"error,omitempty"`
	Observations int64      `json:"observations"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// A run lasting longer than this is treated as healthy, resetting backoff.
	healthyRun = 30 * time.Second
)

// Manager supervises every scanner, feeding their observations into the
// registry and restarting failures with exponential backoff.
type Manager struct {
	reg      *core.Registry
	log      *slog.Logger
	scanners []Scanner

	mu       sync.RWMutex
	statuses map[string]*Status
	byName   map[string]Scanner
	order    []string

	counts sync.Map // scanner name -> *atomic.Int64
}

func NewManager(reg *core.Registry, log *slog.Logger, scanners ...Scanner) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		reg:      reg,
		log:      log,
		scanners: scanners,
		statuses: make(map[string]*Status, len(scanners)),
		byName:   make(map[string]Scanner, len(scanners)),
	}
	now := time.Now()
	for _, s := range scanners {
		name := s.Name()
		m.order = append(m.order, name)
		m.byName[name] = s
		m.statuses[name] = &Status{Name: name, State: StatePending, UpdatedAt: now}
		m.counts.Store(name, new(atomic.Int64))
	}
	return m
}

// Statuses returns a snapshot of every scanner's health, in registration order.
func (m *Manager) Statuses() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Status, 0, len(m.order))
	for _, name := range m.order {
		st := *m.statuses[name]
		if c, ok := m.counts.Load(name); ok {
			st.Observations = c.(*atomic.Int64).Load()
		}
		// A degraded-but-running backend reports its problem live, since the
		// user may fix the permission while ranger keeps running.
		if st.State == StateRunning {
			if n, ok := m.byName[name].(Noticer); ok {
				if reason, hint := n.Notice(); reason != "" {
					st.Reason, st.Hint = reason, hint
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// Run starts every scanner and blocks until the context is cancelled and all
// backends have stopped.
func (m *Manager) Run(ctx context.Context) {
	observations := make(chan core.Observation, 512)

	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		for obs := range observations {
			if c, ok := m.counts.Load(obs.Source); ok {
				c.(*atomic.Int64).Add(1)
			}
			m.reg.Observe(obs)
		}
	}()

	var scanners sync.WaitGroup
	for _, s := range m.scanners {
		scanners.Add(1)
		go func(s Scanner) {
			defer scanners.Done()
			m.supervise(ctx, s, observations)
		}(s)
	}

	scanners.Wait()
	close(observations)
	drain.Wait()
}

// supervise runs one scanner, restarting it with backoff until the context ends.
func (m *Manager) supervise(ctx context.Context, s Scanner, out chan<- core.Observation) {
	name := s.Name()

	if avail := s.Check(ctx); !avail.OK {
		m.log.Warn("scanner unavailable", "scanner", name, "reason", avail.Reason)
		m.setStatus(name, func(st *Status) {
			st.State = StateUnavailable
			st.Reason = avail.Reason
			st.Hint = avail.Hint
		})
		return
	}

	backoff := minBackoff
	for ctx.Err() == nil {
		started := time.Now()
		m.setStatus(name, func(st *Status) {
			st.State = StateRunning
			st.Error = ""
			st.Reason = ""
			st.Hint = ""
			st.StartedAt = &started
		})
		m.log.Info("scanner started", "scanner", name)

		err := s.Run(ctx, out)
		elapsed := time.Since(started)

		if ctx.Err() != nil {
			m.setStatus(name, func(st *Status) { st.State = StateStopped })
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			m.log.Error("scanner failed", "scanner", name, "err", err, "uptime", elapsed)
			m.setStatus(name, func(st *Status) {
				st.State = StateFailed
				st.Error = err.Error()
			})
		} else {
			m.log.Info("scanner returned", "scanner", name, "uptime", elapsed)
		}

		if elapsed >= healthyRun {
			backoff = minBackoff
		}
		select {
		case <-ctx.Done():
			m.setStatus(name, func(st *Status) { st.State = StateStopped })
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
	m.setStatus(name, func(st *Status) { st.State = StateStopped })
}

// setStatus mutates a scanner's status and broadcasts the change.
func (m *Manager) setStatus(name string, mutate func(*Status)) {
	m.mu.Lock()
	st, ok := m.statuses[name]
	if !ok {
		m.mu.Unlock()
		return
	}
	mutate(st)
	st.UpdatedAt = time.Now()
	snapshot := *st
	m.mu.Unlock()

	if c, ok := m.counts.Load(name); ok {
		snapshot.Observations = c.(*atomic.Int64).Load()
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		m.log.Error("marshal scanner status", "scanner", name, "err", err)
		return
	}
	m.reg.Bus().Publish(core.Event{Type: core.EventStatus, ID: name, Data: payload})
}
