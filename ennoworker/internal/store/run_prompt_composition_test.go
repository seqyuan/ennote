package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/systemprompt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compositionSections() []domain.PromptSection {
	return []domain.PromptSection{
		{ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Bytes: 5, Digest: systemprompt.TextDigest("base\n")},
		{ID: "context.agents.project", Kind: domain.PromptSectionContext, Source: "project:AGENTS.md", Bytes: 12, Digest: systemprompt.TextDigest("project rules")},
	}
}

// claimedRunFrozen mirrors production: config freezing happens at Claim time
// (ResolveAndFreezeConfig is what writes agent_runs.system_prompt_snapshot_json),
// so a composition freeze always has a frozen base prompt to bind to.
func claimedRunFrozen(t *testing.T, fixture *fileRunFixture, requestID string) *domain.AgentRun {
	t.Helper()
	run := fixture.SubmitAndClaim(t, requestID)
	_, err := fixture.Runs.ResolveAndFreezeConfig(context.Background(), run)
	require.NoError(t, err)
	return run
}

func composedDigestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func TestFreezeSystemPromptCompositionRecordsSectionsAndText(t *testing.T) {
	fixture := newFileRunFixture(t, "composition")
	run := claimedRunFrozen(t, fixture, "composition-1")
	sections := compositionSections()
	prompt := "base\nproject rules"

	require.NoError(t, fixture.Runs.FreezeSystemPromptComposition(context.Background(), run.ID, prompt, sections))

	// The exact prompt bytes live once, addressed by content.
	var bytes int
	var text string
	require.NoError(t, fixture.DB.QueryRow(`SELECT bytes, text FROM prompt_blobs WHERE digest=?`,
		composedDigestOf(prompt)).Scan(&bytes, &text))
	assert.Equal(t, len(prompt), bytes)
	assert.Equal(t, prompt, text)

	// The Run carries the section identity record and both digests.
	var stored string
	require.NoError(t, fixture.DB.QueryRow(
		`SELECT system_prompt_snapshot_json FROM agent_runs WHERE id=?`, run.ID).Scan(&stored))
	var snapshot domain.SystemPromptSnapshot
	require.NoError(t, json.Unmarshal([]byte(stored), &snapshot))
	expectedSectionsDigest, err := systemprompt.SectionsDigest(sections)
	require.NoError(t, err)
	assert.Equal(t, expectedSectionsDigest, snapshot.SectionsDigest)
	assert.Equal(t, composedDigestOf(prompt), snapshot.ComposedDigest)
	assert.Equal(t, sections, snapshot.Sections)

	// The read projection returns the same facts.
	loaded, err := fixture.Runs.LoadRunPromptComposition(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, run.ID, loaded.RunID)
	assert.Equal(t, prompt, loaded.Prompt)
	assert.Equal(t, sections, loaded.Sections)
	assert.Equal(t, expectedSectionsDigest, loaded.SectionsDigest)
	assert.Equal(t, composedDigestOf(prompt), loaded.ComposedDigest)
	assert.Equal(t, 1, loaded.Version)
	assert.NotEmpty(t, loaded.Digest, "the frozen base-prompt digest is preserved")
}

func TestFreezeSystemPromptCompositionIsIdempotentForTheSameComposition(t *testing.T) {
	fixture := newFileRunFixture(t, "composition-idempotent")
	run := claimedRunFrozen(t, fixture, "composition-2")
	sections := compositionSections()

	require.NoError(t, fixture.Runs.FreezeSystemPromptComposition(context.Background(), run.ID, "base\nproject rules", sections))
	require.NoError(t, fixture.Runs.FreezeSystemPromptComposition(context.Background(), run.ID, "base\nproject rules", sections))

	var blobs, runs int
	require.NoError(t, fixture.DB.QueryRow(`SELECT COUNT(*) FROM prompt_blobs`).Scan(&blobs))
	require.NoError(t, fixture.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE id=?`, run.ID).Scan(&runs))
	assert.Equal(t, 1, blobs, "an identical composition reuses one content-addressed row")
	assert.Equal(t, 1, runs)
}

func TestFreezeSystemPromptCompositionRejectsConflictingRewrite(t *testing.T) {
	fixture := newFileRunFixture(t, "composition-conflict")
	run := claimedRunFrozen(t, fixture, "composition-3")
	require.NoError(t, fixture.Runs.FreezeSystemPromptComposition(
		context.Background(), run.ID, "base\nproject rules", compositionSections()))

	conflicting := []domain.PromptSection{{
		ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Bytes: 9, Digest: systemprompt.TextDigest("different"),
	}}
	err := fixture.Runs.FreezeSystemPromptComposition(context.Background(), run.ID, "different", conflicting)
	require.ErrorIs(t, err, store.ErrSystemPromptCompositionFrozen)

	// The original composition is untouched.
	loaded, err := fixture.Runs.LoadRunPromptComposition(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "base\nproject rules", loaded.Prompt)
	assert.Equal(t, compositionSections(), loaded.Sections)
}

func TestFreezeSystemPromptCompositionRequiresARunningRun(t *testing.T) {
	fixture := newFileRunFixture(t, "composition-state")
	run := claimedRunFrozen(t, fixture, "composition-4")
	require.NoError(t, fixture.Runs.Fail(context.Background(), run.ID, "test_failed", "stub"))

	err := fixture.Runs.FreezeSystemPromptComposition(context.Background(), run.ID, "base", compositionSections())
	require.ErrorIs(t, err, store.ErrInvalidRunState)
}

func TestFreezeSystemPromptCompositionRejectsUnknownRun(t *testing.T) {
	fixture := newFileRunFixture(t, "composition-missing")
	err := fixture.Runs.FreezeSystemPromptComposition(context.Background(),
		"00000000-0000-4000-8000-000000000000", "base", compositionSections())
	require.ErrorIs(t, err, store.ErrRunNotFound)
}

func TestLoadRunPromptCompositionReportsUnrecordedComposition(t *testing.T) {
	fixture := newFileRunFixture(t, "composition-unrecorded")
	run := claimedRunFrozen(t, fixture, "composition-5")

	loaded, err := fixture.Runs.LoadRunPromptComposition(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Empty(t, loaded.Prompt, "a Run whose composition was never recorded reads as unrecorded")
	assert.Empty(t, loaded.Sections)
	assert.Empty(t, loaded.ComposedDigest)
	assert.NotEmpty(t, loaded.Digest, "the base-prompt digest is still reported")
}

func TestFreezeSystemPromptCompositionStoresEmptyCompositionWithoutBlob(t *testing.T) {
	fixture := newFileRunFixture(t, "composition-empty")
	run := claimedRunFrozen(t, fixture, "composition-6")

	require.NoError(t, fixture.Runs.FreezeSystemPromptComposition(context.Background(), run.ID, "", nil))

	var blobs int
	require.NoError(t, fixture.DB.QueryRow(`SELECT COUNT(*) FROM prompt_blobs`).Scan(&blobs))
	assert.Equal(t, 0, blobs)

	loaded, err := fixture.Runs.LoadRunPromptComposition(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Empty(t, loaded.Sections)
	assert.Empty(t, loaded.ComposedDigest)
}
