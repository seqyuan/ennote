package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ToolExecution is the read/write identity of one tool call crossing the policy
// chain. Original is snapshotted and read-only; Effective carries the latest
// rewritten arguments so downstream listeners always see the newest value.
type ToolExecution struct {
	RunID       string
	Iteration   int
	CallIndex   int
	Original    domain.ToolCall // snapshot, read-only
	Effective   domain.ToolCall // chain-visible, may be rewritten
	Policy      domain.PolicySnapshot
	WorkspaceID string
	RiskClass   domain.RiskClass // resolved once at chain construction, read-only
}

// PreToolDecision reuses the existing ToolDecision closed action set.
// allow / require_approval may carry rewritten Arguments; deny / terminate_batch
// must not. The container rejects pre decisions carrying RuleID/StandingScope*.
type PreToolDecision = ToolDecision

// PostToolDecision is the post-execution projection decision:
//
//	accept   — keep result
//	replace  — replace result (re-projected content)
//	block    — turn corrective feedback into an isError result
type PostToolDecision struct {
	Action         string // "accept" | "replace" | "block"
	Result         domain.ToolResult
	StopAfterBatch bool
	Code           string
	Reason         string
}

// PreToolHook is a waterfall: return a decision to short-circuit, or call next()
// to delegate. exec is passed by pointer so a listener may rewrite Effective and
// downstream listeners see the new arguments.
type PreToolHook func(ctx context.Context, exec *ToolExecution, next func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error)

// ToolGuard is a monotonic guard: it can only deny. A returned (reason, true)
// denies; ("", false) allows. There is no allow result, so a later listener can
// never undo an earlier denial. Guards must be pure: no side effects, no I/O.
// Reserved slot: not a seam, no provider/consumer, not implemented this phase.
type ToolGuard func(exec *ToolExecution) (denyReason string, ok bool)

// PostToolHook is the post-execution waterfall.
type PostToolHook func(ctx context.Context, exec *ToolExecution, result domain.ToolResult, next func(domain.ToolResult) (PostToolDecision, error)) (PostToolDecision, error)

type preToolHookEntry struct {
	id   uint64
	hook PreToolHook
}

type toolGuardEntry struct {
	id    uint64
	guard ToolGuard
}

type postToolHookEntry struct {
	id   uint64
	hook PostToolHook
}

// PolicyChain is a per-run policy chain. Registration happens only during load
// time; after Freeze the chain is read-only (I6).
type PolicyChain struct {
	mu     sync.Mutex
	nextID uint64
	pre    []preToolHookEntry
	guards []toolGuardEntry // reserved slot (ToolGuard), unused this phase
	post   []postToolHookEntry
	frozen bool
}

// NewPolicyChain returns an empty, unfrozen chain.
func NewPolicyChain() *PolicyChain {
	return &PolicyChain{}
}

// NewPolicyChainFromToolPolicy builds a Stage 0 chain wrapping one ToolPolicy:
// its BeforeToolBatch becomes the sole pre listener and its AfterToolCall the
// sole post listener. Registration order matches the pre-refactor behaviour.
func NewPolicyChainFromToolPolicy(policy ToolPolicy) (*PolicyChain, error) {
	if policy == nil {
		return nil, fmt.Errorf("tool policy is required")
	}
	chain := NewPolicyChain()
	if _, err := chain.RegisterPre(builtinPreAdapter(policy), false); err != nil {
		return nil, err
	}
	if _, err := chain.RegisterPost(builtinPostAdapter(policy)); err != nil {
		return nil, err
	}
	return chain, nil
}

// builtinPreAdapter wraps the batch-level ToolPolicy.BeforeToolBatch as a
// per-exec pre listener. Safe because BuiltinToolPolicy's loop is per-call pure
// (no cross-call state): a single-element batch yields the identical decision.
func builtinPreAdapter(policy ToolPolicy) PreToolHook {
	return func(ctx context.Context, exec *ToolExecution, _ func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		decisions, err := callBeforeToolBatch(ctx, policy, ToolBatchContext{
			RunID:       exec.RunID,
			Iteration:   exec.Iteration,
			Policy:      exec.Policy,
			WorkspaceID: exec.WorkspaceID,
		}, []domain.ToolCall{exec.Effective})
		if err != nil {
			return PreToolDecision{}, err
		}
		if len(decisions) != 1 {
			return PreToolDecision{}, fmt.Errorf("tool policy returned %d decisions for 1 call", len(decisions))
		}
		return decisions[0], nil
	}
}

// builtinPostAdapter wraps ToolPolicy.AfterToolCall as the post listener.
func builtinPostAdapter(policy ToolPolicy) PostToolHook {
	return func(ctx context.Context, exec *ToolExecution, result domain.ToolResult, _ func(domain.ToolResult) (PostToolDecision, error)) (PostToolDecision, error) {
		d, err := callAfterTool(ctx, policy, ToolCallContext{
			RunID:       exec.RunID,
			Iteration:   exec.Iteration,
			CallIndex:   exec.CallIndex,
			Policy:      exec.Policy,
			WorkspaceID: exec.WorkspaceID,
		}, exec.Effective, result)
		if err != nil {
			return PostToolDecision{}, err
		}
		return PostToolDecision{
			Action:         "replace",
			Result:         d.Result,
			StopAfterBatch: d.StopAfterBatch,
			Code:           d.Code,
			Reason:         d.Reason,
		}, nil
	}
}

// RegisterPre adds a pre listener and returns its disposer. The disposer is a
// no-op after Freeze (I3/I6: a frozen chain is torn down wholesale, not entry
// by entry). prepend inserts before existing listeners.
func (c *PolicyChain) RegisterPre(h PreToolHook, prepend bool) (func(), error) {
	if h == nil {
		return nil, fmt.Errorf("pre hook is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return nil, fmt.Errorf("policy chain is frozen")
	}
	c.nextID++
	entry := preToolHookEntry{id: c.nextID, hook: h}
	if prepend {
		c.pre = append([]preToolHookEntry{entry}, c.pre...)
	} else {
		c.pre = append(c.pre, entry)
	}
	return c.removePre(entry.id), nil
}

// RegisterGuard is the reserved-slot registration point; unused this phase.
func (c *PolicyChain) RegisterGuard(g ToolGuard) (func(), error) {
	if g == nil {
		return nil, fmt.Errorf("guard is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return nil, fmt.Errorf("policy chain is frozen")
	}
	c.nextID++
	entry := toolGuardEntry{id: c.nextID, guard: g}
	c.guards = append(c.guards, entry)
	return c.removeGuard(entry.id), nil
}

// RegisterPost adds a post listener and returns its disposer.
func (c *PolicyChain) RegisterPost(h PostToolHook) (func(), error) {
	if h == nil {
		return nil, fmt.Errorf("post hook is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return nil, fmt.Errorf("policy chain is frozen")
	}
	c.nextID++
	entry := postToolHookEntry{id: c.nextID, hook: h}
	c.post = append(c.post, entry)
	return c.removePost(entry.id), nil
}

func (c *PolicyChain) removePre(id uint64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.frozen {
				return
			}
			for i, e := range c.pre {
				if e.id == id {
					c.pre = append(c.pre[:i], c.pre[i+1:]...)
					return
				}
			}
		})
	}
}

func (c *PolicyChain) removeGuard(id uint64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.frozen {
				return
			}
			for i, e := range c.guards {
				if e.id == id {
					c.guards = append(c.guards[:i], c.guards[i+1:]...)
					return
				}
			}
		})
	}
}

func (c *PolicyChain) removePost(id uint64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.frozen {
				return
			}
			for i, e := range c.post {
				if e.id == id {
					c.post = append(c.post[:i], c.post[i+1:]...)
					return
				}
			}
		})
	}
}

// Freeze returns an immutable view; afterwards any registration or mutation is
// rejected (I6).
func (c *PolicyChain) Freeze() (*FrozenPolicyChain, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return nil, fmt.Errorf("policy chain already frozen")
	}
	c.frozen = true
	frozen := &FrozenPolicyChain{
		pre:  make([]PreToolHook, len(c.pre)),
		post: make([]PostToolHook, len(c.post)),
	}
	for i, e := range c.pre {
		frozen.pre[i] = e.hook
	}
	for i, e := range c.post {
		frozen.post[i] = e.hook
	}
	return frozen, nil
}

// FrozenPolicyChain is the immutable per-run view of a PolicyChain.
type FrozenPolicyChain struct {
	pre  []PreToolHook
	post []PostToolHook
}

// Preflight runs the pre chain for every exec in order: for each exec the full
// listener chain runs (short-circuit stops it) before the next exec starts
// (G3). terminate is true when any decision is terminate_batch. A nil exec is a
// caller error.
func (c *FrozenPolicyChain) Preflight(ctx context.Context, execs []*ToolExecution) ([]PreToolDecision, bool, error) {
	decisions := make([]PreToolDecision, len(execs))
	terminate := false
	for i, exec := range execs {
		if exec == nil {
			return nil, false, fmt.Errorf("tool execution is nil at index %d", i)
		}
		d, err := c.runPreChain(ctx, exec)
		if err != nil {
			return nil, false, err
		}
		if d.Action == ToolTerminateBatch {
			terminate = true
		}
		decisions[i] = d
	}
	return decisions, terminate, nil
}

// Post runs the post chain for one executed result.
func (c *FrozenPolicyChain) Post(ctx context.Context, exec *ToolExecution, result domain.ToolResult) (PostToolDecision, error) {
	return c.runPostChain(ctx, exec, result)
}

// runPreChain is the per-exec waterfall with the container's I7 enforcement:
// sticky-deny and short-circuit-allow rejection, plus panic isolation.
func (c *FrozenPolicyChain) runPreChain(ctx context.Context, exec *ToolExecution) (PreToolDecision, error) {
	var next func() (PreToolDecision, error)
	index := 0
	next = func() (PreToolDecision, error) {
		if index >= len(c.pre) {
			// Chain end: default allow with RiskClass propagated (G4).
			return PreToolDecision{Action: ToolAllow, RiskClass: exec.RiskClass}, nil
		}
		h := c.pre[index]
		index++
		var downstream PreToolDecision
		var downstreamErr error
		downstreamSet := false

		d, err := func() (d PreToolDecision, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("pre-tool policy panic: %v", r)
				}
			}()
			return h(ctx, exec, func(e *ToolExecution) (PreToolDecision, error) {
				downstream, downstreamErr = next()
				downstreamSet = true
				return downstream, downstreamErr
			})
		}()
		if err != nil {
			return PreToolDecision{}, err
		}
		// Rule 1 (sticky-deny): a downstream deny/terminate cannot be overridden
		// by this listener returning allow/require_approval.
		if downstreamSet && isDenyDecision(downstream) && !isDenyDecision(d) {
			return PreToolDecision{}, fmt.Errorf("deny_override_attempted: downstream %q overridden by %q", downstream.Action, d.Action)
		}
		// Rule 2 (short-circuit allow): allow/require_approval may only be
		// produced by the end of the delegation chain. Short-circuiting with a
		// non-deny decision while listeners remain below is rejected.
		if !downstreamSet && !isDenyDecision(d) && index < len(c.pre) {
			return PreToolDecision{}, fmt.Errorf("allow_short_circuit_attempted: listener returned %q without delegating", d.Action)
		}
		return d, nil
	}
	return next()
}

// runPostChain is the per-exec post waterfall. Post has no deny vocabulary, so
// no sticky-deny applies.
func (c *FrozenPolicyChain) runPostChain(ctx context.Context, exec *ToolExecution, result domain.ToolResult) (PostToolDecision, error) {
	var run func(domain.ToolResult) (PostToolDecision, error)
	index := 0
	run = func(r domain.ToolResult) (PostToolDecision, error) {
		if index >= len(c.post) {
			return PostToolDecision{Action: "accept", Result: r}, nil
		}
		h := c.post[index]
		index++
		d, err := func() (d PostToolDecision, err error) {
			defer func() {
				if rec := recover(); rec != nil {
					err = fmt.Errorf("post-tool policy panic: %v", rec)
				}
			}()
			return h(ctx, exec, r, run)
		}()
		if err != nil {
			return PostToolDecision{}, err
		}
		return d, nil
	}
	return run(result)
}

func isDenyDecision(d PreToolDecision) bool {
	return d.Action == ToolDeny || d.Action == ToolTerminateBatch
}
