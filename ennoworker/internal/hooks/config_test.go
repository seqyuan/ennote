package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveHookSetMergesLayers(t *testing.T) {
	dir := t.TempDir()

	globalPath := filepath.Join(dir, "global.json")
	globalContent := `{"hooks":{"PreToolUse":{"matchers":[{"id":"block-rm","matcher":"bash|exec","hooks":[{"id":"h1","type":"command","command":"block-rm.sh"}]}]}}}`
	require.NoError(t, os.WriteFile(globalPath, []byte(globalContent), 0o600))

	wsPath := filepath.Join(dir, "workspace", ".ennote")
	require.NoError(t, os.MkdirAll(wsPath, 0o700))
	wsConfig := filepath.Join(wsPath, "config.json")
	wsContent := `{"hooks":{"PreToolUse":{"matchers":[{"id":"format-check","matcher":"write","hooks":[{"id":"h2","type":"command","command":"format.sh"}]}]}}}`
	require.NoError(t, os.WriteFile(wsConfig, []byte(wsContent), 0o600))

	global, err := loadHooksFile(globalPath, "global")
	require.NoError(t, err)
	ws, err := loadHooksFile(wsConfig, "workspace")
	require.NoError(t, err)

	resolved, err := ResolveHookSet(global, ws)
	require.NoError(t, err)

	event := resolved["PreToolUse"]
	require.Len(t, event.Matchers, 2)
	assert.Equal(t, "block-rm", event.Matchers[0].ID)
	assert.Equal(t, "format-check", event.Matchers[1].ID)
}

func TestResolveHookSetReplaceMode(t *testing.T) {
	dir := t.TempDir()

	globalPath := filepath.Join(dir, "global.json")
	require.NoError(t, os.WriteFile(globalPath, []byte(`{"hooks":{"PreToolUse":{"matchers":[{"id":"block-rm","matcher":"bash","hooks":[{"id":"h1","type":"command","command":"rm.sh"}]}]}}}`), 0o600))

	envPath := filepath.Join(dir, "env.json")
	require.NoError(t, os.WriteFile(envPath, []byte(`{"hooks":{"PreToolUse":{"mode":"replace","matchers":[{"id":"replaced","matcher":"*","hooks":[{"id":"h2","type":"command","command":"replaced.sh"}]}]}}}`), 0o600))

	global, _ := loadHooksFile(globalPath, "global")
	env, _ := loadHooksFile(envPath, "env")

	resolved, err := ResolveHookSet(global, env)
	require.NoError(t, err)

	// Replace mode: only the env matcher survives.
	event := resolved["PreToolUse"]
	require.Len(t, event.Matchers, 1)
	assert.Equal(t, "replaced", event.Matchers[0].ID)
}

func TestResolveHookSetDuplicateIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":{"matchers":[
		{"id":"m1","matcher":"bash","hooks":[{"id":"h1","type":"command","command":"a.sh"}]},
		{"id":"m1","matcher":"write","hooks":[{"id":"h2","type":"command","command":"b.sh"}]}
	]}}}`), 0o600))

	layer, err := loadHooksFile(path, "test")
	require.NoError(t, err)
	_, err = ResolveHookSet(layer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate matcher id")
}

func TestHookValidation(t *testing.T) {
	// Missing ID.
	h := HookConfig{Type: "command", Command: "echo"}
	assert.Error(t, h.Validate())

	// Invalid type.
	h = HookConfig{ID: "h1", Type: "wasm", Command: "echo"}
	assert.Error(t, h.Validate())

	// Empty command.
	h = HookConfig{ID: "h1", Type: "command"}
	assert.Error(t, h.Validate())

	// Valid.
	h = HookConfig{ID: "h1", Type: "command", Command: "echo hello"}
	assert.NoError(t, h.Validate())

	// Empty type defaults to "command".
	h = HookConfig{ID: "h2", Command: "echo"}
	assert.NoError(t, h.Validate())
}

func TestMatcherApplies(t *testing.T) {
	// Empty / * matches all.
	assert.True(t, matcherApplies("", "bash", nil))
	assert.True(t, matcherApplies("*", "bash", nil))

	// Exact match.
	assert.True(t, matcherApplies("bash", "bash", nil))
	assert.False(t, matcherApplies("bash", "write", nil))

	// Pipe list.
	assert.True(t, matcherApplies("bash|write|edit", "write", nil))
	assert.False(t, matcherApplies("bash|write", "edit", nil))

	// Regex via /.../.
	assert.True(t, matcherApplies("/read|search/", "read", nil))
	assert.True(t, matcherApplies("/read|search/", "search", nil))
	assert.False(t, matcherApplies("/read|search/", "write", nil))

	// Invalid regex is skipped.
	assert.False(t, matcherApplies("/[invalid/", "bash", nil))

	// Non-tool event matches all.
	assert.True(t, matcherApplies("bash", "", nil))
}

func TestMatchHooks(t *testing.T) {
	resolved := loadTestHookSet(t)

	// All hooks for "bash" matcher.
	hooks := resolved.MatchHooks("PreToolUse", "bash", nil)
	require.Len(t, hooks, 2)
	assert.Equal(t, "block-rm", hooks[0].ID)

	// Specific tool only matches its matcher.
	hooks = resolved.MatchHooks("PreToolUse", "write", nil)
	require.Len(t, hooks, 1)
	assert.Equal(t, "format-go", hooks[0].ID)

	// Unmatched tool returns empty.
	hooks = resolved.MatchHooks("PreToolUse", "ls", nil)
	assert.Empty(t, hooks)

	// Non-existent event returns empty.
	hooks = resolved.MatchHooks("PostToolUse", "bash", nil)
	assert.Empty(t, hooks)
}

func TestHookSetDigest(t *testing.T) {
	resolved := loadTestHookSet(t)
	d1, err := resolved.Digest()
	require.NoError(t, err)
	d2, err := resolved.Digest()
	require.NoError(t, err)
	assert.Equal(t, d1, d2) // same set produces same digest
}

func TestEmptyHookSet(t *testing.T) {
	set := HookSet{}
	assert.True(t, set.IsEmpty())
	hooks := set.MatchHooks("PreToolUse", "bash", nil)
	assert.Nil(t, hooks)
}

func loadTestHookSet(t *testing.T) HookSet {
	t.Helper()
	var layer HookLayer
	require.NoError(t, json.Unmarshal([]byte(`{
		"hooks": {
			"PreToolUse": {
				"matchers": [
					{
						"id": "block-rm",
						"matcher": "bash|exec",
						"hooks": [
							{"id":"block-rm","type":"command","command":"block-rm.sh"},
							{"id":"block-rf","type":"command","command":"block-rf.sh"}
						]
					},
					{
						"id": "format-write",
						"matcher": "write|edit",
						"hooks": [
							{"id":"format-go","type":"command","command":"gofmt.sh"}
						]
					}
				]
			}
		}
	}`), &layer))
	resolved, err := ResolveHookSet(&layer)
	require.NoError(t, err)
	return resolved
}
