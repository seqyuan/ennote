package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertToolCall writes one tool_calls row directly, so a test can pin exactly
// which columns the execution reader is allowed to see.
func insertToolCall(t *testing.T, db *store.CallRepo, runID, id string, seq int64, tool, arguments, status string,
	isError bool, exitCode any, projectedPreview, projectedPath, rawPreview string) {
	t.Helper()
	_, err := db.DB.Exec(`INSERT INTO tool_calls
		(id, run_id, seq, tool_call_id, tool_name, arguments_json, status, is_error, exit_code,
		 projected_result_preview, projected_result_path, raw_result_preview, raw_result_path,
		 iteration, call_index, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 0, ?, '2026-11-15T00:00:00Z', '2026-11-15T00:00:01Z')`,
		id, runID, seq, id, tool, arguments, status, boolInt(isError), exitCode,
		projectedPreview, projectedPath, rawPreview, seq)
	require.NoError(t, err)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// The reader exposes the projected output only: the raw (pre-redaction) preview
// holds a passing summary that must never satisfy an acceptance clause.
func TestListToolExecutionsReadsProjectedOutputOnly(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "calls-projected")
	ctx := context.Background()
	runID := submission.Run.ID
	calls := &store.CallRepo{DB: repo.DB}
	insertToolCall(t, calls, runID, "call-1", 1, "bash", `{"command":"pytest -q"}`, "completed", false, nil,
		"[redacted by policy]", "", "3 passed in 0.11s")

	executions, err := calls.ListToolExecutions(ctx, runID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	assert.Equal(t, "[redacted by policy]", executions[0].Output)
	assert.False(t, executions[0].Truncated)

	// The summary the raw column holds is not evidence, so the leaf cannot hold.
	verdict := agentflow.EvaluateAcceptance(ctx, []domain.FlowAcceptance{
		{Check: "tests-passed", Command: "pytest -q"},
	}, agentflow.AcceptanceFacts{Executions: executionsSeam{executions: executions}})
	assert.NotEqual(t, domain.AcceptanceHolds, verdict.Result)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
	assert.Equal(t, "evidence_no_pass_summary", verdict.Checks[0].Code)
}

// executionsSeam adapts a recorded slice to the evaluator's seam.
type executionsSeam struct{ executions []domain.ToolExecution }

func (s executionsSeam) Executions() []domain.ToolExecution { return s.executions }

// The projected output file is read bounded, and a file longer than the budget
// is reported as truncated so a missing summary stays undecidable.
func TestListToolExecutionsReadsProjectedFileBounded(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "calls-projected-file")
	ctx := context.Background()
	runID := submission.Run.ID
	calls := &store.CallRepo{DB: repo.DB}
	dir := t.TempDir()
	small := filepath.Join(dir, "small.log")
	require.NoError(t, os.WriteFile(small, []byte("ok  \texample.com/x\t0.002s\n"), 0o600))
	large := filepath.Join(dir, "large.log")
	require.NoError(t, os.WriteFile(large, []byte(strings.Repeat("x", store.MaxToolExecutionOutputBytes+1024)), 0o600))
	missing := filepath.Join(dir, "gone.log")

	insertToolCall(t, calls, runID, "call-small", 1, "bash", `{"command":"go test ./..."}`, "completed", false, nil,
		"ok", small, "raw")
	insertToolCall(t, calls, runID, "call-large", 2, "bash", `{"command":"go test ./..."}`, "completed", false, nil,
		"ok", large, "raw")
	insertToolCall(t, calls, runID, "call-missing", 3, "bash", `{"command":"go test ./..."}`, "completed", false, nil,
		"preview", missing, "raw")

	executions, err := calls.ListToolExecutions(ctx, runID)
	require.NoError(t, err)
	require.Len(t, executions, 3)
	assert.Equal(t, "ok  \texample.com/x\t0.002s\n", executions[0].Output)
	assert.False(t, executions[0].Truncated)
	assert.Len(t, executions[1].Output, store.MaxToolExecutionOutputBytes)
	assert.True(t, executions[1].Truncated)
	// An unreadable projection is truncated, not "no output": the preview stays
	// and the missing summary is undecidable.
	assert.Equal(t, "preview", executions[2].Output)
	assert.True(t, executions[2].Truncated)
}

// Only completed bash/exec calls of this Run are executions: a started call, a
// failed call and a non-shell tool are not evidence, and the order is the
// recorded seq order.
func TestListToolExecutionsSelectsRecordedShellCalls(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "calls-select")
	ctx := context.Background()
	runID := submission.Run.ID
	calls := &store.CallRepo{DB: repo.DB}
	insertToolCall(t, calls, runID, "call-bash", 2, "bash", `{"command":"go test ./..."}`, "completed", false, nil,
		"ok  \texample.com/x\t0.002s\n", "", "")
	insertToolCall(t, calls, runID, "call-exec", 3, "exec", `{"argv":["cargo","test"]}`, "completed", false, 1,
		"test result: FAILED. 0 passed; 1 failed\n", "", "")
	insertToolCall(t, calls, runID, "call-started", 4, "bash", `{"command":"go test ./..."}`, "started", false, nil,
		"", "", "")
	insertToolCall(t, calls, runID, "call-failed", 5, "bash", `{"command":"go test ./..."}`, "failed", true, nil,
		"", "", "")
	insertToolCall(t, calls, runID, "call-read", 6, "read_file", `{"path":"a.go"}`, "completed", false, nil,
		"a.go", "", "")

	executions, err := calls.ListToolExecutions(ctx, runID)
	require.NoError(t, err)
	require.Len(t, executions, 2)
	assert.Equal(t, int64(2), executions[0].Seq)
	assert.Equal(t, "bash", executions[0].Tool)
	assert.Equal(t, "go test ./...", executions[0].Command)
	assert.Empty(t, executions[0].Argv)
	assert.Equal(t, domain.ExitCodeUnrecorded, executions[0].ExitCode)
	assert.Equal(t, int64(3), executions[1].Seq)
	assert.Equal(t, "exec", executions[1].Tool)
	assert.Equal(t, []string{"cargo", "test"}, executions[1].Argv)
	assert.Empty(t, executions[1].Command)
	assert.Equal(t, 1, executions[1].ExitCode)
}

// A Run with no recorded shell execution answers with an empty slice: "read, and
// there were none" is a different fact from "could not read".
func TestListToolExecutionsEmptyRun(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "calls-empty")
	executions, err := (&store.CallRepo{DB: repo.DB}).ListToolExecutions(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Empty(t, executions)
}

// The read is bounded to the newest rows: the leaf decides on the highest
// matching seq, so an old execution is not evidence, and a Run with thousands of
// shell calls must not load all of them into one fold.
func TestListToolExecutionsCapsRowsToTheNewest(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "calls-row-cap")
	ctx := context.Background()
	calls := &store.CallRepo{DB: repo.DB}
	total := store.MaxToolExecutionRows + 5
	for seq := 1; seq <= total; seq++ {
		insertToolCall(t, calls, submission.Run.ID, "call-"+strconv.Itoa(seq), int64(seq), "bash",
			`{"command":"go test -v ./..."}`, "completed", false, nil, "ok", "", "")
	}

	executions, err := calls.ListToolExecutions(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.Len(t, executions, store.MaxToolExecutionRows)
	assert.Equal(t, int64(total-store.MaxToolExecutionRows+1), executions[0].Seq,
		"the window is the newest rows, still oldest first")
	assert.Equal(t, int64(total), executions[len(executions)-1].Seq)
}

// One row's preview is capped too: a 1 MiB preview must not be materialised whole
// per row, and a capped preview is reported truncated so the leaf knows its
// middle was dropped. The bound covers both ends of the preview (see
// TestListToolExecutionsRetainsHeadAndTail), so it is asserted as a bound on the
// read, not as an exact length.
func TestListToolExecutionsCapsPreviewBytes(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "calls-preview-cap")
	ctx := context.Background()
	calls := &store.CallRepo{DB: repo.DB}
	withoutSummary := strings.Repeat("x", store.MaxToolExecutionOutputBytes+1024)
	// A multi-byte preview proves the byte cap does not leave half a rune.
	multiByte := strings.Repeat("\u00e9", store.MaxToolExecutionOutputBytes)
	insertToolCall(t, calls, submission.Run.ID, "call-long", 1, "bash",
		`{"command":"go test -v ./..."}`, "completed", false, nil, withoutSummary, "", "")
	insertToolCall(t, calls, submission.Run.ID, "call-multibyte", 2, "bash",
		`{"command":"go test -v ./..."}`, "completed", false, nil, multiByte, "", "")

	executions, err := calls.ListToolExecutions(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.Len(t, executions, 2)
	const seamSlack = 128 // the two cut markers and their newlines
	assert.LessOrEqual(t, len(executions[0].Output), store.MaxToolExecutionOutputBytes+seamSlack,
		"the per-row read budget is unchanged")
	assert.True(t, strings.HasPrefix(executions[0].Output, "xxxx"), "the head of the preview is retained")
	assert.Contains(t, executions[0].Output, domain.OutputTailTruncationMarker)
	assert.True(t, executions[0].Truncated, "a capped preview is not the whole output")
	assert.True(t, utf8.ValidString(executions[1].Output), "the byte cap must not split a rune")
	assert.True(t, executions[1].Truncated)

	// The cap hides whatever summary followed it, so the clause is undecidable
	// rather than "no pass summary was printed".
	verdict := agentflow.EvaluateAcceptance(ctx, []domain.FlowAcceptance{
		{Check: "tests-passed", Command: "go test -v ./..."},
	}, agentflow.AcceptanceFacts{Executions: executionsSeam{executions: executions[:1]}})
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
	assert.Equal(t, "evidence_truncated", verdict.Checks[0].Code)
}

// The recorder writes the exit status the shell tool captured, so the column is
// no longer dead. A call whose process produced no status stays NULL and reads
// back as the ExitCodeUnrecorded sentinel: unknown is never a fabricated 0.
func TestToolFinishRecordsExitStatus(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "calls-exit-status")
	ctx := context.Background()
	calls := &store.CallRepo{DB: repo.DB}
	runID := submission.Run.ID
	invocation := domain.ToolCall{ID: "provider-bash", Name: "bash",
		Arguments: json.RawMessage(`{"command":"go test ./..."}`)}

	start := func(recordID string, index int) {
		t.Helper()
		require.NoError(t, calls.ToolStarted(ctx, domain.ToolCallStart{ID: recordID, RunID: runID,
			Iteration: 1, CallIndex: index, Call: invocation}))
	}
	finish := func(recordID string, index int, isError bool, status *int) domain.ToolResult {
		t.Helper()
		result := domain.ToolResult{ToolCallID: invocation.ID, ToolName: invocation.Name,
			Content: "recorded output", IsError: isError, ExitStatus: status}
		finish := domain.ToolCallFinish{ID: recordID, RunID: runID, Iteration: 1, CallIndex: index,
			Call: invocation, RawResult: result, Result: result}
		if isError {
			require.NoError(t, calls.ToolFailed(ctx, finish))
		} else {
			require.NoError(t, calls.ToolCompleted(ctx, finish))
		}
		return result
	}
	code := func(value int) *int { return &value }

	start("record-zero", 0)
	finish("record-zero", 0, false, code(0))
	start("record-legacy", 1)
	finish("record-legacy", 1, false, nil)
	start("record-one", 2)
	finish("record-one", 2, true, code(1))
	start("record-signal", 3)
	finish("record-signal", 3, true, code(137))
	start("record-timeout", 4)
	finish("record-timeout", 4, true, code(domain.ExitCodeTimedOut))
	start("record-unknown", 5)
	finish("record-unknown", 5, true, nil)

	executions, err := calls.ListToolExecutions(ctx, runID)
	require.NoError(t, err)
	// Only completed calls are executions; the failed and timed-out rows are read
	// straight from the table below.
	require.Len(t, executions, 2)
	assert.Equal(t, invocation.ID, executions[0].ToolCallID)
	assert.Equal(t, 0, executions[0].ExitCode, "a recorded success is 0, not unknown")
	assert.Equal(t, domain.ExitCodeUnrecorded, executions[1].ExitCode,
		"a call that recorded nothing still reads as unknown, exactly like an old row")

	recorded := map[string]sql.NullInt64{}
	rows, err := repo.DB.Query(`SELECT id, exit_code FROM tool_calls WHERE run_id = ?`, runID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var status sql.NullInt64
		require.NoError(t, rows.Scan(&id, &status))
		recorded[id] = status
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, sql.NullInt64{Int64: 1, Valid: true}, recorded["record-one"])
	assert.Equal(t, sql.NullInt64{Int64: 137, Valid: true}, recorded["record-signal"],
		"a signal death is recorded as 128+signal")
	assert.Equal(t, sql.NullInt64{Int64: domain.ExitCodeTimedOut, Valid: true}, recorded["record-timeout"])
	assert.False(t, recorded["record-unknown"].Valid,
		"an unknown status is NULL, not 0")
}

// The execution reader retains both ends of a long recorded preview inside the
// same read budget. The verdict of a test run is at the tail, so a head-only read
// reported a passing suite as "no pass summary was printed".
func TestListToolExecutionsRetainsHeadAndTail(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "calls-preview-tail")
	ctx := context.Background()
	calls := &store.CallRepo{DB: repo.DB}
	const summary = "\n===== 40 passed, 2 skipped in 91.20s =====\n"
	line := "tests/test_pipeline.py::test_align PASSED\n"
	preview := strings.Repeat(line, store.MaxToolExecutionOutputBytes/len(line)+64) + summary
	require.Greater(t, len(preview), store.MaxToolExecutionOutputBytes)
	insertToolCall(t, calls, submission.Run.ID, "call-tail", 1, "bash",
		`{"command":"python -m pytest -v"}`, "completed", false, 0, preview, "", "")

	executions, err := calls.ListToolExecutions(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	output := executions[0].Output
	assert.True(t, executions[0].Truncated, "part of the preview was dropped")
	assert.True(t, strings.HasPrefix(output, line), "the head is still available")
	assert.True(t, strings.HasSuffix(output, summary), "the tail, where the verdict is, is retained")
	assert.Contains(t, output, domain.OutputHeadTruncationMarker)
	assert.Contains(t, output, domain.OutputTailTruncationMarker)
	assert.LessOrEqual(t, len(output),
		store.MaxToolExecutionOutputBytes+len(domain.OutputHeadTruncationMarker)+len(domain.OutputTailTruncationMarker)+8,
		"the read budget is unchanged: only the seam markers are added")

	// The leaf can now decide the run: the retained tail holds the summary and a
	// zero exit status was recorded.
	verdict := agentflow.EvaluateAcceptance(ctx, []domain.FlowAcceptance{
		{Check: "tests-passed", Command: "python -m pytest -v"},
	}, agentflow.AcceptanceFacts{Executions: executionsSeam{executions: executions}})
	assert.Equal(t, domain.AcceptanceHolds, verdict.Result, verdict.Checks[0].Detail)
	assert.Empty(t, verdict.Checks[0].Code)
}

// A failed call keeps its result: the durable payload carries the content the
// client renders, and the row settles with is_error set on the column the per-tool
// telemetry aggregation reads. (That aggregation currently drops its whole payload
// on a Scan error — see the run_telemetry fix — so this pins the column, not the
// ErrorCount the consumer would compute.)
func TestToolFailedKeepsResultAndCountsAsError(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "calls-failed-result")
	ctx := context.Background()
	calls := &store.CallRepo{DB: repo.DB}
	runID := submission.Run.ID
	invocation := domain.ToolCall{ID: "provider-bash", Name: "bash",
		Arguments: json.RawMessage(`{"command":"go test ./..."}`)}
	require.NoError(t, calls.ToolStarted(ctx, domain.ToolCallStart{ID: "record-panic", RunID: runID,
		Iteration: 1, CallIndex: 0, Call: invocation}))
	result := domain.ToolResult{ToolCallID: invocation.ID, ToolName: invocation.Name,
		Content: "tool panic: boom", IsError: true}
	require.NoError(t, calls.ToolFailed(ctx, domain.ToolCallFinish{ID: "record-panic", RunID: runID,
		Iteration: 1, CallIndex: 0, Call: invocation, RawResult: result, Result: result,
		Status: "failed", Policy: domain.ToolPolicyMetadata{Code: "tool_panicked"}, AttemptCount: 1}))

	var payloadJSON string
	require.NoError(t, repo.DB.QueryRow(`SELECT payload_json FROM run_events
		WHERE run_id = ? AND event_type = 'tool_call_failed'`, runID).Scan(&payloadJSON))
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(payloadJSON), &payload))
	assert.Equal(t, "tool panic: boom", payload["content"])
	assert.Equal(t, true, payload["isError"])
	assert.Equal(t, float64(1), payload["attemptCount"])

	var status, preview string
	var isError int
	require.NoError(t, repo.DB.QueryRow(`SELECT status, is_error, result_preview FROM tool_calls WHERE id = ?`,
		"record-panic").Scan(&status, &isError, &preview))
	assert.Equal(t, "failed", status)
	assert.Equal(t, 1, isError, "a failed call is an error result, not just a non-completed row")
	assert.Equal(t, "tool panic: boom", preview)
}
