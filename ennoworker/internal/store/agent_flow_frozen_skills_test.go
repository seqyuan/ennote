package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinnedFlowFreeze builds a single-task freeze carrying digest-pinned task
// Skills, including one duplicate, plus one legacy-style unpinned ref.
func pinnedFlowFreeze(roleVersionID string) []store.FlowNodeFreeze {
	return []store.FlowNodeFreeze{{
		TaskIndex: 0, Handle: "build", RoleVersionID: roleVersionID,
		Skills: domain.FrozenSkillRefs{
			{ID: "go-dev", Digest: "sha256:go-dev"},
			{ID: "go-dev", Digest: "sha256:duplicate-wins-first"},
			{ID: "review-guard"},
		},
		GoalDigest: "goal-digest", GoalText: "Build it",
	}}
}

func newPinnedFlowRun(t *testing.T) (*store.AgentFlowRunRepo, *domain.RunAgentFlow, func(string) string) {
	t.Helper()
	db, flowRuns, projectID, _, roleVersionID := setupFlowFixture(t)
	def := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: "pinned-flow",
		Budget: domain.FlowBudget{MaxTotalTokens: 20000},
		Tasks: map[string]domain.FlowTask{
			"build": {Type: domain.FlowTaskRole, Role: "flow-worker@1", Goal: "Build it"},
		},
	}
	version := flowVersionFromDef(def)
	session := sqlCreateSession(t, db, projectID)
	run, err := flowRuns.CreateFlowRun(context.Background(), store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID,
		DefinitionJSON: version.DefinitionJSON, ConfigDigest: version.ConfigDigest,
		InputsJSON: []byte(`{"inputs":{},"vars":{}}`),
	}, pinnedFlowFreeze(roleVersionID))
	require.NoError(t, err)
	return flowRuns, run, func(query string) string {
		var raw string
		require.NoError(t, db.QueryRow(query, run.RunID).Scan(&raw))
		return raw
	}
}

// A new Flow Run freezes both the Skill ID and the content digest, and the load
// path reconstructs the digest-pinned view while the public API keeps the ID
// list.
func TestFlowRunFreezesTaskSkillDigests(t *testing.T) {
	flowRuns, run, query := newPinnedFlowRun(t)
	ctx := context.Background()

	raw := query(`SELECT skill_digests_json FROM run_agent_flow_nodes WHERE run_id=? AND task_index=0`)
	refs, err := domain.DecodeFrozenSkills([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, domain.FrozenSkillRefs{
		{ID: "go-dev", Digest: "sha256:go-dev"},
		{ID: "review-guard"},
	}, refs, "duplicates collapse deterministically, keeping the first digest")

	node, err := flowRuns.GetNode(ctx, run.RunID, 0)
	require.NoError(t, err)
	assert.Equal(t, refs, node.FrozenSkills)
	assert.Equal(t, []string{"go-dev", "review-guard"}, node.SkillDigests)

	nodes, err := flowRuns.ListNodes(ctx, run.RunID)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, refs, nodes[0].FrozenSkills)
}

// Legacy rows carry the bare ID array. They must still load, as unpinned refs:
// there is no frozen content identity to verify, so recovery keeps working.
func TestFlowRunLoadsLegacyTaskSkillIDsAsUnpinned(t *testing.T) {
	flowRuns, run, _ := newPinnedFlowRun(t)
	ctx := context.Background()
	_, err := flowRuns.DB.Exec(`UPDATE run_agent_flow_nodes SET skill_digests_json='["go-dev","review-guard"]'
		WHERE run_id=? AND task_index=0`, run.RunID)
	require.NoError(t, err)

	node, err := flowRuns.GetNode(ctx, run.RunID, 0)
	require.NoError(t, err)
	assert.Equal(t, domain.FrozenSkillRefs{{ID: "go-dev"}, {ID: "review-guard"}}, node.FrozenSkills)
	assert.Equal(t, []string{"go-dev", "review-guard"}, node.SkillDigests)
	assert.False(t, node.FrozenSkills[0].Pinned(), "legacy refs are explicitly unpinned")
}

func TestFlowRunFreezeRejectsBlankTaskSkillID(t *testing.T) {
	db, flowRuns, projectID, _, roleVersionID := setupFlowFixture(t)
	def := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: "pinned-flow",
		Budget: domain.FlowBudget{MaxTotalTokens: 20000},
		Tasks: map[string]domain.FlowTask{
			"build": {Type: domain.FlowTaskRole, Role: "flow-worker@1", Goal: "Build it"},
		},
	}
	version := flowVersionFromDef(def)
	session := sqlCreateSession(t, db, projectID)
	freeze := pinnedFlowFreeze(roleVersionID)
	freeze[0].Skills = domain.FrozenSkillRefs{{ID: "  ", Digest: "sha256:blank"}}
	_, err := flowRuns.CreateFlowRun(context.Background(), store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID,
		DefinitionJSON: version.DefinitionJSON, ConfigDigest: version.ConfigDigest,
		InputsJSON: []byte(`{"inputs":{},"vars":{}}`),
	}, freeze)
	require.ErrorContains(t, err, "task skill id is required")
}

// The delegation item is the transport between a frozen Flow node and its child
// Run: the digest must survive the round trip, and legacy ID-only rows must keep
// decoding for existing children and retries.
func TestDelegationPinnedTaskSkillsRoundTrip(t *testing.T) {
	delegations, _, submission := setupRootBudgetParent(t, "task-skills-pinned")
	ctx := context.Background()
	item := taskItem("review")
	item.FrozenSkills = domain.FrozenSkillRefs{
		{ID: "review-guard", Digest: "sha256:guard"},
		{ID: "go-dev", Digest: "sha256:go"},
	}
	// A plain model-authored ID overlaps a pinned one and a new one is added:
	// the pinned digest wins and the merged list stays ordered and deduped.
	item.Skills = []string{"go-dev", "go-dev", "extra"}

	group, created, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-pinned-skills",
		Strategy: domain.DelegationStrategySingle, Items: []store.CreateDelegationItemInput{item},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, created, 1)
	require.Len(t, children, 1)
	assert.Equal(t, []string{"review-guard", "go-dev", "extra"}, created[0].Skills)

	want := domain.FrozenSkillRefs{
		{ID: "review-guard", Digest: "sha256:guard"},
		{ID: "go-dev", Digest: "sha256:go"},
		{ID: "extra"},
	}
	refs, err := delegations.TaskFrozenSkillsForChildRun(ctx, children[0].ID)
	require.NoError(t, err)
	assert.Equal(t, want, refs)

	ids, err := delegations.TaskSkillsForChildRun(ctx, children[0].ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"review-guard", "go-dev", "extra"}, ids)

	var raw string
	require.NoError(t, delegations.DB.QueryRow(
		`SELECT skills_json FROM delegation_items WHERE group_id=?`, group.ID).Scan(&raw))
	stored, err := domain.DecodeFrozenSkills([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, want, stored)

	// Simulate a row written before digest pinning: still loadable, unpinned.
	_, err = delegations.DB.Exec(`UPDATE delegation_items SET skills_json='["go-dev"]' WHERE group_id=?`, group.ID)
	require.NoError(t, err)
	legacy, err := delegations.TaskFrozenSkillsForChildRun(ctx, children[0].ID)
	require.NoError(t, err)
	assert.Equal(t, domain.FrozenSkillRefs{{ID: "go-dev"}}, legacy)
	legacyIDs, err := delegations.TaskSkillsForChildRun(ctx, children[0].ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"go-dev"}, legacyIDs)

	listed, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, []string{"go-dev"}, listed[0].Skills)
}

// The frozen digest must reach the delegation item that the child Run actually
// reads its task Skills from: node freeze -> ChildSpec -> delegation_items.
func TestOrchestratorChildCarriesFrozenSkillDigestToDelegationItem(t *testing.T) {
	db, flowRuns, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(flowYAML("review", "")))
	require.NoError(t, err)
	version := flowVersionFromDef(def)
	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "src/main.go"}, nil)
	require.NoError(t, err)
	freeze, diagnostics, err := freezeFlowForTest(t, db, flowRuns, projectID, "", def, inputs)
	require.NoError(t, err, diagnostics)
	// Pin the producer's task Skill exactly as graphrun.Service now does.
	freeze[0].Skills = domain.FrozenSkillRefs{{ID: "skill-go-dev", Digest: "sha256:producer-skill"}}

	session := sqlCreateSession(t, db, projectID)
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID,
		DefinitionJSON: version.DefinitionJSON, ConfigDigest: version.ConfigDigest, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)

	var budget domain.BudgetCeilingJSON
	require.NoError(t, json.Unmarshal(freeze[0].BudgetJSON, &budget))
	children := &store.OrchestratorChildren{DB: db,
		Policies: &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}}
	info, err := children.CreateTaskChild(ctx, run.RunID, session.ID, agentflow.ChildSpec{
		Handle: "producer", RoleVersionID: freeze[0].RoleVersionID,
		Skills:     freeze[0].Skills,
		Assignment: "Implement src/main.go", Budget: budget,
		RoleDefinitionJSON: freeze[0].RoleDefinitionJSON,
	})
	require.NoError(t, err)

	refs, err := (&store.DelegationRepo{DB: db}).TaskFrozenSkillsForChildRun(ctx, info.RunID)
	require.NoError(t, err)
	assert.Equal(t, domain.FrozenSkillRefs{{ID: "skill-go-dev", Digest: "sha256:producer-skill"}}, refs,
		"the child Run must be able to verify the Skill content frozen by the flow node")
}
