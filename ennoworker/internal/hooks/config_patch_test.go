package hooks

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func layerFromJSON(t *testing.T, source, raw string) *HookLayer {
	t.Helper()
	layer := &HookLayer{Source: source}
	require.NoError(t, json.Unmarshal([]byte(raw), layer))
	return layer
}

// TestResolveHookSetAppliesMatcherPatches pins the design 六 P1 semantics:
// a later layer patches one matcher by id (whole-row replacement) without
// touching the append/replace of the other matchers.
func TestResolveHookSetAppliesMatcherPatches(t *testing.T) {
	global := layerFromJSON(t, "global", `{
		"hooks": {"PreToolUse": {"matchers": [
			{"id":"m1","hooks":[{"id":"h1","type":"command","command":"printf a"}]},
			{"id":"m2","hooks":[{"id":"h2","type":"command","command":"printf b"}]}
		]}}
	}`)
	project := layerFromJSON(t, "project", `{
		"hookPatches": [
			{"id":"m1","set":{"id":"m1","matcher":"bash","hooks":[{"id":"h1","type":"command","command":"printf replaced"}]}}
		]
	}`)

	set, err := ResolveHookSet(global, project)
	require.NoError(t, err)

	pre := set["PreToolUse"]
	require.Len(t, pre.Matchers, 2)
	assert.Equal(t, "m1", pre.Matchers[0].ID)
	assert.Equal(t, "bash", pre.Matchers[0].Matcher) // whole-row replacement
	assert.Equal(t, "printf replaced", pre.Matchers[0].Hooks[0].Command)
	assert.Equal(t, "m2", pre.Matchers[1].ID) // untouched
}

// TestResolveHookSetPatchDeletesMatcher pins the delete semantics: Set omitted
// removes the matcher, leaving the rest intact.
func TestResolveHookSetPatchDeletesMatcher(t *testing.T) {
	global := layerFromJSON(t, "global", `{
		"hooks": {"PreToolUse": {"matchers": [
			{"id":"m1","hooks":[{"id":"h1","type":"command","command":"printf a"}]},
			{"id":"m2","hooks":[{"id":"h2","type":"command","command":"printf b"}]}
		]}}
	}`)
	project := layerFromJSON(t, "project", `{"hookPatches":[{"id":"m1"}]}`)

	set, err := ResolveHookSet(global, project)
	require.NoError(t, err)
	require.Len(t, set["PreToolUse"].Matchers, 1)
	assert.Equal(t, "m2", set["PreToolUse"].Matchers[0].ID)
}

// TestResolveHookSetUnknownPatchFailsLoud pins the fail-loud behaviour for a
// patch targeting a matcher id that does not exist.
func TestResolveHookSetUnknownPatchFailsLoud(t *testing.T) {
	global := layerFromJSON(t, "global", `{"hooks":{"PreToolUse":{"matchers":[{"id":"m1","hooks":[]}]}}}`)
	project := layerFromJSON(t, "project", `{"hookPatches":[{"id":"missing","set":{}}]}`)

	_, err := ResolveHookSet(global, project)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown matcher id")
}
