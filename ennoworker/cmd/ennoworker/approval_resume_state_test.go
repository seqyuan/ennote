package main

import (
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resumeStateJSON(t *testing.T, state map[string]any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	return encoded
}

func TestDecodeFrozenResumeStateAcceptsTheCurrentCheckpoint(t *testing.T) {
	state, err := decodeFrozenResumeState(resumeStateJSON(t, map[string]any{
		"version": agent.ResumeStateVersion, "iteration": 3,
		"approvalDigestVersion": agent.ApprovalDigestV2,
		"systemPrompt":          "frozen prompt",
	}))
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, agent.ResumeStateVersion, state.Version)
	assert.Equal(t, 3, state.Iteration)
	assert.Equal(t, "frozen prompt", state.SystemPrompt)
}

// A legacy checkpoint predates the digest-version field, so an absent version
// means V1. A legacy checkpoint that explicitly claims V2 is inconsistent and
// must not be coerced.
func TestDecodeFrozenResumeStateNormalizesLegacyDigestVersion(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4} {
		state, err := decodeFrozenResumeState(resumeStateJSON(t, map[string]any{
			"version": version, "iteration": 1,
		}))
		require.NoError(t, err, "legacy version %d", version)
		assert.Equal(t, agent.ApprovalDigestV1, state.ApprovalDigestVersion,
			"legacy version %d must normalize to V1", version)

		explicit, err := decodeFrozenResumeState(resumeStateJSON(t, map[string]any{
			"version": version, "iteration": 1, "approvalDigestVersion": agent.ApprovalDigestV1,
		}))
		require.NoError(t, err)
		assert.Equal(t, agent.ApprovalDigestV1, explicit.ApprovalDigestVersion)

		_, err = decodeFrozenResumeState(resumeStateJSON(t, map[string]any{
			"version": version, "iteration": 1, "approvalDigestVersion": agent.ApprovalDigestV2,
		}))
		require.Error(t, err, "legacy version %d must not claim V2", version)
	}
}

// The checkpoint decides which tool batch the model may execute next, so the
// current version must carry the current digest version: a mismatch means the
// batch digest was not computed the way this binary computes it.
func TestDecodeFrozenResumeStateRejectsACurrentCheckpointWithoutTheCurrentDigestVersion(t *testing.T) {
	for _, digestVersion := range []int{0, agent.ApprovalDigestV1} {
		_, err := decodeFrozenResumeState(resumeStateJSON(t, map[string]any{
			"version": agent.ResumeStateVersion, "iteration": 1,
			"approvalDigestVersion": digestVersion,
		}))
		require.Error(t, err, "digest version %d must be rejected for the current checkpoint", digestVersion)
	}
}

func TestDecodeFrozenResumeStateRejectsAnUnsupportedVersion(t *testing.T) {
	_, err := decodeFrozenResumeState(resumeStateJSON(t, map[string]any{
		"version": agent.ResumeStateVersion + 1, "iteration": 1,
	}))
	require.Error(t, err)

	// A JSON null decodes into the zero value, which is not a valid checkpoint.
	_, err = decodeFrozenResumeState(json.RawMessage(`null`))
	require.Error(t, err)
}

func TestDecodeFrozenResumeStateRejectsMalformedState(t *testing.T) {
	_, err := decodeFrozenResumeState(json.RawMessage(`{"version":`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

// The child path and the public path must accept exactly the same checkpoints:
// one decoder, one matrix.
func TestDecodeFrozenResumeStateIsTheOnlyVersionMatrix(t *testing.T) {
	_, err := decodeFrozenResumeState(json.RawMessage(`{"version":0,"iteration":1}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported checkpoint version")
	assert.Equal(t, domain.ErrorApprovalCheckpointInvalid, domain.ErrorApprovalCheckpointInvalid)
}
