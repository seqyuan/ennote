package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const importFlowYAML = `schemaVersion: 1
id: imported-flow
inputs:
  target: {type: path, required: true}
outputs:
  report: {type: string}
budget:
  max_total_tokens: 60000
tasks:
  producer:
    role: flow-worker@1
    skills: [go-dev]
    goal: "Implement {inputs.target}"
    budget: {tokens: 30000}
  accept:
    terminal: {status: success, output: report}
    output: report
    depends: [producer]
`

// Matrix 3A-2: dependency pre-check never persists anything.
func TestAgentFlowCheckDependenciesNoPersist(t *testing.T) {
	server, handler, _ := setupFlowServer(t)
	roleRef := publishFixtureRole(t, server) // publishes flow-worker@1 global
	require.NotEmpty(t, roleRef)
	rec := request(t, handler, http.MethodPost, "/v1/agent-flows/check-dependencies",
		map[string]any{"yaml": importFlowYAML}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var result struct {
		Valid        bool                             `json:"valid"`
		Diagnostics  []agentflow.ValidationDiagnostic `json:"diagnostics"`
		Dependencies []agentflow.DependencyStatus     `json:"dependencies"`
	}
	decodeData(t, rec, &result)
	assert.True(t, result.Valid, "%v", result.Diagnostics)
	require.Len(t, result.Dependencies, 2)
	byName := map[string]agentflow.DependencyStatus{}
	for _, dep := range result.Dependencies {
		byName[dep.Name] = dep
	}
	// flow-worker@1 is published and go-dev is in the catalog -> present.
	assert.True(t, byName["flow-worker"].Present)
	assert.True(t, byName["go-dev"].Present)

	// A missing role version makes the flow invalid (same §5.3 resolver as
	// publish) AND the dependency report flags it; nothing is installed.
	var rolesBefore int
	require.NoError(t, server.DB.QueryRow(`SELECT COUNT(*) FROM agent_profiles WHERE object_kind='role'`).Scan(&rolesBefore))
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/check-dependencies",
		map[string]any{"yaml": strings.ReplaceAll(importFlowYAML, "flow-worker@1", "flow-worker@99")}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	decodeData(t, rec, &result)
	assert.False(t, result.Valid) // role_not_found fails the 9-point validation
	foundMissing := false
	for _, dep := range result.Dependencies {
		if dep.Name == "flow-worker" && dep.Version == 99 && !dep.Present {
			foundMissing = true
		}
	}
	assert.True(t, foundMissing, "missing role version must be reported absent")
	var rolesAfter int
	require.NoError(t, server.DB.QueryRow(`SELECT COUNT(*) FROM agent_profiles WHERE object_kind='role'`).Scan(&rolesAfter))
	assert.Equal(t, rolesBefore, rolesAfter) // nothing auto-installed
	var profilesCount int
	require.NoError(t, server.DB.QueryRow(`SELECT COUNT(*) FROM agent_flow_profiles`).Scan(&profilesCount))
	assert.Zero(t, profilesCount) // pre-check never drafts
}

// Matrix 3A-3: import creates a managed DRAFT only — never publishes, binds,
// or authorizes.
func TestAgentFlowImportCreatesDraftOnly(t *testing.T) {
	server, handler, _ := setupFlowServer(t)
	roleRef := publishFixtureRole(t, server)
	require.NotEmpty(t, roleRef)
	rec := request(t, handler, http.MethodPost, "/v1/agent-flows/import",
		map[string]any{"yaml": importFlowYAML}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var result struct {
		ProfileID      string `json:"profileId"`
		Slug           string `json:"slug"`
		DraftRevision  int    `json:"draftRevision"`
		AlreadyDrafted bool   `json:"alreadyDrafted"`
	}
	decodeData(t, rec, &result)
	assert.Equal(t, "imported-flow", result.Slug)
	assert.Equal(t, 1, result.DraftRevision)
	assert.False(t, result.AlreadyDrafted)

	// No immutable version was published.
	versions, err := server.AgentFlows.Profiles.ListVersions(context.Background(), result.ProfileID)
	require.NoError(t, err)
	assert.Empty(t, versions)

	// No project binding exists.
	project, _, err := (&store.ProjectRepo{DB: server.DB}).CreateWithWorkspace(context.Background(),
		domain.CreateProjectInput{Name: "Import", HostPath: t.TempDir()})
	require.NoError(t, err)
	bindings, err := server.AgentFlows.Bindings.ListByProject(context.Background(), project.ID)
	require.NoError(t, err)
	assert.Empty(t, bindings)

	// Draft is readable and identical.
	draft, err := server.AgentFlows.Profiles.GetDraft(context.Background(), result.ProfileID)
	require.NoError(t, err)
	assert.Equal(t, importFlowYAML, draft.YAML)
}

// Matrix 3A-4/5: import is idempotent on identical digest; a changed flow
// bumps the draft revision via CAS.
func TestAgentFlowImportIdempotentAndOverride(t *testing.T) {
	server, handler, _ := setupFlowServer(t)
	roleRef := publishFixtureRole(t, server)
	require.NotEmpty(t, roleRef)
	rec := request(t, handler, http.MethodPost, "/v1/agent-flows/import",
		map[string]any{"yaml": importFlowYAML}, true)
	require.Equal(t, http.StatusCreated, rec.Code)
	var result struct {
		ProfileID      string `json:"profileId"`
		DraftRevision  int    `json:"draftRevision"`
		AlreadyDrafted bool   `json:"alreadyDrafted"`
	}
	decodeData(t, rec, &result)

	// Identical import: revision unchanged, alreadyDrafted.
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/import",
		map[string]any{"yaml": importFlowYAML}, true)
	require.Equal(t, http.StatusCreated, rec.Code)
	decodeData(t, rec, &result)
	assert.True(t, result.AlreadyDrafted)
	assert.Equal(t, 1, result.DraftRevision)

	// Changed import: revision bumps.
	changed := importFlowYAML + "  fixer:\n    role: flow-worker@1\n    depends: [producer]\n"
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/import",
		map[string]any{"yaml": changed}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	decodeData(t, rec, &result)
	assert.False(t, result.AlreadyDrafted)
	assert.Equal(t, 2, result.DraftRevision)
}

// Matrix 3A-6: invalid YAML fails loudly before anything is drafted.
func TestAgentFlowImportValidationRejects(t *testing.T) {
	server, handler, _ := setupFlowServer(t)
	// Two entries -> invalid.
	rec := request(t, handler, http.MethodPost, "/v1/agent-flows/import",
		map[string]any{"yaml": `schemaVersion: 1
id: bad-flow
budget:
  max_total_tokens: 10000
tasks:
  a: {role: flow-worker@1, goal: "a"}
  b: {role: flow-worker@1, goal: "b"}
`}, true)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "agent_flow_validation_failed")
	var profiles int
	require.NoError(t, server.DB.QueryRow(`SELECT COUNT(*) FROM agent_flow_profiles`).Scan(&profiles))
	assert.Zero(t, profiles)
}

// Matrix 3A-8/9: export returns importable YAML; draft and version round-trip
// to the same config digest.
func TestAgentFlowExportRoundTrip(t *testing.T) {
	server, handler, _ := setupFlowServer(t)
	roleRef := publishFixtureRole(t, server)
	require.NotEmpty(t, roleRef)
	rec := request(t, handler, http.MethodPost, "/v1/agent-flows/import",
		map[string]any{"yaml": importFlowYAML}, true)
	require.Equal(t, http.StatusCreated, rec.Code)
	var result struct {
		ProfileID string `json:"profileId"`
	}
	decodeData(t, rec, &result)

	// Draft export returns the authoring YAML verbatim.
	rec = request(t, handler, http.MethodGet, "/v1/agent-flows/"+result.ProfileID+"/export?source=draft", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, importFlowYAML, rec.Body.String())
	digest, err := agentflow.ConfigDigest(agentflowParse(t, importFlowYAML))
	require.NoError(t, err)
	assert.Equal(t, digest, agentflowParseDigest(t, importFlowYAML))

	// Publish then export the version; re-importing it keeps the digest.
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/"+result.ProfileID+"/publish",
		map[string]any{"expectedRevision": 1}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version domain.AgentFlowVersion
	decodeData(t, rec, &version)
	rec = request(t, handler, http.MethodGet, "/v1/agent-flows/"+result.ProfileID+"/export?source=version&versionID="+version.ID, nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	reimported := agentflowParse(t, rec.Body.String())
	reimportedDigest, err := agentflow.ConfigDigest(reimported)
	require.NoError(t, err)
	assert.Equal(t, version.ConfigDigest, reimportedDigest, "exported YAML re-imports to the same config digest")
}

func agentflowParse(t *testing.T, yamlText string) *domain.FlowDefinition {
	t.Helper()
	def, err := agentflow.ParseDefinition([]byte(yamlText))
	require.NoError(t, err)
	return def
}

func agentflowParseDigest(t *testing.T, yamlText string) string {
	t.Helper()
	digest, err := agentflow.ConfigDigest(agentflowParse(t, yamlText))
	require.NoError(t, err)
	return digest
}
