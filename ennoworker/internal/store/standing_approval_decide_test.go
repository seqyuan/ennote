package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func standingCandidateItems() []domain.ApprovalItem {
	return []domain.ApprovalItem{
		{CallIndex: 0, ToolCallID: "call-web", ToolName: "web_fetch", RiskClass: domain.RiskExternal,
			ArgumentsPreview: `{"url":"https://example.com/"}`,
			StandingScope:    &domain.StandingScopeInfo{Kind: "origin", ScopeVersion: 1, Display: "example.com (all paths)"}},
	}
}

func TestDecideCreatesStandingRuleAtomically(t *testing.T) {
	ctx := context.Background()
	_, approvals, submission := setupApprovalRun(t)
	db := approvals.DB
	request, err := approvals.Suspend(ctx, submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":5}`), standingCandidateItems(),
		[]domain.StandingGrantCandidate{{
			CallIndex: 0, ToolCallID: "call-web", ToolName: "web_fetch",
			ScopeKind: "origin", ScopeVersion: 1, ScopeKey: "https://example.com:443",
			ScopeDisplay: "example.com (all paths)", RiskClass: domain.RiskExternal,
		}})
	require.NoError(t, err)

	// Approve with remember for call index 0.
	approval, err := approvals.Decide(ctx, request.ID, domain.DecisionApproved, "req-1", []int{0})
	require.NoError(t, err)
	assert.Equal(t, domain.ApprovalApproved, approval.Status)

	// Rule must exist and be active.
	standing := &store.StandingApprovalRepo{DB: db}
	rules, err := standing.ListActive(ctx, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "web_fetch", rules[0].ToolName)
	assert.Equal(t, "https://example.com:443", rules[0].ScopeKey)

	// Grant mapping must be persisted.
	tx, _ := db.BeginTx(ctx, nil)
	grants, err := standing.GetGrantsTx(ctx, tx, request.ID)
	require.NoError(t, tx.Commit())
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.Equal(t, rules[0].ID, grants[0].RuleID)
}

func TestDecideIdempotencyRequiresSameSelection(t *testing.T) {
	_, approvals, submission := setupApprovalRun(t)
	request, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":5}`), standingCandidateItems(),
		[]domain.StandingGrantCandidate{{
			CallIndex: 0, ToolCallID: "call-web", ToolName: "web_fetch",
			ScopeKind: "origin", ScopeVersion: 1, ScopeKey: "https://example.com:443",
			ScopeDisplay: "example.com (all paths)", RiskClass: domain.RiskExternal,
		}})
	require.NoError(t, err)

	// First approve with selection [0].
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "req-1", []int{0})
	require.NoError(t, err)

	// Same decision + same selection → idempotent success.
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "req-2", []int{0})
	require.NoError(t, err)

	// Same decision + different selection (empty) → conflict.
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "req-3", []int{})
	assert.ErrorIs(t, err, store.ErrApprovalConflict)
}

func TestDecideRejectsInvalidSelections(t *testing.T) {
	_, approvals, submission := setupApprovalRun(t)
	request, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":5}`), standingCandidateItems(),
		[]domain.StandingGrantCandidate{{
			CallIndex: 0, ToolCallID: "call-web", ToolName: "web_fetch",
			ScopeKind: "origin", ScopeVersion: 1, ScopeKey: "https://example.com:443",
			ScopeDisplay: "example.com (all paths)", RiskClass: domain.RiskExternal,
		}})
	require.NoError(t, err)

	// Non-candidate index.
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "req-1", []int{7})
	assert.ErrorIs(t, err, store.ErrStandingGrantInvalid)

	// Duplicate index.
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "req-2", []int{0, 0})
	assert.ErrorIs(t, err, store.ErrStandingGrantInvalid)

	// Rejected with selection.
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionRejected, "req-3", []int{0})
	assert.ErrorIs(t, err, store.ErrStandingGrantInvalid)
}

func TestDecideStandingLimitLeavesApprovalPending(t *testing.T) {
	_, approvals, submission := setupApprovalRun(t)
	db := approvals.DB
	request, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":5}`), standingCandidateItems(),
		[]domain.StandingGrantCandidate{{
			CallIndex: 0, ToolCallID: "call-web", ToolName: "web_fetch",
			ScopeKind: "origin", ScopeVersion: 1, ScopeKey: "https://example.com:443",
			ScopeDisplay: "example.com (all paths)", RiskClass: domain.RiskExternal,
		}})
	require.NoError(t, err)

	// Pre-fill the session with 64 active rules to trip the limit.
	standing := &store.StandingApprovalRepo{DB: db}
	approval := &domain.ToolApprovalRequest{ID: "prefill", RunID: submission.Run.ID, SessionID: submission.Run.SessionID}
	for i := 0; i < 64; i++ {
		tx, _ := db.BeginTx(context.Background(), nil)
		_, _, err := standing.GetOrCreateActiveTx(context.Background(), tx,
			domain.StandingGrantCandidate{
				ToolName: "web_fetch", ScopeKind: "origin", ScopeVersion: 1,
				ScopeKey:     "https://prefill-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + ".com:443",
				ScopeDisplay: "prefill (all paths)", RiskClass: domain.RiskExternal,
			}, approval)
		require.NoError(t, err, "prefill rule %d", i)
		require.NoError(t, tx.Commit())
	}

	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "req-1", []int{0})
	assert.ErrorIs(t, err, store.ErrStandingApprovalLimit)

	// Approval must still be pending after the failed decide.
	pending, err := approvals.FindPendingBySession(context.Background(), submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, domain.ApprovalPending, pending.Status)
}
