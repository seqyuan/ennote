package globalsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func flowHeroDocument(handle, prompt string) *heroesource.Document {
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

func flowDefinition(id string) *domain.FlowDefinition {
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

func TestFlowSourceRevisionsBundleLocalHeroesImmutably(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	definition := flowDefinition("qc-flow")
	digest, err := store.CreateFlowFromDefinition(definition)
	require.NoError(t, err)
	require.True(t, len(digest) > len("sha256:"))
	assert.Equal(t, "sha256:"+mustConfigDigest(t, definition), digest)

	_, err = store.CreateGraphHero("qc-flow", flowHeroDocument("reviewer", "first prompt"))
	require.NoError(t, err)
	first, err := store.PublishFlowRevision("qc-flow")
	require.NoError(t, err)
	assert.Equal(t, SourceKindFlow, first.SourceKind)
	assert.Equal(t, digest, first.Digest)
	require.Contains(t, first.HeroDigests, "reviewer")

	revisionFlow, heroes, read, err := store.ReadFlowRevision("qc-flow", first.ID())
	require.NoError(t, err)
	assert.Equal(t, first, read)
	assert.Equal(t, "first prompt", heroes["reviewer"].Prompt)
	assert.Equal(t, domain.FlowTaskCheck, revisionFlow.Tasks["gate"].Type)

	// Editing the draft hero after publish must not change the published bundle.
	encoded, err := heroesource.Encode(flowHeroDocument("reviewer", "second prompt"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store.GraphsDir(), "qc-flow", "heroes", "reviewer", "hero.md"), encoded, 0o600))
	_, immutableHeroes, _, err := store.ReadFlowRevision("qc-flow", first.ID())
	require.NoError(t, err)
	assert.Equal(t, "first prompt", immutableHeroes["reviewer"].Prompt)

	// A hero-only change still creates a new immutable revision; the definition
	// digest is intentionally unchanged because it commits hero references, not
	// hero bodies.
	second, err := store.PublishFlowRevision("qc-flow")
	require.NoError(t, err)
	assert.Equal(t, "v000002", second.ID())
	assert.Equal(t, first.Digest, second.Digest)
	assert.NotEqual(t, first.HeroDigests["reviewer"], second.HeroDigests["reviewer"])

	// Tampering with the bundled hero fails closed.
	path := filepath.Join(store.GraphsDir(), "qc-flow", "revisions", first.ID(), "heroes", "reviewer", "hero.md")
	require.NoError(t, os.WriteFile(path, []byte("tampered"), 0o600))
	_, _, _, err = store.ReadFlowRevision("qc-flow", first.ID())
	require.Error(t, err)
}

func mustConfigDigest(t *testing.T, definition *domain.FlowDefinition) string {
	t.Helper()
	digest, err := agentflow.ConfigDigest(definition)
	require.NoError(t, err)
	return digest
}

func TestSourceKindRejectsBothDocuments(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, err := store.CreateFlowFromDefinition(flowDefinition("dual"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store.GraphsDir(), "dual", "graph.yaml"), []byte("schema_version: 1\n"), 0o600))

	_, err = store.SourceKind("dual")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains both")
}

func TestGraphAndFlowCatalogsAreSeparated(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, _, err := store.CreateGraph("graph-one", "Graph One")
	require.NoError(t, err)
	_, err = store.CreateFlowFromDefinition(flowDefinition("flow-one"))
	require.NoError(t, err)

	graphs, err := store.ListGraphs()
	require.NoError(t, err)
	require.Len(t, graphs, 2)
	byID := map[string]GraphEntry{}
	for _, entry := range graphs {
		byID[entry.ID] = entry
	}
	assert.Equal(t, SourceKindGraph, byID["graph-one"].SourceKind)
	assert.Equal(t, LegacyFileGraphV1, byID["graph-one"].LegacyFile)
	assert.Equal(t, SourceKindGraph, byID["flow-one"].SourceKind)
	assert.Equal(t, LegacyFileFlow, byID["flow-one"].LegacyFile)
	require.NotNil(t, byID["flow-one"].Flow)

	flows, err := store.ListFlows()
	require.NoError(t, err)
	require.Len(t, flows, 1)
	assert.Equal(t, "flow-one", flows[0].ID)
	assert.Equal(t, SourceKindFlow, flows[0].SourceKind)
	require.NotNil(t, flows[0].Flow)
}

func TestLegacyRevisionWithoutSourceKindReadsAsGraph(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	document := &graphsource.Document{SchemaVersion: 1, ID: "legacy", Name: "Legacy",
		Tasks: map[string]graphsource.Task{}, Graph: map[string][]string{}}
	digest, err := store.CreateGraphFromDocument(document)
	require.NoError(t, err)
	_, err = store.PublishGraphRevision("legacy")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graph_legacy_source")

	// The revision exists because it was frozen before this change. A revision
	// without sourceKind still reads as a graph document.
	contents, err := graphsource.Encode(document)
	require.NoError(t, err)
	_, err = publishRevision(filepath.Join(store.GraphsDir(), "legacy"), "legacy", "",
		[]revisionFile{{Path: graphSourceName, Data: contents}}, digest, nil, nil)
	require.NoError(t, err)
	metadataPath := filepath.Join(store.GraphsDir(), "legacy", "revisions", "v000001", "revision.json")
	require.NoError(t, os.WriteFile(metadataPath, []byte(`{"schemaVersion":1,"resourceId":"legacy","version":1,"digest":"`+digest+`","publishedAt":"2026-08-10T00:00:00Z"}`), 0o600))

	read, revisionMeta, err := store.ReadGraphRevision("legacy", "v000001")
	require.NoError(t, err)
	assert.Equal(t, "Legacy", read.Name)
	assert.Empty(t, revisionMeta.SourceKind)

	_, _, _, err = store.ReadFlowRevision("legacy", "v000001")
	require.Error(t, err)
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

	_, _, err = store.ReadGraph("qc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graph_legacy_source")

	legacy := Store{HomeDir: t.TempDir()}
	_, _, err = legacy.CreateGraph("old", "Old")
	require.NoError(t, err)
	document, _, err := legacy.ReadGraph("old")
	require.NoError(t, err)
	assert.Equal(t, "Old", document.Name)
	_, _, err = legacy.ReadGraphDefinition("old")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graph_legacy_source")
}

// One catalog. A current graph.yaml and a legacy flow.yaml are both Graphs.
// The legacy file is labeled so the UI can tell them apart; it is not a second
// object type. A directory that holds both files is still an error.
func TestListGraphsIncludesLegacyFlow(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, _, err := store.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)

	legacy := filepath.Join(store.GraphsDir(), "review")
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, flowSourceName), []byte(`schemaVersion: 1
id: review
budget: {max_total_tokens: 1000}
tasks:
  read: {hero: local/reviewer, goal: "Read"}
`), 0o600))

	both := filepath.Join(store.GraphsDir(), "both")
	require.NoError(t, os.MkdirAll(both, 0o755))
	for _, name := range []string{graphSourceName, flowSourceName} {
		require.NoError(t, os.WriteFile(filepath.Join(both, name), []byte("schemaVersion: 1\n"), 0o600))
	}

	entries, err := store.ListGraphs()
	require.NoError(t, err)
	byID := map[string]GraphEntry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	require.Contains(t, byID, "qc")
	assert.Empty(t, byID["qc"].LegacyFile)
	assert.NotNil(t, byID["qc"].Flow)

	require.Contains(t, byID, "review")
	assert.Equal(t, LegacyFileFlow, byID["review"].LegacyFile)
	assert.Equal(t, SourceKindGraph, byID["review"].SourceKind)
	assert.NotNil(t, byID["review"].Flow)

	require.Contains(t, byID, "both")
	assert.Contains(t, byID["both"].Error, "both")
}

func TestCreateGraphWritesDefinition(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	definition, _, err := store.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)
	assert.Equal(t, "QC", definition.Name)
	assert.Empty(t, definition.Tasks)
	assert.Equal(t, int64(200000), definition.Budget.MaxTotalTokens)
	data, err := os.ReadFile(filepath.Join(store.GraphsDir(), "qc", "graph.yaml"))
	require.NoError(t, err)
	kind, err := ClassifyGraphSource(data)
	require.NoError(t, err)
	assert.Equal(t, GraphFileDefinition, kind)
}
