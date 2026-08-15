//go:build integration

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveModelFallbackFreezesCandidateSet qualifies the model fallback path
// against a real Provider: a Role declares a fallback model profile; the
// frozen effective config carries both the primary and fallback in the routing
// candidate set, and the child still completes on the primary.
//
// It requires ENNOTE_LIVE_BASE_URL / ENNOTE_LIVE_API_KEY / ENNOTE_LIVE_MODEL.
func TestLiveModelFallbackFreezesCandidateSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	stack := newLiveStack(t, "fallback-live")
	db := stack.DB
	session := stack.Session

	// ——— second (fallback) model on the same provider ———
	fallback, err := stack.ModelRepo.Create(ctx, store.CreateModelInput{
		ProviderID: "live-provider", ModelName: "deepseek-v4-pro", DisplayName: "deepseek-v4-pro",
		ContextWindow: 1000000, MaxOutputTokens: 8192,
		SupportsToolUse: true, SupportsThinking: true,
		ThinkingDialect:               domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts:      []domain.ThinkingEffort{domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium, domain.ThinkingHigh},
		InputCostUSDMicrosPerMillion:  280,
		OutputCostUSDMicrosPerMillion: 420,
	})
	require.NoError(t, err)

	// ——— Role meta with a fallback binding ———
	definition := fallbackRoleDefinitionJSON(stack.ModelID, fallback.ID)
	meta, err := store.NewDelegationRoleMeta("fallback-reader-v1", []byte(definition))
	require.NoError(t, err)
	meta.ObjectID = "fallback-reader"
	meta.Handle = "fallback-reader"
	meta.DisplayName = "Fallback Reader"

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub, Providers: stack.Providers,
		Models: stack.ModelRepo, Policies: stack.Policies}
	messageRepo := &store.MessageRepo{DB: db}

	// ——— parent + delegated child with the fallback role ———
	parentSubmission, err := runRepo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "fallback-parent",
		Text: "Parent inquiry", RequestedConfig: json.RawMessage(`{"maxIterations":1}`),
	})
	require.NoError(t, err)
	parentRun, err := runRepo.Claim(ctx, parentSubmission.Run.ID)
	require.NoError(t, err)
	_, err = runRepo.ResolveAndFreezeConfig(ctx, parentRun)
	require.NoError(t, err)

	delegations := &store.DelegationRepo{DB: db, RoleSources: stack.Sources, Models: stack.ModelRepo, Policies: stack.Policies}
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: parentRun.ID, ParentToolCallID: "fallback", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{{
			Name: "fallback-explore", RoleVersionID: "fallback-reader-v1",
			AssignmentJSON: json.RawMessage(`{"objective":"Inspect /workspace and report its contents in one sentence."}`),
			OutputContract: "text-v1",
			Budget: domain.BudgetCeilingJSON{MaxModelCalls: 6, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 2048, MaxWallTimeMS: 120000},
			RoleMeta: meta,
		}},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: parentRun.ID, ItemID: items[0].ID, SessionID: session.ID,
	})
	require.NoError(t, err)

	// ——— freeze the child: the candidate set must contain primary + fallback ———
	claimed, err := runRepo.Claim(ctx, child.ID)
	require.NoError(t, err)
	resolved, err := runRepo.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.NotEmpty(t, resolved.Effective.Routing.Candidates)
	candidateIDs := map[string]bool{}
	for _, c := range resolved.Effective.Routing.Candidates {
		candidateIDs[c.ModelProfileID] = true
	}
	assert.True(t, candidateIDs[stack.ModelID], "primary model must be in the candidate set")
	assert.True(t, candidateIDs[fallback.ID], "fallback model must be in the candidate set")
	assert.Equal(t, stack.ModelID, resolved.Effective.ModelProfileID, "primary model stays pinned")

	// ——— execute the child on the real Provider ———
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	callRepo := &store.CallRepo{DB: db, Publisher: hub}
	trustStore, err := workspace.NewTrustStore(t.TempDir())
	require.NoError(t, err)
	emptySkills := t.TempDir()
	executor := &agentExecutor{
		db: db, writer: writer, homeDir: t.TempDir(), runs: runRepo, calls: callRepo,
		sessionDB: &store.SessionRepo{DB: db}, msgRepo: messageRepo,
		projects:  &store.ProjectRepo{Files: stack.Projects},
		skillRepo: &store.SkillSnapshotRepo{DB: db}, skillsDir: emptySkills,
		builtinDir: emptySkills, sandbox: "none",
		hub:               hub,
		approvals:         &store.ApprovalRepo{DB: db},
		standingApprovals: &store.StandingApprovalRepo{DB: db},
		trustStore:        trustStore,
	}
	output, execErr := executor.executeDelegatedChild(ctx, claimed)
	require.NoError(t, execErr)
	require.NotNil(t, output.Terminal)
	assert.Equal(t, domain.SubmitCompleted, output.Terminal.Status)

	// ——— real Provider usage recorded ———
	var usageCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&usageCount))
	assert.Positive(t, usageCount)
}

// fallbackRoleDefinitionJSON builds a read-only Role definition bound to
// primaryModelID with fallbackModelID as a fallback candidate.
func fallbackRoleDefinitionJSON(primaryModelID, fallbackModelID string) string {
	return `{"schemaVersion":1,"rolePrompt":"You are a read-only workspace inspector. Use read, ls, grep, and find to answer the task. Be concise. End by calling submit_result with a structured result.","modelBinding":{"mode":"fixed","modelProfileId":` +
		jsonQuote(primaryModelID) + `,"thinkingEffort":"default","fallbackModelProfileIds":[` +
		jsonQuote(fallbackModelID) + `],"overridableFields":[]},"skills":{"entries":[]},"authority":"read_only","permissionCeiling":"discuss","allowedTools":["read","ls","grep","find"],"contextPolicy":{"defaultMode":"task_only","allowedModes":["task_only"],"ownExecutionContinuity":"none"},"delegationPolicy":{"admission":"auto_within_budget","allowedCallerKinds":["host"],"allowedStrategies":["single","parallel"],"maxInvocationsPerParentRun":16,"maxConcurrentInstances":16,"budgetCeiling":{"maxModelCalls":6,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":0,"maxWallTimeMs":120000}},"outputContract":"text-v1","maxLoopIterations":8}`
}

func jsonQuote(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}
