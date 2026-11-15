package graphrun_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Graph Run freezes each task's acceptance clause onto its node, next to the
// budget. The fold path therefore never has to re-read the version, where a
// failed read would silently skip verification.
func TestServiceFreezesTaskAcceptanceOntoTheNode(t *testing.T) {
	fixture := newGraphRunFixture(t)
	definition := fixture.graph("acceptance-flow", false)
	review := definition.Tasks["review"]
	review.Acceptance = []domain.FlowAcceptance{
		{File: "outputs/qc.csv", Check: "csv-columns", Columns: []string{"sample", "reads"}},
		{File: "outputs/qc.json", Check: "json-valid"},
	}
	definition.Tasks["review"] = review

	_, err := fixture.sources.CreateGraphFromDefinition(definition)
	require.NoError(t, err)
	_, err = fixture.sources.CreateGraphHero("acceptance-flow",
		fixture.localHero(t, "reviewer", heroesource.AuthorityReadOnly, []string{"read"}, "prompt"))
	require.NoError(t, err)
	revision, err := fixture.sources.PublishGraphRevision("acceptance-flow")
	require.NoError(t, err)
	fixture.bind(t, "acceptance-flow", revision.ID(), true)

	service := &graphrun.Service{Sources: fixture.sources, Models: fixture.models,
		Sessions: fixture.sessions, Projects: fixture.projects}
	run, err := service.Start(context.Background(), fixture.projectID, "acceptance-flow", 0, fixture.sessionID, nil, nil)
	require.NoError(t, err)

	db, err := fixture.sessions.OpenSession(context.Background(), fixture.sessionID)
	require.NoError(t, err)
	nodes, err := (&store.AgentFlowRunRepo{DB: db}).ListNodes(context.Background(), run.RunID)
	require.NoError(t, err)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, node := range nodes {
		byHandle[node.Handle] = node
	}
	assert.Equal(t, review.Acceptance, byHandle["review"].Acceptance)
	assert.Empty(t, byHandle["review"].AcceptanceVerdict, "a frozen node starts with no verdict")
	assert.Empty(t, byHandle["scan"].Acceptance, "a task without a clause freezes the empty list")

	// The column of a task with no clause is the empty JSON array, never null:
	// the fold path decodes it without a "no specs" special case.
	var raw string
	require.NoError(t, db.QueryRow(
		`SELECT acceptance_json FROM run_agent_flow_nodes WHERE run_id=? AND handle='scan'`, run.RunID).Scan(&raw))
	assert.JSONEq(t, `[]`, raw)
}

// The Hero's acceptance restriction is part of the frozen HeroDefinition on the
// node, not a re-read of a mutable file: the publish decision (which leaves this
// Hero accepts) and the frozen definition travel together in the revision.
func TestServiceFreezesHeroAcceptanceKindsOntoTheNode(t *testing.T) {
	fixture := newGraphRunFixture(t)
	definition := fixture.graph("acceptance-kinds-flow", false)
	review := definition.Tasks["review"]
	review.Acceptance = []domain.FlowAcceptance{{File: "outputs/qc.json", Check: "json-valid"}}
	definition.Tasks["review"] = review

	_, err := fixture.sources.CreateGraphFromDefinition(definition)
	require.NoError(t, err)
	hero := fixture.localHero(t, "reviewer", heroesource.AuthorityReadOnly, []string{"read"}, "prompt")
	hero.AcceptanceKinds = []string{"json-valid"}
	_, err = fixture.sources.CreateGraphHero("acceptance-kinds-flow", hero)
	require.NoError(t, err)
	revision, err := fixture.sources.PublishGraphRevision("acceptance-kinds-flow")
	require.NoError(t, err)
	fixture.bind(t, "acceptance-kinds-flow", revision.ID(), true)

	service := &graphrun.Service{Sources: fixture.sources, Models: fixture.models,
		Sessions: fixture.sessions, Projects: fixture.projects}
	run, err := service.Start(context.Background(), fixture.projectID, "acceptance-kinds-flow", 0, fixture.sessionID, nil, nil)
	require.NoError(t, err)

	db, err := fixture.sessions.OpenSession(context.Background(), fixture.sessionID)
	require.NoError(t, err)
	nodes, err := (&store.AgentFlowRunRepo{DB: db}).ListNodes(context.Background(), run.RunID)
	require.NoError(t, err)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, node := range nodes {
		byHandle[node.Handle] = node
	}

	var frozenHero domain.HeroDefinition
	require.NoError(t, json.Unmarshal(byHandle["review"].HeroDefinitionJSON, &frozenHero))
	assert.Equal(t, []string{"json-valid"}, frozenHero.AcceptanceKinds)
	assert.Equal(t, review.Acceptance, byHandle["review"].Acceptance)

	// The same Hero, same node: the task-level clause is frozen next to it, so a
	// fold never has to re-resolve which leaves are allowed.
	assert.Equal(t, "json-valid", byHandle["review"].Acceptance[0].Check)
}
