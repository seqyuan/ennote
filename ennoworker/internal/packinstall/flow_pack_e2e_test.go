package packinstall_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/packinstall"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowPackInstallPublishBindFreeze pins the whole enabled path: install is
// a draft, publish is explicit, a Project binding pins that revision, and only
// then does graphrun freeze the full definition (check + fan-out + terminal).
func TestFlowPackInstallPublishBindFreeze(t *testing.T) {
	f := newFixture(t)
	f.service.CheckAllowlistAllowsAll = true
	source := writePack(t, flowPackFiles(f, roleFile(t, f.modelID, "qc-reviewer", "QC Reviewer")))
	_, err := f.service.Install(context.Background(), packinstall.Request{SourcePath: source})
	require.NoError(t, err)

	// Installed draft is not runnable: no revision exists yet.
	revisions, err := f.sources.ListFlowRevisions("qc-flow")
	require.NoError(t, err)
	assert.Empty(t, revisions)

	revision, err := f.sources.PublishFlowRevision("qc-flow")
	require.NoError(t, err)
	project, _, err := f.projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "Flow Project", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	_, err = f.projects.UpdateGraphBindings(context.Background(), project.ID, func(bindings *[]projectstore.GraphBinding) error {
		*bindings = append(*bindings, projectstore.GraphBinding{
			SourceKind: projectstore.SourceKindFlow, GraphID: "qc-flow", Revision: revision.ID(), Enabled: true,
		})
		return nil
	})
	require.NoError(t, err)
	session, err := f.sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Flow"})
	require.NoError(t, err)

	service := &graphrun.Service{
		Sources: f.sources, Models: &store.ModelRepo{Files: f.models},
		Sessions: f.sessions, Projects: f.projects,
	}
	run, err := service.StartFlow(context.Background(), project.ID, "qc-flow", 0, session.ID, nil, nil)
	require.NoError(t, err)
	db, err := f.sessions.OpenSession(context.Background(), session.ID)
	require.NoError(t, err)
	var definitionJSON string
	require.NoError(t, db.QueryRow(`SELECT definition_json FROM run_agent_flow WHERE run_id=?`, run.RunID).Scan(&definitionJSON))
	var definition domain.FlowDefinition
	require.NoError(t, json.Unmarshal([]byte(definitionJSON), &definition))
	assert.Equal(t, domain.FlowTaskCheck, definition.Tasks["gate"].Type)
	require.NotNil(t, definition.Tasks["review"].FanOut)
	require.NotNil(t, definition.Tasks["done"].Terminal)

	nodes, err := (&store.AgentFlowRunRepo{DB: db}).ListNodes(context.Background(), run.RunID)
	require.NoError(t, err)
	require.Len(t, nodes, len(definition.Tasks))
	var reviewNode, gateNode, doneNode *domain.RunAgentFlowNode
	for _, node := range nodes {
		switch node.Handle {
		case "review":
			reviewNode = node
		case "gate":
			gateNode = node
		case "done":
			doneNode = node
		}
	}
	require.NotNil(t, reviewNode)
	require.NotNil(t, gateNode)
	require.NotNil(t, doneNode)
	assert.True(t, reviewNode.ReadOnly)
	assert.Empty(t, gateNode.RoleDefinitionJSON)
	assert.Empty(t, doneNode.RoleDefinitionJSON)
}
