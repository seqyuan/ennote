package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// V1 and V2 digests must be byte-stable golden values, and empty RuleID must
// participate in the V2 hash so "no rule" and "rule X" produce different digests.
func TestApprovalDigestVersions(t *testing.T) {
	base := []plannedToolCall{{
		original:  domain.ToolCall{ID: "c1", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://a.com/"}`)},
		effective: domain.ToolCall{ID: "c1", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://a.com/"}`)},
		decision:  ToolDecision{Action: ToolRequireApproval, RiskClass: domain.RiskExternal}, requiresApproval: true,
	}}
	snapshot := domain.PolicySnapshot{ID: "ask", Version: 1}

	// Golden stability.
	v1a := approvalBatchDigest(base, snapshot, ApprovalDigestV1)
	v1b := approvalBatchDigest(base, snapshot, ApprovalDigestV1)
	assert.Equal(t, v1a, v1b, "V1 digest must be deterministic")
	v2a := approvalBatchDigest(base, snapshot, ApprovalDigestV2)
	v2b := approvalBatchDigest(base, snapshot, ApprovalDigestV2)
	assert.Equal(t, v2a, v2b, "V2 digest must be deterministic")

	// V1 and V2 must differ for the same plan.
	assert.NotEqual(t, v1a, v2a, "V1 and V2 must produce different digests for identical plans")

	// Same plan with empty RuleID vs populated RuleID must produce different V2 digests.
	withRule := []plannedToolCall{{
		original:         base[0].original,
		effective:        base[0].effective,
		decision:         ToolDecision{Action: ToolRequireApproval, RiskClass: domain.RiskExternal, RuleID: "rule-1"},
		requiresApproval: true,
	}}
	v2Empty := approvalBatchDigest(base, snapshot, ApprovalDigestV2)
	v2Rule := approvalBatchDigest(withRule, snapshot, ApprovalDigestV2)
	assert.NotEqual(t, v2Empty, v2Rule, "empty vs populated RuleID must change the V2 digest")

	// V1 ignores RuleID entirely.
	v1Rule := approvalBatchDigest(withRule, snapshot, ApprovalDigestV1)
	assert.Equal(t, v1a, v1Rule, "V1 must ignore RuleID for backward compatibility")
}

func TestApprovalDigestV2SurvivesCheckpointJSONRoundTrip(t *testing.T) {
	before := domain.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage("{\n  \"path\": \"notes.txt\",\n  \"content\": \"updated\"\n}")}
	encoded, err := json.Marshal(before)
	require.NoError(t, err)
	var after domain.ToolCall
	require.NoError(t, json.Unmarshal(encoded, &after))
	require.NotEqual(t, string(before.Arguments), string(after.Arguments),
		"the fixture must reproduce checkpoint JSON whitespace normalization")
	policy := domain.PolicySnapshot{ID: "ask", Version: 1}
	plan := func(call domain.ToolCall) []plannedToolCall {
		return []plannedToolCall{{original: call, effective: call,
			decision:         ToolDecision{Action: ToolRequireApproval, RiskClass: domain.RiskLocalWrite},
			requiresApproval: true}}
	}
	assert.NotEqual(t, approvalBatchDigest(plan(before), policy, ApprovalDigestV1),
		approvalBatchDigest(plan(after), policy, ApprovalDigestV1), "V1 raw-byte compatibility remains unchanged")
	assert.Equal(t, approvalBatchDigest(plan(before), policy, ApprovalDigestV2),
		approvalBatchDigest(plan(after), policy, ApprovalDigestV2))
}

// standingAuthorizationSnapshot must capture ALL calls released by standing
// rules, including those not in the require_approval set.
func TestStandingAuthorizationSnapshotCapturesReleasedCalls(t *testing.T) {
	plans := []plannedToolCall{
		{ // released by standing rule — must be captured
			originalIndex: 0,
			original:      domain.ToolCall{ID: "a", Name: "web_fetch"},
			effective:     domain.ToolCall{ID: "a", Name: "web_fetch"},
			decision: ToolDecision{Action: ToolAllow, RiskClass: domain.RiskExternal, RuleID: "r1",
				StandingScopeKind: "origin", StandingScopeVersion: 1, StandingScopeKey: "https://a.com:443"},
			allowed: true,
		},
		{ // still requiring approval — must NOT be captured
			originalIndex:    1,
			original:         domain.ToolCall{ID: "b", Name: "web_fetch"},
			effective:        domain.ToolCall{ID: "b", Name: "web_fetch"},
			decision:         ToolDecision{Action: ToolRequireApproval, RiskClass: domain.RiskExternal},
			requiresApproval: true,
		},
		{ // read-only allowed — must NOT be captured
			originalIndex: 2,
			original:      domain.ToolCall{ID: "c", Name: "read"},
			effective:     domain.ToolCall{ID: "c", Name: "read"},
			decision:      ToolDecision{Action: ToolAllow, RiskClass: domain.RiskReadOnly},
			allowed:       true,
		},
	}

	snapshots := standingAuthorizationSnapshot(plans)
	require.Len(t, snapshots, 1, "only the standing-released call should be captured")
	assert.Equal(t, 0, snapshots[0].CallIndex)
	assert.Equal(t, "a", snapshots[0].ToolCallID)
	assert.Equal(t, "r1", snapshots[0].RuleID)
	assert.Equal(t, "https://a.com:443", snapshots[0].ScopeKey)
}

// attachStandingScopes must decorate only external-risk eligible items and
// never leak the canonical scope key.
func TestAttachStandingScopes(t *testing.T) {
	resolver := &fakeScopeResolver{}
	// originalIndex is part of a plan's identity: the callIndex that leaves the
	// dispatcher is the position in the model's batch, not the position in this
	// hook-filtered slice.
	plans := []plannedToolCall{
		{
			originalIndex: 0,
			original:      domain.ToolCall{ID: "x", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.com/"}`)},
			effective:     domain.ToolCall{ID: "x", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.com/"}`)},
			decision:      ToolDecision{Action: ToolRequireApproval, RiskClass: domain.RiskExternal}, requiresApproval: true,
		},
		{
			originalIndex: 1,
			original:      domain.ToolCall{ID: "y", Name: "write", Arguments: json.RawMessage(`{"path":"a"}`)},
			effective:     domain.ToolCall{ID: "y", Name: "write", Arguments: json.RawMessage(`{"path":"a"}`)},
			decision:      ToolDecision{Action: ToolRequireApproval, RiskClass: domain.RiskLocalWrite}, requiresApproval: true,
		},
	}
	items := approvalItems(plans, nil)
	attachStandingScopes(items, plans, resolver)

	require.Len(t, items, 2)
	assert.NotNil(t, items[0].StandingScope, "external eligible item should get standing scope")
	assert.Equal(t, "origin", items[0].StandingScope.Kind)
	assert.Equal(t, 1, items[0].StandingScope.ScopeVersion)
	assert.Equal(t, "example.com (all paths)", items[0].StandingScope.Display)
	assert.Nil(t, items[1].StandingScope, "local_write item must not get standing scope")
}

type fakeScopeResolver struct{}

func (f *fakeScopeResolver) ResolveStandingApprovalScope(toolName string, arguments json.RawMessage) (domain.StandingApprovalScope, bool, error) {
	if toolName != "web_fetch" {
		return domain.StandingApprovalScope{}, false, nil
	}
	return domain.StandingApprovalScope{
		Kind: "origin", ScopeVersion: 1,
		Key:     "https://example.com:443",
		Display: "example.com (all paths)",
	}, true, nil
}

type fakeStandingMatcher struct{}

func (fakeStandingMatcher) MatchActive(context.Context, string, []domain.StandingScopeRef) (map[domain.StandingScopeRef]domain.StandingApproval, error) {
	return nil, nil
}

// A frozen standing grant names a call by its position in the model's batch. On
// resume a PreToolUse hook may block that batch's earlier calls again, so the
// surviving plan is not at that position in the compacted plans slice: replay
// must find the plan by originalIndex, or the resumed Run re-asks for an approval
// the user already granted.
func TestStandingSnapshotReplayFindsTheCallByOriginalIndex(t *testing.T) {
	loop := &Loop{
		ToolPolicy:            permissionPolicy(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionAsk)}),
		StandingApprovals:     fakeStandingMatcher{},
		StandingScopeResolver: &fakeScopeResolver{},
		StandingAuthorizationSnapshot: []domain.StandingAuthorizationSnapshot{{
			CallIndex: 1, ToolCallID: "call-web", ToolName: "web_fetch", ScopeKind: "origin",
			ScopeVersion: 1, ScopeKey: "https://example.com:443", RuleID: "rule-1",
		}},
	}
	// The model's batch was [read, web_fetch] and the hook blocked the read, so the
	// single surviving plan sits at plans[0] while belonging to originalIndex 1.
	calls := []domain.ToolCall{{ID: "call-web", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.com/"}`)}}
	plans, terminate, err := loop.planToolBatch(context.Background(), "run-resume", 1, calls, []int{1})

	require.NoError(t, err)
	assert.False(t, terminate)
	require.Len(t, plans, 1)
	assert.True(t, plans[0].allowed, "the frozen grant must be replayed onto the call it was granted for")
	assert.Equal(t, "rule-1", plans[0].decision.RuleID)
	assert.Equal(t, "call-web", plans[0].original.ID)
}
