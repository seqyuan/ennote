package store

import (
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/systemprompt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodeSnapshotForTest builds a valid frozen snapshot the way the freezer
// would, so the decode tests exercise the real validation path.
func encodeSnapshotForTest(t *testing.T, sections []domain.PromptSection, sectionsDigest string) (string, string) {
	t.Helper()
	snapshot, err := newSystemPromptSnapshot("profile-1", "Review evidence.")
	require.NoError(t, err)
	snapshot.Sections = sections
	snapshot.SectionsDigest = sectionsDigest
	if len(sections) > 0 {
		snapshot.ComposedDigest = systemprompt.TextDigest("composed text")
	}
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	return string(encoded), snapshot.Digest
}

func TestDecodeSystemPromptSnapshotAcceptsRecordedSections(t *testing.T) {
	sections := []domain.PromptSection{{
		ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Bytes: 16, Digest: systemprompt.TextDigest("Review evidence."),
	}}
	digest, err := systemprompt.SectionsDigest(sections)
	require.NoError(t, err)
	encoded, snapshotDigest := encodeSnapshotForTest(t, sections, digest)

	decoded, err := decodeSystemPromptSnapshot(encoded, snapshotDigest)
	require.NoError(t, err)
	assert.Equal(t, sections, decoded.Sections)
	assert.Equal(t, digest, decoded.SectionsDigest)
}

// A Run frozen before the composition freeze existed carries no sections and
// must keep decoding: backward compatibility is part of the freeze contract.
func TestDecodeSystemPromptSnapshotAcceptsSnapshotsWithoutSections(t *testing.T) {
	encoded, snapshotDigest := encodeSnapshotForTest(t, nil, "")

	decoded, err := decodeSystemPromptSnapshot(encoded, snapshotDigest)
	require.NoError(t, err)
	assert.Empty(t, decoded.Sections)
	assert.Empty(t, decoded.SectionsDigest)
}

// Tampering with any recorded section must fail closed rather than let a Run
// resume against a composition that is not the one that was frozen.
func TestDecodeSystemPromptSnapshotFailsClosedOnTamperedSections(t *testing.T) {
	sections := []domain.PromptSection{
		{ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Bytes: 16, Digest: "a"},
		{ID: "role.definition", Kind: domain.PromptSectionRole, Source: "role:analyst@2", Bytes: 20, Digest: "b"},
	}
	digest, err := systemprompt.SectionsDigest(sections)
	require.NoError(t, err)
	_, snapshotDigest := encodeSnapshotForTest(t, sections, digest)

	mutations := map[string]func(t *testing.T) string{
		"changed section digest": func(t *testing.T) string {
			t.Helper()
			// The per-section digest changes but the recorded sections digest is
			// left stale: exactly the tamper validation must catch.
			out, _ := encodeSnapshotForTest(t, []domain.PromptSection{
				sections[0], {ID: "role.definition", Kind: domain.PromptSectionRole, Source: "role:analyst@2", Bytes: 20, Digest: "c"},
			}, digest)
			return out
		},
		"changed byte count": func(t *testing.T) string {
			t.Helper()
			mutated := []domain.PromptSection{sections[0], {ID: "role.definition", Kind: domain.PromptSectionRole, Source: "role:analyst@2", Bytes: 21, Digest: "b"}}
			// The digest is deliberately NOT recomputed: this is exactly the
			// tamper the validation must catch.
			out, _ := encodeSnapshotForTest(t, mutated, digest)
			return out
		},
		"changed source": func(t *testing.T) string {
			t.Helper()
			mutated := []domain.PromptSection{sections[0], {ID: "role.definition", Kind: domain.PromptSectionRole, Source: "role:analyst@3", Bytes: 20, Digest: "b"}}
			out, _ := encodeSnapshotForTest(t, mutated, digest)
			return out
		},
		"reordered sections": func(t *testing.T) string {
			t.Helper()
			out, _ := encodeSnapshotForTest(t, []domain.PromptSection{sections[1], sections[0]}, digest)
			return out
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			_, err := decodeSystemPromptSnapshot(mutate(t), snapshotDigest)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "sections digest mismatch")
		})
	}
}

// Sections without a digest must never be accepted: an unverifiable record is
// worse than no record, because it looks authoritative.
func TestDecodeSystemPromptSnapshotRejectsUnguardedSections(t *testing.T) {
	sections := []domain.PromptSection{{
		ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Bytes: 1, Digest: "a",
	}}
	encoded, snapshotDigest := encodeSnapshotForTest(t, sections, "")

	_, err := decodeSystemPromptSnapshot(encoded, snapshotDigest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sections without a sections digest")
}

// The recorded catalog state is a closed vocabulary, not free text: an unknown
// value would render as a state no reader can interpret.
func TestDecodeSystemPromptSnapshotRejectsUnknownSkillCatalogState(t *testing.T) {
	encoded, snapshotDigest := encodeSnapshotForTest(t, nil, "")
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &snapshot))
	snapshot["skillCatalogState"] = "partially-materialized"
	patched, err := json.Marshal(snapshot)
	require.NoError(t, err)

	_, err = decodeSystemPromptSnapshot(string(patched), snapshotDigest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown skill catalog state")

	// Every recorded state decodes.
	for _, state := range []domain.SkillCatalogState{
		domain.SkillCatalogUnrecorded, domain.SkillCatalogMaterialized, domain.SkillCatalogDisabled,
	} {
		snapshot["skillCatalogState"] = string(state)
		patched, err := json.Marshal(snapshot)
		require.NoError(t, err)
		_, err = decodeSystemPromptSnapshot(string(patched), snapshotDigest)
		require.NoError(t, err, "state %q must decode", state)
	}
}

// The base-prompt digest formula is untouched by this change, so a snapshot
// encoded before sections existed still validates against its stored digest.
func TestSystemPromptSnapshotDigestIgnoresCompositionFields(t *testing.T) {
	plain, err := newSystemPromptSnapshot("profile-1", "Review evidence.")
	require.NoError(t, err)

	withSections, err := newSystemPromptSnapshot("profile-1", "Review evidence.")
	require.NoError(t, err)
	withSections.Sections = []domain.PromptSection{{ID: "base", Kind: domain.PromptSectionBase}}
	withSections.SectionsDigest = "whatever"
	withSections.ComposedDigest = "whatever"

	assert.Equal(t, plain.Digest, withSections.Digest,
		"composition provenance must not change the frozen base-prompt digest")
}
