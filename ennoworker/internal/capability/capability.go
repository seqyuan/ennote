// Package capability owns the frozen per-run view of ennote's swappable
// capabilities (design 五). Stage 0 is a shell: it wraps the capability
// objects that already exist as per-run values and unifies their teardown into
// one idempotent Dispose. Stage 1 adds RestrictChild projection for delegated
// agents; Stage 2 adds arbitrary-depth recursion.
package capability

import (
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/tools"
)

// CapabilitySnapshot is the frozen per-run capability set. Tools and Policy are
// immutable views built during Run load; the combined disposer tears down the
// per-run resources they own (e.g. MCP connections) when the Run ends.
type CapabilitySnapshot struct {
	RunID          string
	ExecutionDepth int
	Tools          *tools.Registry
	Policy         *agent.FrozenPolicyChain
	dispose        func()
}

// New builds a snapshot and combines the optional disposers into one
// LIFO-ordered (last registered disposed first), idempotent Dispose.
func New(runID string, depth int, registry *tools.Registry, policy *agent.FrozenPolicyChain, disposers ...func()) *CapabilitySnapshot {
	return &CapabilitySnapshot{
		RunID:          runID,
		ExecutionDepth: depth,
		Tools:          registry,
		Policy:         policy,
		dispose:        combineDisposers(disposers...),
	}
}

// Dispose tears down the snapshot's owned resources. It is idempotent: repeat
// calls are no-ops.
func (s *CapabilitySnapshot) Dispose() {
	if s == nil || s.dispose == nil {
		return
	}
	s.dispose()
	s.dispose = nil
}

func combineDisposers(disposers ...func()) func() {
	real := make([]func(), 0, len(disposers))
	for _, d := range disposers {
		if d != nil {
			real = append(real, d)
		}
	}
	if len(real) == 0 {
		return nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(real) - 1; i >= 0; i-- {
				real[i]()
			}
		})
	}
}
