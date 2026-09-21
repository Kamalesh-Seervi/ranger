// Package scan defines the scanner backend contract and the supervisor that
// runs them.
package scan

import (
	"context"

	"github.com/kd14/ranger/internal/core"
)

// Availability reports whether a backend can run on this machine right now.
// A backend that cannot run explains why rather than failing startup, so the
// UI can tell the user what permission or hardware is missing.
type Availability struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// Available reports a backend as ready.
func Available() Availability { return Availability{OK: true} }

// Unavailable reports a backend as unusable, with a reason and a remedy.
func Unavailable(reason, hint string) Availability {
	return Availability{OK: false, Reason: reason, Hint: hint}
}

// Scanner is one radio backend. Run must respect context cancellation and
// must not close out; the manager owns the channel.
type Scanner interface {
	Name() string
	Check(ctx context.Context) Availability
	Run(ctx context.Context, out chan<- core.Observation) error
}

// Noticer is an optional interface for a scanner that works but is degraded,
// such as Wi-Fi scanning without the permission needed to reveal network
// names. Both strings are empty when nothing is wrong.
type Noticer interface {
	Notice() (reason, hint string)
}

// emit delivers an observation unless the context is cancelled first. It
// returns false when the caller should stop producing.
func emit(ctx context.Context, out chan<- core.Observation, obs core.Observation) bool {
	select {
	case out <- obs:
		return true
	case <-ctx.Done():
		return false
	}
}
