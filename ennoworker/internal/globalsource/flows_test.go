package globalsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func flowRoleDocument(handle, prompt string) *rolesource.Document {
	return &rolesource.Document{
		SchemaVersion: 1, Handle: handle, Name: handle,
		Model:  rolesource.ModelBinding{Ref: "provider/model", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: rolesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 2, MaxToolCalls: 4,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 4, Prompt: prompt,
	}
}

func flowDefinition(id string) *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: id,
		Budget: domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"review": {Type: domain.FlowTaskRole, Role: "local/reviewer", Goal: "Review the workspace"},
			"gate":   {Type: domain.FlowTaskCheck, Command: "test -f ready", Depends: []string{"review"}},
			"done": {Type: domain.FlowTaskRole, Depends: []string{"gate"},
				Terminal: &domain.FlowTerminal{Status: "success"}},
		},
	}
}

func TestFlowSourceRevisionsBundleLocalRolesImmutably(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	definition := flowDefinition("qc-flow")
	digest, err := store.CreateFlowFromDefinition(definition)
	require.NoError(t, err)
	require.True(t, len(digest) > len("sha256:"))
	assert.Equal(t, "sha256:"+mustConfigDigest(t, definition), digest)

	_, err = store.CreateGraphRole("qc-flow", flowRoleDocument("reviewer", "first prompt"))
	require.NoError(t, err)
	first, err := store.PublishFlowRevision("qc-flow")
	require.NoError(t, err)
	assert.Equal(t, SourceKindFlow, first.SourceKind)
	assert.Equal(t, digest, first.Digest)
	require.Contains(t, first.RoleDigests, "reviewer")

	revisionFlow, roles, read, err := store.ReadFlowRevision("qc-flow", first.ID())
	require.NoError(t, err)
	assert.Equal(t, first, read)
	assert.Equal(t, "first prompt", roles["reviewer"].Prompt)
	assert.Equal(t, domain.FlowTaskCheck, revisionFlow.Tasks["gate"].Type)

	// Editing the draft role after publish must not change the published bundle.
	encoded, err := rolesource.Encode(flowRoleDocument("reviewer", "second prompt"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store.GraphsDir(), "qc-flow", "roles", "reviewer", "role.md"), encoded, 0o600))
	_, immutableRoles, _, err := store.ReadFlowRevision("qc-flow", first.ID())
	require.NoError(t, err)
	assert.Equal(t, "first prompt", immutableRoles["reviewer"].Prompt)

	// A role-only change still creates a new immutable revision; the definition
	// digest is intentionally unchanged because it commits role references, not
	// role bodies.
	second, err := store.PublishFlowRevision("qc-flow")
	require.NoError(t, err)
	assert.Equal(t, "v000002", second.ID())
	assert.Equal(t, first.Digest, second.Digest)
	assert.NotEqual(t, first.RoleDigests["reviewer"], second.RoleDigests["reviewer"])

	// Tampering with the bundled role fails closed.
	path := filepath.Join(store.GraphsDir(), "qc-flow", "revisions", first.ID(), "roles", "reviewer", "role.md")
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
	require.Len(t, graphs, 1)
	assert.Equal(t, "graph-one", graphs[0].ID)
	assert.Equal(t, SourceKindGraph, graphs[0].SourceKind)

	flows, err := store.ListFlows()
	require.NoError(t, err)
	require.Len(t, flows, 1)
	assert.Equal(t, "flow-one", flows[0].ID)
	assert.Equal(t, SourceKindFlow, flows[0].SourceKind)
	require.NotNil(t, flows[0].Flow)
}

func TestLegacyRevisionWithoutSourceKindReadsAsGraph(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("legacy", "Legacy")
	require.NoError(t, err)
	revision, err := store.PublishGraphRevision("legacy")
	require.NoError(t, err)
	metadataPath := filepath.Join(store.GraphsDir(), "legacy", "revisions", revision.ID(), "revision.json")
	require.NoError(t, os.WriteFile(metadataPath, []byte(`{"schemaVersion":1,"resourceId":"legacy","version":1,"digest":"`+digest+`","publishedAt":"2026-08-10T00:00:00Z"}`), 0o600))

	document, read, err := store.ReadGraphRevision("legacy", revision.ID())
	require.NoError(t, err)
	assert.Equal(t, "Legacy", document.Name)
	assert.Empty(t, read.SourceKind)

	_, _, _, err = store.ReadFlowRevision("legacy", revision.ID())
	require.Error(t, err)
}
