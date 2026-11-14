package globalsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func graphHeroDocument(handle, prompt string) *heroesource.Document {
	return &heroesource.Document{
		SchemaVersion: 1, Handle: handle, Name: handle,
		Model:  heroesource.ModelBinding{Ref: "provider/model", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 2, MaxToolCalls: 4,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 4, Prompt: prompt,
	}
}

func graphDefinition(id string) *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: id,
		Budget: domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"review": {Type: domain.FlowTaskHero, Hero: "local/reviewer", Goal: "Review the workspace"},
			"gate":   {Type: domain.FlowTaskCheck, Command: "test -f ready", Depends: []string{"review"}},
			"done": {Type: domain.FlowTaskHero, Depends: []string{"gate"},
				Terminal: &domain.FlowTerminal{Status: "success"}},
		},
	}
}

func TestGraphRevisionsBundleLocalHeroesImmutably(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	definition := graphDefinition("qc-graph")
	digest, err := store.CreateGraphFromDefinition(definition)
	require.NoError(t, err)
	require.True(t, len(digest) > len("sha256:"))
	assert.Equal(t, "sha256:"+mustConfigDigest(t, definition), digest)

	_, err = store.CreateGraphHero("qc-graph", graphHeroDocument("reviewer", "first prompt"))
	require.NoError(t, err)
	first, err := store.PublishGraphRevision("qc-graph")
	require.NoError(t, err)
	assert.Equal(t, SourceKindGraph, first.SourceKind)
	assert.Equal(t, digest, first.Digest)
	require.Contains(t, first.HeroDigests, "reviewer")

	revisionDefinition, heroes, read, err := store.ReadGraphDefinitionRevision("qc-graph", first.ID())
	require.NoError(t, err)
	assert.Equal(t, first, read)
	assert.Equal(t, "first prompt", heroes["reviewer"].Prompt)
	assert.Equal(t, domain.FlowTaskCheck, revisionDefinition.Tasks["gate"].Type)

	// Editing the draft hero after publish must not change the published bundle.
	encoded, err := heroesource.Encode(graphHeroDocument("reviewer", "second prompt"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store.GraphsDir(), "qc-graph", "heroes", "reviewer", "hero.md"), encoded, 0o600))
	_, immutableHeroes, _, err := store.ReadGraphDefinitionRevision("qc-graph", first.ID())
	require.NoError(t, err)
	assert.Equal(t, "first prompt", immutableHeroes["reviewer"].Prompt)

	// A hero-only change still creates a new immutable revision; the definition
	// digest is intentionally unchanged because it commits hero references, not
	// hero bodies.
	second, err := store.PublishGraphRevision("qc-graph")
	require.NoError(t, err)
	assert.Equal(t, "v000002", second.ID())
	assert.Equal(t, first.Digest, second.Digest)
	assert.NotEqual(t, first.HeroDigests["reviewer"], second.HeroDigests["reviewer"])

	// Tampering with the bundled hero fails closed.
	path := filepath.Join(store.GraphsDir(), "qc-graph", "revisions", first.ID(), "heroes", "reviewer", "hero.md")
	require.NoError(t, os.WriteFile(path, []byte("tampered"), 0o600))
	_, _, _, err = store.ReadGraphDefinitionRevision("qc-graph", first.ID())
	require.Error(t, err)
}

func mustConfigDigest(t *testing.T, definition *domain.FlowDefinition) string {
	t.Helper()
	digest, err := agentflow.ConfigDigest(definition)
	require.NoError(t, err)
	return digest
}

func TestReadGraphDefinition(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	dir := filepath.Join(store.GraphsDir(), "qc")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "graph.yaml"), []byte(`schemaVersion: 1
id: qc
name: QC
budget: {max_total_tokens: 100000}
tasks:
  review: {hero: local/reviewer, goal: "Review the workspace"}
`), 0o600))

	definition, digest, err := store.ReadGraphDefinition("qc")
	require.NoError(t, err)
	assert.Equal(t, "QC", definition.Name)
	assert.Equal(t, "sha256:"+mustConfigDigest(t, definition), digest)
}

func TestCreateGraphWritesDefinition(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	definition, _, err := store.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)
	assert.Equal(t, "QC", definition.Name)
	assert.Empty(t, definition.Tasks)
	assert.Equal(t, int64(200000), definition.Budget.MaxTotalTokens)

	// The file on disk round-trips through the same parser.
	read, digest, err := store.ReadGraphDefinition("qc")
	require.NoError(t, err)
	assert.Equal(t, "QC", read.Name)
	_, err = agentflow.ConfigDigest(read)
	require.NoError(t, err)
	assert.NotEmpty(t, digest)
}
