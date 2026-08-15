package hooks

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDispatchSerialMergeAccumulatesContext pins the serial-merge mode
// contract (@mode serial-merge): every matching hook runs in order,
// AdditionalContext accumulates, and none of them block.
func TestDispatchSerialMergeAccumulatesContext(t *testing.T) {
	set := HookSet{
		"PostToolUse": EventHookSet{
			Matchers: []HookMatcherConfig{
				{ID: "m1", Hooks: []HookConfig{
					{ID: "h1", Type: CommandType, Command: `printf '{"additionalContext":"ctx-one"}'`},
					{ID: "h2", Type: CommandType, Command: `printf '{"additionalContext":"ctx-two"}'`},
				}},
			},
		},
	}
	d := NewDispatcher(set, t.TempDir(), io.Discard)
	require.NotNil(t, d)

	dec := d.Dispatch(context.Background(), "PostToolUse", "bash", HookInput{ToolName: "bash"})
	assert.False(t, dec.Block)
	assert.Equal(t, "ctx-one\nctx-two", dec.AdditionalContext)
}

// TestDispatchPreToolUseShortCircuitsOnBlock pins the serial-merge mode
// contract: the first PreToolUse block stops the chain, so a blocked call is
// never also rewritten by a later hook.
func TestDispatchPreToolUseShortCircuitsOnBlock(t *testing.T) {
	set := HookSet{
		"PreToolUse": EventHookSet{
			Matchers: []HookMatcherConfig{
				{ID: "m1", Hooks: []HookConfig{
					{ID: "block", Type: CommandType, Command: `printf '{"decision":"block","reason":"nope"}'`},
					{ID: "rewrite", Type: CommandType, Command: `printf '{"updatedInput":{"command":"ls"}}'`},
				}},
			},
		},
	}
	d := NewDispatcher(set, t.TempDir(), io.Discard)
	require.NotNil(t, d)

	dec := d.Dispatch(context.Background(), EventPreToolUse, "bash", HookInput{ToolName: "bash"})
	assert.True(t, dec.Block)
	assert.Equal(t, "nope", dec.Reason)
	assert.Empty(t, dec.UpdatedInput) // later hook never ran
}

// TestDispatchSerialMergeLastWriterWins pins the serial-merge UpdatedInput
// contract: later hooks overwrite earlier rewrites.
func TestDispatchSerialMergeLastWriterWins(t *testing.T) {
	set := HookSet{
		"PreToolUse": EventHookSet{
			Matchers: []HookMatcherConfig{
				{ID: "m1", Hooks: []HookConfig{
					{ID: "first", Type: CommandType, Command: `printf '{"updatedInput":{"command":"first"}}'`},
					{ID: "second", Type: CommandType, Command: `printf '{"updatedInput":{"command":"second"}}'`},
				}},
			},
		},
	}
	d := NewDispatcher(set, t.TempDir(), io.Discard)
	require.NotNil(t, d)

	dec := d.Dispatch(context.Background(), EventPreToolUse, "bash", HookInput{ToolName: "bash"})
	assert.False(t, dec.Block)
	assert.JSONEq(t, `{"command":"second"}`, string(dec.UpdatedInput))
}
