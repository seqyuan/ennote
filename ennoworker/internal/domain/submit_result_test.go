package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every accepted blocker is accepted on both statuses it is defined for. This is
// the whole vocabulary: three values, no fourth, and none of them is a default.
func TestValidateSubmitResultAcceptsEveryBlocker(t *testing.T) {
	for _, status := range []SubmitStatus{SubmitBlocked, SubmitNeedsInput} {
		for _, blocker := range SubmitBlockers {
			arguments := json.RawMessage(`{"status":"` + string(status) + `","blocker":"` +
				string(blocker) + `","summary":"stopped"}`)
			result, err := ValidateSubmitResult(arguments)
			require.NoError(t, err, "%s/%s must be accepted", status, blocker)
			assert.Equal(t, status, result.Status)
			assert.Equal(t, blocker, result.Blocker)
		}
	}
}

// A blocked or needs_input result without a blocker is refused: that is exactly
// the ambiguity the field exists to remove, and a silent "" would be read as
// "completed with a caveat".
func TestValidateSubmitResultRequiresBlockerOnBlocked(t *testing.T) {
	for _, status := range []SubmitStatus{SubmitBlocked, SubmitNeedsInput} {
		_, err := ValidateSubmitResult(json.RawMessage(`{"status":"` + string(status) + `","summary":"stopped"}`))
		require.Error(t, err)
		message := err.Error()
		assert.Contains(t, message, "blocker", "the message must name the field")
		for _, accepted := range SubmitBlockers {
			assert.Contains(t, message, string(accepted), "the message must list %s", accepted)
		}
	}
}

// An unknown blocker value is refused, and the message lists what is accepted: a
// reviewer must not have to open the source to learn the vocabulary.
func TestValidateSubmitResultRejectsUnknownBlocker(t *testing.T) {
	_, err := ValidateSubmitResult(json.RawMessage(`{"status":"blocked","blocker":"run_failed","summary":"stopped"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocker")
	assert.Contains(t, err.Error(), "run_failed")
	assert.Contains(t, err.Error(), "missing_evidence")
}

// completed must not carry a blocker: a finished task has nothing blocking it,
// and accepting one would let the fold read two statuses for one fact.
func TestValidateSubmitResultRejectsBlockerOnCompleted(t *testing.T) {
	_, err := ValidateSubmitResult(json.RawMessage(
		`{"status":"completed","blocker":"missing_evidence","summary":"done"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocker")
	assert.Contains(t, err.Error(), "completed")
	assert.Contains(t, err.Error(), "missing_evidence")
}

// Regression: a completed result without the field is untouched, and an old
// blocked result (no blocker key at all) still decodes - the field is optional
// on the wire, its absence is just never read as a reason.
func TestValidateSubmitResultWithoutBlockerUnchanged(t *testing.T) {
	result, err := ValidateSubmitResult(json.RawMessage(`{"status":"completed","summary":"done"}`))
	require.NoError(t, err)
	assert.Equal(t, SubmitCompleted, result.Status)
	assert.Equal(t, SubmitBlocker(""), result.Blocker)

	// An old stored result keeps the old terminal contract: it decodes, and the
	// empty blocker is not turned into a guess.
	var stored SubmitResult
	require.NoError(t, json.Unmarshal([]byte(`{"status":"blocked","summary":"legacy"}`), &stored))
	assert.Equal(t, SubmitBlocked, stored.Status)
	assert.Equal(t, SubmitBlocker(""), stored.Blocker)
}

// The blocker is part of what delegation_items.result_json stores: the whole
// struct is marshalled, so a decoded result keeps the typed reason and an unset
// one adds no key at all.
func TestSubmitResultBlockerPersistsInTheStoredResult(t *testing.T) {
	encoded, err := json.Marshal(SubmitResult{Status: SubmitNeedsInput,
		Blocker: BlockerNeedsUserInput, Summary: "which sample?"})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"blocker":"needs_user_input"`)
	var decoded SubmitResult
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, BlockerNeedsUserInput, decoded.Blocker)

	without, err := json.Marshal(SubmitResult{Status: SubmitCompleted, Summary: "done"})
	require.NoError(t, err)
	assert.NotContains(t, string(without), "blocker",
		"an absent blocker must not be written as an empty string")
}
