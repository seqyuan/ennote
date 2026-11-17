package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A batch that both blocks a call through PreToolUse and asks for approval is
// suspended, and resuming it re-runs PreToolUse over the same iteration. The
// skip is therefore recorded twice for one (run, iteration, callIndex) - a key
// the tool_calls unique index already holds - and recording it twice used to fail
// the resume before it reached the approval it was waiting for.
func TestToolSkippedToleratesAReplayedBatch(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "tool-skip-replay")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	calls := &store.CallRepo{DB: repo.DB}
	skip := blockedCallFinish(submission.Run.ID, 1, "record-skip-1")
	require.NoError(t, calls.ToolSkipped(ctx, skip))

	replayed := skip
	replayed.ID = "record-skip-2"
	require.NoError(t, calls.ToolSkipped(ctx, replayed), "a replayed skip must not fail the resume")

	assert.Equal(t, 1, countToolCalls(t, repo.DB, submission.Run.ID), "one call, one row")
	assert.Equal(t, 1, countRunEvents(t, repo.DB, submission.Run.ID, "tool_policy_denied"),
		"the replay must not append a second event for the same decision")
}

// The suppression is keyed on the call, not on the record: a second blocked call
// in the same iteration still gets its own row and its own event.
func TestToolSkippedStillRecordsADistinctCall(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "tool-skip-distinct")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	calls := &store.CallRepo{DB: repo.DB}
	for _, callIndex := range []int{1, 2} {
		require.NoError(t, calls.ToolSkipped(ctx,
			blockedCallFinish(submission.Run.ID, callIndex, fmt.Sprintf("record-skip-%d", callIndex))))
	}

	assert.Equal(t, 2, countToolCalls(t, repo.DB, submission.Run.ID), "each blocked call keeps its own row")
	assert.Equal(t, 2, countRunEvents(t, repo.DB, submission.Run.ID, "tool_policy_denied"),
		"each blocked call keeps its own event")
}

func blockedCallFinish(runID string, callIndex int, recordID string) domain.ToolCallFinish {
	return domain.ToolCallFinish{
		ID: recordID, RunID: runID, Iteration: 1, CallIndex: callIndex,
		Call:   domain.ToolCall{ID: "tool-bash", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /"}`)},
		Result: domain.ToolResult{ToolCallID: "tool-bash", ToolName: "bash", Content: "hook_blocked: refused", IsError: true},
		Reason: "hook_blocked",
		Policy: domain.ToolPolicyMetadata{Action: "deny", Code: "hook_blocked"},
	}
}

func countToolCalls(t *testing.T, db *sql.DB, runID string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE run_id = ?`, runID).Scan(&count))
	return count
}

func countRunEvents(t *testing.T, db *sql.DB, runID, eventType string) int {
	t.Helper()
	events, err := (&store.EventRepo{DB: db}).After(context.Background(), runID, 0, 200)
	require.NoError(t, err)
	count := 0
	for _, event := range events {
		if event.EventType == eventType {
			count++
		}
	}
	return count
}
