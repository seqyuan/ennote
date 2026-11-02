package store

import (
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/buildinfo"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A frozen Run must name the build that froze it, otherwise a support report
// cannot be tied to a binary.
func TestNewSystemPromptSnapshotRecordsTheBuildVersion(t *testing.T) {
	snapshot, err := newSystemPromptSnapshot("profile-1", "Review evidence.")
	require.NoError(t, err)
	assert.Equal(t, buildinfo.Version, snapshot.PlatformVersion)
}

// Snapshots written before the build version was stamped carry the platform tag
// "hosted-v1". They must keep decoding: an upgrade cannot invalidate the frozen
// config of every Run already in flight.
func TestDecodeSystemPromptSnapshotAcceptsALegacyPlatformVersion(t *testing.T) {
	legacy := domain.SystemPromptSnapshot{Version: 1, PlatformVersion: "hosted-v1", AgentPrompt: "Review evidence."}
	digest, err := digestSystemPromptSnapshot(legacy)
	require.NoError(t, err)
	legacy.Digest = digest
	encoded, err := json.Marshal(legacy)
	require.NoError(t, err)

	decoded, err := decodeSystemPromptSnapshot(string(encoded), digest)
	require.NoError(t, err)
	assert.Equal(t, "hosted-v1", decoded.PlatformVersion)
	assert.Equal(t, "Review evidence.", decoded.AgentPrompt)
}

// The platform version is part of what the digest commits, so rewriting it
// breaks verification instead of silently relabeling an old Run as a new build.
func TestDecodeSystemPromptSnapshotFailsClosedOnARewrittenPlatformVersion(t *testing.T) {
	snapshot, err := newSystemPromptSnapshot("profile-1", "Review evidence.")
	require.NoError(t, err)
	snapshot.PlatformVersion = "hosted-v1"
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)

	_, err = decodeSystemPromptSnapshot(string(encoded), snapshot.Digest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "digest mismatch")
}

// An empty platform version is unverifiable provenance: it says a Run was frozen
// by a build nobody can name.
func TestDecodeSystemPromptSnapshotRejectsAnEmptyPlatformVersion(t *testing.T) {
	snapshot, err := newSystemPromptSnapshot("profile-1", "Review evidence.")
	require.NoError(t, err)
	snapshot.PlatformVersion = ""
	digest, err := digestSystemPromptSnapshot(snapshot)
	require.NoError(t, err)
	snapshot.Digest = digest
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)

	_, err = decodeSystemPromptSnapshot(string(encoded), digest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "digest mismatch")
}
