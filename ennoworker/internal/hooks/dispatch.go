// This file implements the Dispatcher: for one event it matches the configured
// hooks, runs them in order via the Runner, and merges their outputs into a
// single Decision. It owns the fail-open policy (a hook that fails to run
// is warned about and skipped, never blocking the agent) and the PreToolUse
// short-circuit (the first block stops further hooks so a blocked call is not
// also rewritten).
package hooks

import (
	"context"
	"io"
	"os"
	"strings"
)

// EventPreToolUse is the one event whose first block short-circuits the rest
// of the chain.
const EventPreToolUse = "PreToolUse"

// Dispatcher runs the hooks configured for an event and merges their results.
// A nil *Dispatcher is a valid no-op (Dispatch returns an empty Decision).
type Dispatcher struct {
	set     HookSet
	runner  *Runner
	warnLog io.Writer
}

// NewDispatcher builds a dispatcher over the given hook set. It returns nil
// when the set is empty, so the common no-hooks case costs nothing and
// callers can treat nil as "hooks disabled".
func NewDispatcher(set HookSet, projectDir string, warnLog io.Writer) *Dispatcher {
	if set.IsEmpty() {
		return nil
	}
	if warnLog == nil {
		warnLog = os.Stderr
	}
	return &Dispatcher{
		set:     set,
		runner:  &Runner{ProjectDir: projectDir, WarnLog: warnLog},
		warnLog: warnLog,
	}
}

// Dispatch runs every hook matching (eventType, toolName) in order and returns
// the merged decision. On a nil dispatcher or no matched hooks it returns the
// zero Decision. For PreToolUse the first block stops the chain so a blocked
// call is not subsequently rewritten.
//
// @mode serial-merge
func (d *Dispatcher) Dispatch(ctx context.Context, eventType, toolName string, input HookInput) Decision {
	var dec Decision
	if d == nil {
		return dec
	}
	matched := d.set.MatchHooks(eventType, toolName, d.warnLog)
	for _, h := range matched {
		out, err := d.runner.Run(ctx, h, input)
		if err != nil {
			warnf(d.warnLog, "hooks: %s: %v\n", eventType, err)
			continue // fail-open
		}
		if out.Blocks() {
			dec.Block = true
			dec.Reason = joinNonEmpty(dec.Reason, out.Reason, "\n")
		}
		if out.AdditionalContext != "" {
			dec.AdditionalContext = joinNonEmpty(dec.AdditionalContext, out.AdditionalContext, "\n")
		}
		if len(out.UpdatedInput) > 0 {
			dec.UpdatedInput = out.UpdatedInput // last writer wins
		}
		if dec.Block && eventType == EventPreToolUse {
			break // blocked tool call is not also rewritten
		}
	}
	return dec
}

// DispatchSync is like Dispatch but for events that run synchronously and may
// return an error only on infrastructure failure (not on block).
// It returns the decision and a non-nil error only when the dispatch
// infrastructure itself failed (should be treated as fail-open).
func (d *Dispatcher) DispatchSync(ctx context.Context, eventType, toolName string, input HookInput) (Decision, error) {
	return d.Dispatch(ctx, eventType, toolName, input), nil
}

func joinNonEmpty(a, b, sep string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + sep + b
	}
}
