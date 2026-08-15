package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSession(t *testing.T, db *sql.DB) string {
	t.Helper()
	id := "s-" + t.Name()
	pid := "p-" + t.Name()
	// V2: sessions have no FK to the removed projects table.
	_, err := db.Exec(`INSERT INTO sessions (id, project_id, title, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
		id, pid, t.Name(), time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	require.NoError(t, err)
	return id
}

// testApprovalFK creates the minimal agent_run + run_execution_checkpoint
// needed to satisfy FK constraints on tool_approval_requests.
func testApprovalFK(t *testing.T, db *sql.DB, approvalID, sessionID string) {
	t.Helper()
	runID := "run-" + approvalID
	cpID := "cp-" + approvalID
	msgID := "msg-" + approvalID
	now := time.Now().UTC().Format(time.RFC3339)
	// Create a message first (FK requirement for turns).
	_, err := db.Exec(
		`INSERT INTO messages (id, session_id, role, status, created_at) VALUES (?, ?, 'user', 'complete', ?)`,
		msgID, sessionID, now)
	require.NoError(t, err)
	// Create a turn (FK requirement for agent_runs).
	_, err = db.Exec(
		`INSERT INTO turns (id, session_id, client_request_id, user_message_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, 'pending', ?, ?)`,
		"turn-"+approvalID, sessionID, "req-"+approvalID, msgID, now, now)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO agent_runs (id, turn_id, session_id, status, requested_config_json, effective_config_json, created_at)
		 VALUES (?, ?, ?, 'running', '{}', '{}', ?)`,
		runID, "turn-"+approvalID, sessionID, now)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO run_execution_checkpoints (id, run_id, schema_version, iteration, batch_digest, state_json, status, created_at)
		 VALUES (?, ?, 4, 1, '', '{}', 'pending', ?)`, cpID, runID, now)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO tool_approval_requests (id, run_id, session_id, checkpoint_id, iteration, batch_digest, status, items_json, requested_at)
		 VALUES (?, ?, ?, ?, 1, '', 'pending', '[]', ?)`,
		approvalID, runID, sessionID, cpID, now)
	require.NoError(t, err)
}

func TestStandingApprovalRepo_CreateAndList(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)

	// Create via GetOrCreateActiveTx.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	candidate := domain.StandingGrantCandidate{
		CallIndex:    0,
		ToolCallID:   "call-1",
		ToolName:     "web_fetch",
		ScopeKind:    "origin",
		ScopeVersion: 1,
		ScopeKey:     "https://example.com:443",
		ScopeDisplay: "example.com (all paths)",
		RiskClass:    domain.RiskExternal,
	}
	approval := &domain.ToolApprovalRequest{
		ID:        "approval-1",
		RunID:     "run-1",
		SessionID: sid,
	}

	rule, created, err := repo.GetOrCreateActiveTx(ctx, tx, candidate, approval)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEmpty(t, rule.ID)
	assert.Equal(t, sid, rule.SessionID)
	assert.Equal(t, "web_fetch", rule.ToolName)
	assert.Equal(t, "origin", rule.ScopeKind)
	assert.Equal(t, 1, rule.ScopeVersion)
	assert.Equal(t, "https://example.com:443", rule.ScopeKey)
	require.NoError(t, tx.Commit())

	// List should see it.
	rules, err := repo.ListActive(ctx, sid)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, rule.ID, rules[0].ID)
}

func TestStandingApprovalRepo_GetOrCreateIdempotent(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)

	candidate := domain.StandingGrantCandidate{
		ToolName:     "web_fetch",
		ScopeKind:    "origin",
		ScopeVersion: 1,
		ScopeKey:     "https://example.com:443",
		ScopeDisplay: "example.com (all paths)",
		RiskClass:    domain.RiskExternal,
	}
	approval := &domain.ToolApprovalRequest{ID: "a1", RunID: "r1", SessionID: sid}

	// First create.
	tx, _ := db.BeginTx(ctx, nil)
	r1, created1, _ := repo.GetOrCreateActiveTx(ctx, tx, candidate, approval)
	require.True(t, created1)
	require.NoError(t, tx.Commit())

	// Second call should return existing.
	tx2, _ := db.BeginTx(ctx, nil)
	r2, created2, _ := repo.GetOrCreateActiveTx(ctx, tx2, candidate, approval)
	assert.False(t, created2)
	assert.Equal(t, r1.ID, r2.ID)
	require.NoError(t, tx2.Commit())

	// Only one active rule.
	rules, _ := repo.ListActive(ctx, sid)
	assert.Len(t, rules, 1)
}

func TestStandingApprovalRepo_Revoke(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)

	candidate := domain.StandingGrantCandidate{
		ToolName:     "web_fetch",
		ScopeKind:    "origin",
		ScopeVersion: 1,
		ScopeKey:     "https://example.com:443",
		ScopeDisplay: "example.com (all paths)",
		RiskClass:    domain.RiskExternal,
	}
	approval := &domain.ToolApprovalRequest{ID: "a1", RunID: "r1", SessionID: sid}

	tx, _ := db.BeginTx(ctx, nil)
	rule, _, _ := repo.GetOrCreateActiveTx(ctx, tx, candidate, approval)
	require.NoError(t, tx.Commit())

	// Revoke.
	err := repo.Revoke(ctx, sid, rule.ID, "req-1")
	require.NoError(t, err)

	// Should not appear in list.
	rules, _ := repo.ListActive(ctx, sid)
	assert.Len(t, rules, 0)

	// Revoke again (idempotent).
	err = repo.Revoke(ctx, sid, rule.ID, "req-2")
	require.NoError(t, err)
}

func TestStandingApprovalRepo_RevokeCrossSession(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()

	// Create two distinct sessions with unique IDs.
	pid1 := "p-rcs-1"
	sid1 := "s-rcs-1"
	_, err := db.Exec(`INSERT INTO sessions (id, project_id, title, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
		sid1, pid1, "rcs1", time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	require.NoError(t, err)

	pid2 := "p-rcs-2"
	sid2 := "s-rcs-2"
	_, err = db.Exec(`INSERT INTO sessions (id, project_id, title, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
		sid2, pid2, "rcs2", time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	require.NoError(t, err)

	candidate := domain.StandingGrantCandidate{
		ToolName: "web_fetch", ScopeKind: "origin", ScopeVersion: 1,
		ScopeKey: "https://example.com:443", ScopeDisplay: "example.com (all paths)",
		RiskClass: domain.RiskExternal,
	}
	approval := &domain.ToolApprovalRequest{ID: "a1", RunID: "r1", SessionID: sid1}

	tx, _ := db.BeginTx(ctx, nil)
	rule, _, _ := repo.GetOrCreateActiveTx(ctx, tx, candidate, approval)
	require.NoError(t, tx.Commit())

	// Revoke from wrong session → 404.
	err = repo.Revoke(ctx, sid2, rule.ID, "req-1")
	assert.ErrorIs(t, err, ErrStandingApprovalNotFound)
}

func TestStandingApprovalRepo_RevokeThenRegrant(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)

	candidate := domain.StandingGrantCandidate{
		ToolName: "web_fetch", ScopeKind: "origin", ScopeVersion: 1,
		ScopeKey: "https://example.com:443", ScopeDisplay: "example.com (all paths)",
		RiskClass: domain.RiskExternal,
	}
	approval := &domain.ToolApprovalRequest{ID: "a1", RunID: "r1", SessionID: sid}

	// Create first rule.
	tx1, _ := db.BeginTx(ctx, nil)
	r1, _, _ := repo.GetOrCreateActiveTx(ctx, tx1, candidate, approval)
	require.NoError(t, tx1.Commit())

	// Revoke.
	require.NoError(t, repo.Revoke(ctx, sid, r1.ID, "r"))

	// Create again (new rule, new ID).
	tx2, _ := db.BeginTx(ctx, nil)
	r2, created, _ := repo.GetOrCreateActiveTx(ctx, tx2, candidate, approval)
	assert.True(t, created)
	assert.NotEqual(t, r1.ID, r2.ID)
	require.NoError(t, tx2.Commit())
}

func TestStandingApprovalRepo_MatchActive(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)

	c := domain.StandingGrantCandidate{
		ToolName: "web_fetch", ScopeKind: "origin", ScopeVersion: 1,
		ScopeKey: "https://example.com:443", ScopeDisplay: "example.com (all paths)",
		RiskClass: domain.RiskExternal,
	}
	a := &domain.ToolApprovalRequest{ID: "a1", RunID: "r1", SessionID: sid}
	tx, _ := db.BeginTx(ctx, nil)
	rule, _, _ := repo.GetOrCreateActiveTx(ctx, tx, c, a)
	require.NoError(t, tx.Commit())

	// Exact match.
	scopes := []domain.StandingScopeRef{
		{ToolName: "web_fetch", Kind: "origin", ScopeVersion: 1, Key: "https://example.com:443"},
	}
	matched, err := repo.MatchActive(ctx, sid, scopes)
	require.NoError(t, err)
	assert.Len(t, matched, 1)
	assert.Equal(t, rule.ID, matched[scopes[0]].ID)

	// Version mismatch.
	scopes2 := []domain.StandingScopeRef{
		{ToolName: "web_fetch", Kind: "origin", ScopeVersion: 2, Key: "https://example.com:443"},
	}
	matched2, err := repo.MatchActive(ctx, sid, scopes2)
	require.NoError(t, err)
	assert.Len(t, matched2, 0)

	// Key mismatch (different host).
	scopes3 := []domain.StandingScopeRef{
		{ToolName: "web_fetch", Kind: "origin", ScopeVersion: 1, Key: "https://api.example.com:443"},
	}
	matched3, err := repo.MatchActive(ctx, sid, scopes3)
	require.NoError(t, err)
	assert.Len(t, matched3, 0)
}

func TestStandingApprovalRepo_CandidatesTx(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)
	approvalID := "approval-cand-1"
	testApprovalFK(t, db, approvalID, sid)

	candidates := []domain.StandingGrantCandidate{
		{CallIndex: 0, ToolCallID: "c0", ToolName: "web_fetch", ScopeKind: "origin",
			ScopeVersion: 1, ScopeKey: "https://a.com:443", ScopeDisplay: "a.com (all paths)", RiskClass: domain.RiskExternal},
		{CallIndex: 1, ToolCallID: "c1", ToolName: "web_fetch", ScopeKind: "origin",
			ScopeVersion: 1, ScopeKey: "https://b.com:443", ScopeDisplay: "b.com (all paths)", RiskClass: domain.RiskExternal},
	}

	tx, _ := db.BeginTx(ctx, nil)
	require.NoError(t, repo.SaveCandidatesTx(ctx, tx, approvalID, candidates))
	require.NoError(t, tx.Commit())

	tx2, _ := db.BeginTx(ctx, nil)
	got, err := repo.GetCandidatesTx(ctx, tx2, approvalID)
	require.NoError(t, tx2.Commit())
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, 0, got[0].CallIndex)
	assert.Equal(t, "https://a.com:443", got[0].ScopeKey)
	assert.Equal(t, 1, got[1].CallIndex)
	assert.Equal(t, "https://b.com:443", got[1].ScopeKey)
}

func TestStandingApprovalRepo_GrantsTx(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)
	approvalID := "approval-grant-1"
	testApprovalFK(t, db, approvalID, sid)

	// Create rules first (FK requirement for grants).
	ruleA := "rule-a"
	ruleB := "rule-b"
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO standing_approvals (id, session_id, tool_name, scope_kind, scope_version, scope_key, scope_display, risk_class, created_at, created_by_run_id, created_by_approval_id)
		VALUES (?, ?, 'web_fetch', 'origin', 1, 'https://a.com:443', 'a.com (all paths)', 'external', ?, '', '')`,
		ruleA, sid, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO standing_approvals (id, session_id, tool_name, scope_kind, scope_version, scope_key, scope_display, risk_class, created_at, created_by_run_id, created_by_approval_id)
		VALUES (?, ?, 'web_fetch', 'origin', 1, 'https://b.com:443', 'b.com (all paths)', 'external', ?, '', '')`,
		ruleB, sid, now)
	require.NoError(t, err)

	// Create candidates first (FK requirement for grants).
	candidates := []domain.StandingGrantCandidate{
		{CallIndex: 0, ToolCallID: "c0", ToolName: "web_fetch", ScopeKind: "origin",
			ScopeVersion: 1, ScopeKey: "https://a.com:443", ScopeDisplay: "a.com (all paths)", RiskClass: domain.RiskExternal},
		{CallIndex: 1, ToolCallID: "c1", ToolName: "web_fetch", ScopeKind: "origin",
			ScopeVersion: 1, ScopeKey: "https://b.com:443", ScopeDisplay: "b.com (all paths)", RiskClass: domain.RiskExternal},
	}
	tx, _ := db.BeginTx(ctx, nil)
	require.NoError(t, repo.SaveCandidatesTx(ctx, tx, approvalID, candidates))
	require.NoError(t, tx.Commit())

	tx, _ = db.BeginTx(ctx, nil)
	grants := []domain.StandingGrantResult{
		{CallIndex: 0, RuleID: "rule-a"},
		{CallIndex: 1, RuleID: "rule-b"},
	}
	require.NoError(t, repo.SaveGrantsTx(ctx, tx, approvalID, grants))
	require.NoError(t, tx.Commit())

	tx2, _ := db.BeginTx(ctx, nil)
	got, err := repo.GetGrantsTx(ctx, tx2, approvalID)
	require.NoError(t, tx2.Commit())
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, 0, got[0].CallIndex)
	assert.Equal(t, "rule-a", got[0].RuleID)
	assert.Equal(t, 1, got[1].CallIndex)
	assert.Equal(t, "rule-b", got[1].RuleID)
}

func TestStandingApprovalRepo_ActiveCount(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)

	count, err := repo.ActiveCount(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	c := domain.StandingGrantCandidate{
		ToolName: "web_fetch", ScopeKind: "origin", ScopeVersion: 1,
		ScopeKey: "https://example.com:443", ScopeDisplay: "example.com (all paths)",
		RiskClass: domain.RiskExternal,
	}
	a := &domain.ToolApprovalRequest{ID: "a1", RunID: "r1", SessionID: sid}
	tx, _ := db.BeginTx(ctx, nil)
	_, _, _ = repo.GetOrCreateActiveTx(ctx, tx, c, a)
	require.NoError(t, tx.Commit())

	count, err = repo.ActiveCount(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestStandingApprovalRepo_LimitTrigger(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)

	a := &domain.ToolApprovalRequest{ID: "a-limit", RunID: "r-limit", SessionID: sid}

	// Create 64 unique rules.
	for i := 0; i < 64; i++ {
		key := fmt.Sprintf("https://host-%d.com:443", i)
		c := domain.StandingGrantCandidate{
			ToolName:     "web_fetch",
			ScopeKind:    "origin",
			ScopeVersion: 1,
			ScopeKey:     key,
			ScopeDisplay: fmt.Sprintf("host-%d.com (all paths)", i),
			RiskClass:    domain.RiskExternal,
		}
		tx, _ := db.BeginTx(ctx, nil)
		_, _, err := repo.GetOrCreateActiveTx(ctx, tx, c, a)
		require.NoError(t, err, "rule %d", i)
		require.NoError(t, tx.Commit())
	}

	// 65th should trigger the limit.
	c65 := domain.StandingGrantCandidate{
		ToolName:     "web_fetch",
		ScopeKind:    "origin",
		ScopeVersion: 1,
		ScopeKey:     "https://host-65.com:443",
		ScopeDisplay: "host-65.com (all paths)",
		RiskClass:    domain.RiskExternal,
	}
	tx, _ := db.BeginTx(ctx, nil)
	_, _, err := repo.GetOrCreateActiveTx(ctx, tx, c65, a)
	assert.ErrorIs(t, err, ErrStandingApprovalLimit)
	require.NoError(t, tx.Rollback())

	// Duplicate scope for an already-active rule should NOT trigger limit.
	tx2, _ := db.BeginTx(ctx, nil)
	cDup := domain.StandingGrantCandidate{
		ToolName: "web_fetch", ScopeKind: "origin", ScopeVersion: 1,
		ScopeKey: "https://host-0.com:443", ScopeDisplay: "host-0.com (all paths)",
		RiskClass: domain.RiskExternal,
	}
	_, created, err := repo.GetOrCreateActiveTx(ctx, tx2, cDup, a)
	require.NoError(t, err)
	assert.False(t, created, "duplicate scope should not create new rule")
	require.NoError(t, tx2.Commit())

	// Revoke one, then create should succeed.
	rules, _ := repo.ListActive(ctx, sid)
	require.Len(t, rules, 64)
	require.NoError(t, repo.Revoke(ctx, sid, rules[0].ID, "r"))
	tx3, _ := db.BeginTx(ctx, nil)
	_, created2, err := repo.GetOrCreateActiveTx(ctx, tx3, c65, a)
	require.NoError(t, err)
	assert.True(t, created2)
	require.NoError(t, tx3.Commit())
}

func itoa(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestStandingApprovalRepo_SessionCascade(t *testing.T) {
	db := SetupDB(t)
	repo := &StandingApprovalRepo{DB: db}
	ctx := context.Background()
	sid := testSession(t, db)

	c := domain.StandingGrantCandidate{
		ToolName: "web_fetch", ScopeKind: "origin", ScopeVersion: 1,
		ScopeKey: "https://example.com:443", ScopeDisplay: "example.com (all paths)",
		RiskClass: domain.RiskExternal,
	}
	a := &domain.ToolApprovalRequest{ID: "a1", RunID: "r1", SessionID: sid}
	tx, _ := db.BeginTx(ctx, nil)
	_, _, err := repo.GetOrCreateActiveTx(ctx, tx, c, a)
	require.NoError(t, tx.Commit())

	// Archive should not delete rules.
	_, err = db.ExecContext(ctx, `UPDATE sessions SET status = 'archived' WHERE id = ?`, sid)
	require.NoError(t, err)
	rules, _ := repo.ListActive(ctx, sid)
	assert.Len(t, rules, 1)

	// Hard delete session should cascade-delete rules.
	_, err = db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sid)
	require.NoError(t, err)
	rules, _ = repo.ListActive(ctx, sid)
	// Rules are gone, but the session itself was deleted so the query returns nothing.
	assert.Len(t, rules, 0)
}

// TestStandingApprovalRepo_JSONRoundtrip verifies that scope display survives
// a JSON encode/decode cycle (used in API responses).
func TestStandingApprovalRepo_JSONRoundtrip(t *testing.T) {
	sa := domain.StandingApproval{
		ID:           "rule-1",
		SessionID:    "s-1",
		ToolName:     "web_fetch",
		ScopeKind:    "origin",
		ScopeVersion: 1,
		ScopeKey:     "https://example.com:443",
		ScopeDisplay: "example.com (all paths)",
		RiskClass:    domain.RiskExternal,
		CreatedAt:    time.Now().UTC(),
	}

	data, err := json.Marshal(sa)
	require.NoError(t, err)

	var restored domain.StandingApproval
	require.NoError(t, json.Unmarshal(data, &restored))
	assert.Equal(t, sa.ID, restored.ID)
	assert.Equal(t, sa.ScopeKind, restored.ScopeKind)
	assert.Equal(t, sa.ScopeVersion, restored.ScopeVersion)
	assert.Equal(t, sa.ScopeDisplay, restored.ScopeDisplay)
}
