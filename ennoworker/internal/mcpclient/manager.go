package mcpclient

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// RunConnectionSet owns the MCP sessions for one Run. Sessions are keyed by
// runServerID and carry a connection generation that increments on every
// rebuild. Late responses/notifications from a stale generation are dropped.
// No session is shared across Runs.
type RunConnectionSet struct {
	mu       sync.Mutex
	runID    string
	sessions map[string]*runConnection
	closed   bool
	// lastGenerations tracks the highest generation ever recorded per server
	// so a reconnect after transport death never reuses a stale generation.
	lastGenerations map[string]int
	// onListChanged marks future catalogs stale when any server notifies a
	// tools/list_changed event. Active Run Registry is never hot-updated.
	onListChanged func()
}

// SetListChangedHandler installs the future-catalog staleness callback.
func (s *RunConnectionSet) SetListChangedHandler(handler func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onListChanged = handler
}

type runConnection struct {
	serverID   string
	generation int
	session    *Session
	lastUsed   time.Time
}

// NewRunConnectionSet creates an empty per-Run connection set.
func NewRunConnectionSet(runID string) *RunConnectionSet {
	return &RunConnectionSet{runID: runID, sessions: map[string]*runConnection{}, lastGenerations: map[string]int{}}
}

// GetOrConnect returns the live session for a server, reconnecting with a new
// generation when the current one is gone. dispatch-before-loss semantics are
// enforced by the Tool adapter: a reconnect only happens when no call is in
// flight, so a fresh generation is always safe.
func (s *RunConnectionSet) GetOrConnect(ctx context.Context, runServerID string,
	version *domain.MCPServerProfileVersion, opts ConnectOption, logger *slog.Logger) (*Session, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, context.Canceled
	}
	if rc, ok := s.sessions[runServerID]; ok && rc.session != nil {
		rc.lastUsed = time.Now()
		select {
		case <-rc.session.Done():
			// Transport died; drop it and reconnect below with the NEXT
			// generation so late responses from the dead generation are
			// distinguishable and dropped.
			rc.session.Close()
			if rc.generation > s.lastGenerations[runServerID] {
				s.lastGenerations[runServerID] = rc.generation
			}
			delete(s.sessions, runServerID)
		default:
			return rc.session, rc.generation, nil
		}
	}
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// A tools/list_changed notification only marks the FUTURE catalog stale;
	// it never mutates the active Run's frozen Registry.
	if opts.OnToolListChanged == nil && s.onListChanged != nil {
		opts.OnToolListChanged = s.onListChanged
	}
	sess, err := Connect(connectCtx, version, opts)
	if err != nil {
		return nil, 0, err
	}
	generation := s.currentGenerationLocked(runServerID) + 1
	s.sessions[runServerID] = &runConnection{serverID: runServerID, generation: generation, session: sess, lastUsed: time.Now()}
	if generation > s.lastGenerations[runServerID] {
		s.lastGenerations[runServerID] = generation
	}
	if logger != nil {
		logger.Debug("mcp run connection established", "run", s.runID, "server", runServerID, "generation", generation)
	}
	return sess, generation, nil
}

// currentGenerationLocked reports the highest generation ever recorded for a
// server in this set (0 when never connected). It must be called with s.mu
// held. Generations are monotonic per server: reconnect always bumps.
func (s *RunConnectionSet) currentGenerationLocked(runServerID string) int {
	if rc, ok := s.sessions[runServerID]; ok {
		return rc.generation
	}
	// The dead session was deleted; track the last generation separately so a
	// reconnect never reuses an old generation.
	return s.lastGenerations[runServerID]
}

// CurrentGeneration reports the live generation for a server (0 when absent).
func (s *RunConnectionSet) CurrentGeneration(runServerID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rc, ok := s.sessions[runServerID]; ok {
		return rc.generation
	}
	return 0
}

// Close terminates every session in the set.
func (s *RunConnectionSet) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for id, rc := range s.sessions {
		rc.session.Close()
		delete(s.sessions, id)
	}
}
