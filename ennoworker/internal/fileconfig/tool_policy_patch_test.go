package fileconfig

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyToolPolicyPatchWholeRowReplacement(t *testing.T) {
	// A patch replaces the whole config with a DIFFERENT field set — no merge.
	patched, source, err := ApplyToolPolicyPatch(PatchOp{
		ID:     "policy-profile-1",
		Source: "project",
		Set:    json.RawMessage(`{"mode":"auto","allowedTools":["read","write","custom"]}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "project", source)
	assert.Equal(t, []string{"read", "write", "custom"}, patched.AllowedTools)
	assert.Equal(t, "auto", patched.Mode)
	// Fields absent from the patch are zero — the previous layer's values are
	// never carried over.
	assert.Empty(t, patched.RedactPatterns)
	assert.Empty(t, patched.AllowedExecutables)
}

func TestApplyToolPolicyPatchDefaultsSource(t *testing.T) {
	patched, source, err := ApplyToolPolicyPatch(PatchOp{
		ID:  "policy-profile-1",
		Set: json.RawMessage(`{"mode":"ask"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "project", source)
	assert.Equal(t, "ask", patched.Mode)
}

func TestApplyToolPolicyPatchRejectsInvalid(t *testing.T) {
	_, _, err := ApplyToolPolicyPatch(PatchOp{ID: "", Set: json.RawMessage(`{}`)})
	assert.Error(t, err)

	_, _, err = ApplyToolPolicyPatch(PatchOp{ID: "p1", Set: nil})
	assert.Error(t, err)

	_, _, err = ApplyToolPolicyPatch(PatchOp{ID: "p1", Set: json.RawMessage(`{invalid`)})
	assert.Error(t, err)
}
