package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// classedFakeController records the class each enqueue named, so the tests can
// ask which half of the permit split an approval resume asked for.
type classedFakeController struct {
	fakeController
	mu      sync.Mutex
	classes map[string]runs.RunClass
}

func (c *classedFakeController) EnqueueWithClass(ctx context.Context, runID string, class runs.RunClass) error {
	c.mu.Lock()
	if c.classes == nil {
		c.classes = make(map[string]runs.RunClass)
	}
	c.classes[runID] = class
	c.mu.Unlock()
	return c.fakeController.Enqueue(ctx, runID)
}

func (c *classedFakeController) classOf(runID string) (runs.RunClass, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	class, ok := c.classes[runID]
	return class, ok
}

// A delegated child suspends for approval like any other Run, so the decision
// that resumes it must not hand it the slot reserved for the person. A wave of
// child approvals would otherwise occupy every permit.
func TestApprovalResumeOfAChildTakesTheBatchClass(t *testing.T) {
	control := &classedFakeController{}
	server, handler := setupServer(t, control)
	ctx := context.Background()
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "child approval class", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	_ = bindAPISession(t, server, ctx, session.ID)
	parent, err := server.Runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "class-parent", Text: "delegate",
	})
	require.NoError(t, err)
	_, err = server.Runs.Claim(ctx, parent.Run.ID)
	require.NoError(t, err)
	delegations := &store.DelegationRepo{DB: server.DB, Policies: apiPolicyStore(t)}
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: parent.Run.ID, ParentToolCallID: "class-delegate",
		Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{{
			Name: "inspect", HeroVersionID: "builtin-workspace-explorer-v3",
			AssignmentJSON: json.RawMessage(`{"task":"inspect"}`), OutputContract: "text-v1",
			Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxWallTimeMS: 120000},
			HeroMeta: apiHeroMetaFromDB(t, server.DB, "builtin-workspace-explorer-v3"),
		}},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: parent.Run.ID, ItemID: items[0].ID, SessionID: session.ID,
	})
	require.NoError(t, err)
	_, err = server.Runs.Claim(ctx, child.ID)
	require.NoError(t, err)
	approval, err := server.Approvals.Suspend(ctx, child.ID, 1, 1, "child-digest",
		json.RawMessage(`{"version":1}`), []domain.ApprovalItem{{
			CallIndex: 0, ToolCallID: "write-call", ToolName: "write", RiskClass: domain.RiskLocalWrite,
			ArgumentsPreview: `{"path":"notes.txt"}`,
		}}, nil)
	require.NoError(t, err)

	decision := request(t, handler, http.MethodPost, "/v1/approval-requests/"+approval.ID+"/decision",
		map[string]any{"decision": "approved", "clientRequestId": "class-decision"}, true)
	require.Equal(t, http.StatusOK, decision.Code, decision.Body.String())
	class, ok := control.classOf(child.ID)
	require.True(t, ok, "the resumed child Run must be scheduled")
	assert.Equal(t, runs.ClassBatch, class, "a resumed delegated child must not take the person's slot")
}

// The other half of the rule: when the approval resumed the person's own turn,
// it keeps the reserved class.
func TestApprovalResumeOfAPersonTurnTakesTheInteractiveClass(t *testing.T) {
	control := &classedFakeController{}
	server, handler := setupServer(t, control)
	ctx := context.Background()
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "person approval class", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	_ = bindAPISession(t, server, ctx, session.ID)
	submission, err := server.Runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "class-person", Text: "write a file",
	})
	require.NoError(t, err)
	_, err = server.Runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	approval, err := server.Approvals.Suspend(ctx, submission.Run.ID, 1, 1, "person-digest",
		json.RawMessage(`{"version":1}`), []domain.ApprovalItem{{
			CallIndex: 0, ToolCallID: "write-call", ToolName: "write", RiskClass: domain.RiskLocalWrite,
			ArgumentsPreview: `{"path":"notes.txt"}`,
		}}, nil)
	require.NoError(t, err)

	decision := request(t, handler, http.MethodPost, "/v1/approval-requests/"+approval.ID+"/decision",
		map[string]any{"decision": "approved", "clientRequestId": "person-decision"}, true)
	require.Equal(t, http.StatusOK, decision.Code, decision.Body.String())
	class, ok := control.classOf(submission.Run.ID)
	require.True(t, ok, "the resumed person turn must be scheduled")
	assert.Equal(t, runs.ClassInteractive, class)
}
