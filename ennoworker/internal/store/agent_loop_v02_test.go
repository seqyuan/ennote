package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAndFreezeConfigUsesRequestedModelAndExecutionSettings(t *testing.T) {
	ctx := context.Background()
	stack := newFileConfigStack(t)
	// A second, non-default model exists; the request pins it explicitly.
	second, err := stack.Models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "requested", DisplayName: "Requested",
		ContextWindow: 64000, MaxOutputTokens: 4096, SupportsToolUse: true, IsDefault: false,
	})
	require.NoError(t, err)
	db := store.SetupDB(t)
	project, _, err := newFileProjects(t).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "config", HostPath: t.TempDir()})
	require.NoError(t, err)
	session := sqlCreateSessionWithModel(t, db, project.ID, &stack.DefaultRef)
	repo := &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo, Policies: stack.Policies}
	submission, err := repo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "config-request", Text: "run",
		RequestedConfig: json.RawMessage(`{"modelProfileId":"provider/requested","toolPolicyProfileId":"builtin-tool-auto-v1","maxIterations":7,"toolExecution":"safe_parallel","maxConcurrentReadTools":3}`),
	})
	require.NoError(t, err)
	run, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := repo.ResolveAndFreezeConfig(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, "provider", resolved.Effective.ProviderProfileID)
	assert.Equal(t, second.ID, resolved.Effective.ModelProfileID)
	assert.Equal(t, "requested", resolved.Effective.APIModel)
	assert.Equal(t, 64000, resolved.Effective.ContextTokens)
	assert.Equal(t, 4096, resolved.Effective.MaxOutputTokens)
	assert.Equal(t, 7, resolved.Effective.MaxIterations)
	assert.Equal(t, "safe_parallel", resolved.Effective.ToolExecution.Mode)
	assert.Equal(t, 3, resolved.Effective.ToolExecution.MaxConcurrentReadTools)
	assert.Equal(t, "builtin-tool-auto-v1", resolved.Effective.ToolPolicy.ID)
	var permission domain.ToolPolicyConfig
	require.NoError(t, json.Unmarshal(resolved.Effective.ToolPolicy.Config, &permission))
	assert.Equal(t, string(domain.PermissionAuto), permission.Mode)

	stored, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	var effective domain.EffectiveRunConfig
	require.NoError(t, json.Unmarshal(stored.EffectiveConfig, &effective))
	assert.Equal(t, "requested", effective.APIModel)
	assert.NotEqual(t, `{}`, string(stored.EffectiveConfig))
}

func TestResolveConfigFailsWithStableCodeWhenNoModelExists(t *testing.T) {
	// File-native stack with an empty model catalog: no active model exists.
	ctx := context.Background()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	db := store.SetupDB(t)
	project, _, err := newFileProjects(t).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "missing-model", HostPath: t.TempDir()})
	require.NoError(t, err)
	session := sqlCreateSession(t, db, project.ID)
	repo := &store.RunRepo{DB: db, Providers: &store.ProviderRepo{Files: models},
		Models:   &store.ModelRepo{Files: models},
		Policies: &fileconfig.PolicyStore{Path: filepath.Join(home, "config", "policies.json")}}
	submission, err := repo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "missing-model", Text: "run",
	})
	require.NoError(t, err)
	run, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = repo.ResolveAndFreezeConfig(ctx, run)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderUnavailable, domain.ErrorCodeOf(err))
	stored, getErr := repo.Get(ctx, run.ID)
	require.NoError(t, getErr)
	assert.JSONEq(t, `{}`, string(stored.EffectiveConfig))
}

func TestFinalizeSuccessCommitsReplayableMessageChainAndEvents(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "finalize")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	call := domain.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"sample.txt"}`)}
	artifact := domain.ArtifactReference{ArtifactID: "artifact-1", Name: "results.csv", Kind: domain.ArtifactKindTable,
		MIMEType: "text/csv", SizeBytes: 42, SHA256: "0123456789abcdef"}
	var projectID string
	require.NoError(t, repo.DB.QueryRow(`SELECT project_id FROM sessions WHERE id=?`, submission.Run.SessionID).Scan(&projectID))
	_, err = repo.DB.Exec(`INSERT INTO artifacts
		(id,project_id,session_id,run_id,name,kind,mime_type,storage_path,size_bytes,sha256,metadata_json,
		 created_at,source_tool_call_id,source_kind,retention_class)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,? ,?,'workspace_publish','project')`, artifact.ArtifactID, projectID,
		submission.Run.SessionID, submission.Run.ID, artifact.Name, artifact.Kind, artifact.MIMEType, "blobs/test",
		artifact.SizeBytes, artifact.SHA256, `{}`, time.Now().UTC().Format(time.RFC3339Nano), call.ID)
	require.NoError(t, err)
	result := domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "sample contents",
		Artifacts: []domain.ArtifactReference{artifact}}
	output := domain.RunOutput{Messages: []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "checking"}, {Kind: domain.ContentToolCall, ToolCall: &call}}},
		{Role: domain.RoleTool, Content: []domain.ContentBlock{{Kind: domain.ContentToolResult, ToolResult: &result}}},
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}},
	}}
	require.NoError(t, repo.FinalizeSuccess(ctx, submission.Run.ID, output))

	run, err := repo.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, run.Status)
	require.NotNil(t, run.AssistantMessageID)
	session, err := (&store.SessionRepo{DB: repo.DB}).FindByID(ctx, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, session.ActiveLeafMessageID)
	assert.Equal(t, *run.AssistantMessageID, *session.ActiveLeafMessageID)
	require.NotNil(t, session.ActiveBranchID)
	var branchLeaf string
	require.NoError(t, repo.DB.QueryRow(`SELECT leaf_message_id FROM session_branches WHERE id=?`, *session.ActiveBranchID).Scan(&branchLeaf))
	assert.Equal(t, *session.ActiveLeafMessageID, branchLeaf)
	lineage, err := (&store.MessageRepo{DB: repo.DB}).Lineage(ctx, submission.Run.SessionID, *session.ActiveLeafMessageID)
	require.NoError(t, err)
	require.Len(t, lineage, 4)
	assert.Equal(t, "run", lineage[0].Parts[0].Text)
	require.NotNil(t, lineage[1].Parts[1].ToolCall)
	assert.JSONEq(t, `{"path":"sample.txt"}`, string(lineage[1].Parts[1].ToolCall.Arguments))
	require.NotNil(t, lineage[2].Parts[0].ToolResult)
	assert.Equal(t, "sample contents", lineage[2].Parts[0].ToolResult.Content)
	require.Equal(t, []domain.ArtifactReference{artifact}, lineage[2].Parts[0].ToolResult.Artifacts)
	var linkedMessageID string
	require.NoError(t, repo.DB.QueryRow(`SELECT message_id FROM artifacts WHERE id=?`, artifact.ArtifactID).Scan(&linkedMessageID))
	assert.Equal(t, lineage[2].ID, linkedMessageID)
	assert.Equal(t, "done", lineage[3].Parts[0].Text)

	events, err := (&store.EventRepo{DB: repo.DB}).After(ctx, run.ID, 0, 100)
	require.NoError(t, err)
	types := eventTypes(events)
	messageIndex := indexOf(types, "message_committed")
	transcriptIndex := indexOf(types, "run_transcript_committed")
	telemetryIndex := indexOf(types, "run_telemetry")
	assert.NotEqual(t, -1, messageIndex)
	assert.NotEqual(t, -1, transcriptIndex)
	assert.NotEqual(t, -1, telemetryIndex)
	assert.Greater(t, transcriptIndex, messageIndex)
	assert.Greater(t, telemetryIndex, transcriptIndex)
	assert.Equal(t, "run_succeeded", events[len(events)-1].EventType)
}

func TestFinalizeSuccessRollsBackWhenActiveBranchDrifts(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "finalize-branch-drift")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = repo.DB.Exec(`UPDATE sessions SET active_leaf_message_id=NULL WHERE id=?`, submission.Run.SessionID)
	require.NoError(t, err)

	err = repo.FinalizeSuccess(ctx, submission.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{{
		Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}},
	}}})
	require.Error(t, err)
	var projected int
	require.NoError(t, repo.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE run_id=?`, submission.Run.ID).Scan(&projected))
	assert.Zero(t, projected)
	run, getErr := repo.Get(ctx, submission.Run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, domain.RunRunning, run.Status)
}

func TestFinalizeSuccessRollsBackMessagesWhenTerminalEventFails(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "finalize-rollback")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = repo.DB.Exec(`CREATE TRIGGER fail_run_success BEFORE INSERT ON run_events
		WHEN NEW.event_type = 'run_succeeded' BEGIN SELECT RAISE(ABORT, 'injected event failure'); END`)
	require.NoError(t, err)
	err = repo.FinalizeSuccess(ctx, submission.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{{
		Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}},
	}}})
	require.Error(t, err)
	var projected int
	require.NoError(t, repo.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE run_id = ?`, submission.Run.ID).Scan(&projected))
	assert.Zero(t, projected)
	run, getErr := repo.Get(ctx, submission.Run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, domain.RunRunning, run.Status)
}

func TestCallRecorderCommitsProjectionAndEventAtomically(t *testing.T) {
	runRepo, submission := setupSubmittedRun(t, "calls")
	ctx := context.Background()
	_, err := runRepo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	recorder := &store.CallRepo{DB: runRepo.DB}
	callID := uuid.NewString()
	start := domain.ModelCallStart{ID: callID, RunID: submission.Run.ID, Iteration: 1, Attempt: 1,
		RequestedConfig: json.RawMessage(`{}`), EffectiveConfig: json.RawMessage(`{}`)}

	_, err = runRepo.DB.Exec(`CREATE TRIGGER fail_model_started BEFORE INSERT ON run_events
		WHEN NEW.event_type = 'model_call_started' BEGIN SELECT RAISE(ABORT, 'injected event failure'); END`)
	require.NoError(t, err)
	require.Error(t, recorder.ModelStarted(ctx, start))
	var count int
	require.NoError(t, runRepo.DB.QueryRow(`SELECT COUNT(*) FROM model_calls WHERE id = ?`, callID).Scan(&count))
	assert.Zero(t, count)
	_, err = runRepo.DB.Exec(`DROP TRIGGER fail_model_started`)
	require.NoError(t, err)

	require.NoError(t, recorder.ModelStarted(ctx, start))
	usage := domain.Usage{UncachedInputTokens: 10, OutputTokens: 2, CacheReadTokens: 3}
	require.NoError(t, recorder.ModelUsage(ctx, domain.ModelCallFinish{ID: callID, RunID: submission.Run.ID, Iteration: 1, Attempt: 1, Usage: usage}))
	require.NoError(t, recorder.ModelCompleted(ctx, domain.ModelCallFinish{ID: callID, RunID: submission.Run.ID,
		Iteration: 1, Attempt: 1, ActualModel: "api-model", StopReason: domain.StopReasonStop, Usage: usage}))
	var status, actualModel string
	var inputTokens int64
	require.NoError(t, runRepo.DB.QueryRow(`SELECT status, actual_model, uncached_input_tokens FROM model_calls WHERE id = ?`, callID).
		Scan(&status, &actualModel, &inputTokens))
	assert.Equal(t, "completed", status)
	assert.Equal(t, "api-model", actualModel)
	assert.Equal(t, int64(10), inputTokens)
	require.NoError(t, runRepo.DB.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE ref_id = ?`, callID).Scan(&count))
	assert.Equal(t, 1, count)
	var payloadText string
	require.NoError(t, runRepo.DB.QueryRow(`SELECT payload_json FROM run_events
		WHERE run_id = ? AND event_type = 'model_call_completed'`, submission.Run.ID).Scan(&payloadText))
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(payloadText), &payload))
	assert.Equal(t, callID, payload["callId"])
}

func TestCallRecorderRollsBackEveryProjectionWhenItsEventFails(t *testing.T) {
	t.Run("model usage", func(t *testing.T) {
		runRepo, submission := setupSubmittedRun(t, "rollback-model-usage")
		ctx := context.Background()
		_, err := runRepo.Claim(ctx, submission.Run.ID)
		require.NoError(t, err)
		recorder := &store.CallRepo{DB: runRepo.DB}
		callID := uuid.NewString()
		require.NoError(t, recorder.ModelStarted(ctx, domain.ModelCallStart{ID: callID, RunID: submission.Run.ID, Iteration: 1, Attempt: 1}))
		_, err = runRepo.DB.Exec(`CREATE TRIGGER fail_usage_event BEFORE INSERT ON run_events
			WHEN NEW.event_type = 'usage_updated' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		require.NoError(t, err)
		require.Error(t, recorder.ModelUsage(ctx, domain.ModelCallFinish{ID: callID, RunID: submission.Run.ID, Usage: domain.Usage{UncachedInputTokens: 9}}))
		var inputTokens, usageCount int
		require.NoError(t, runRepo.DB.QueryRow(`SELECT uncached_input_tokens FROM model_calls WHERE id = ?`, callID).Scan(&inputTokens))
		require.NoError(t, runRepo.DB.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE ref_id = ?`, callID).Scan(&usageCount))
		assert.Zero(t, inputTokens)
		assert.Zero(t, usageCount)
	})

	for _, test := range []struct {
		name, eventType string
		finish          func(*store.CallRepo, context.Context, domain.ModelCallFinish) error
	}{
		{name: "model completed", eventType: "model_call_completed", finish: (*store.CallRepo).ModelCompleted},
		{name: "model failed", eventType: "model_call_failed", finish: (*store.CallRepo).ModelFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			runRepo, submission := setupSubmittedRun(t, "rollback-"+test.name)
			ctx := context.Background()
			_, err := runRepo.Claim(ctx, submission.Run.ID)
			require.NoError(t, err)
			recorder := &store.CallRepo{DB: runRepo.DB}
			callID := uuid.NewString()
			require.NoError(t, recorder.ModelStarted(ctx, domain.ModelCallStart{ID: callID, RunID: submission.Run.ID, Iteration: 1, Attempt: 1}))
			_, err = runRepo.DB.Exec(`CREATE TRIGGER fail_model_finish_event BEFORE INSERT ON run_events
				WHEN NEW.event_type = '` + test.eventType + `' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
			require.NoError(t, err)
			finish := domain.ModelCallFinish{ID: callID, RunID: submission.Run.ID, Iteration: 1, Attempt: 1,
				ActualModel: "m", StopReason: domain.StopReasonStop, ErrorCode: "provider_unavailable", Final: true}
			require.Error(t, test.finish(recorder, ctx, finish))
			var status string
			require.NoError(t, runRepo.DB.QueryRow(`SELECT status FROM model_calls WHERE id = ?`, callID).Scan(&status))
			assert.Equal(t, "started", status)
		})
	}

	t.Run("tool started", func(t *testing.T) {
		runRepo, submission := setupSubmittedRun(t, "rollback-tool-started")
		ctx := context.Background()
		_, err := runRepo.Claim(ctx, submission.Run.ID)
		require.NoError(t, err)
		recorder := &store.CallRepo{DB: runRepo.DB}
		_, err = runRepo.DB.Exec(`CREATE TRIGGER fail_tool_started_event BEFORE INSERT ON run_events
			WHEN NEW.event_type = 'tool_call_started' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		require.NoError(t, err)
		callID := uuid.NewString()
		require.Error(t, recorder.ToolStarted(ctx, domain.ToolCallStart{ID: callID, RunID: submission.Run.ID,
			Iteration: 1, Call: domain.ToolCall{ID: "tool", Name: "read", Arguments: json.RawMessage(`{}`)}}))
		var count int
		require.NoError(t, runRepo.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE id = ?`, callID).Scan(&count))
		assert.Zero(t, count)
	})

	t.Run("tool completed", func(t *testing.T) {
		runRepo, submission := setupSubmittedRun(t, "rollback-tool-completed")
		ctx := context.Background()
		_, err := runRepo.Claim(ctx, submission.Run.ID)
		require.NoError(t, err)
		recorder := &store.CallRepo{DB: runRepo.DB}
		callID := uuid.NewString()
		call := domain.ToolCall{ID: "tool", Name: "read", Arguments: json.RawMessage(`{}`)}
		require.NoError(t, recorder.ToolStarted(ctx, domain.ToolCallStart{ID: callID, RunID: submission.Run.ID, Iteration: 1, Call: call}))
		_, err = runRepo.DB.Exec(`CREATE TRIGGER fail_tool_completed_event BEFORE INSERT ON run_events
			WHEN NEW.event_type = 'tool_call_completed' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		require.NoError(t, err)
		require.Error(t, recorder.ToolCompleted(ctx, domain.ToolCallFinish{ID: callID, RunID: submission.Run.ID,
			Iteration: 1, Call: call, Result: domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "done"}}))
		var status string
		require.NoError(t, runRepo.DB.QueryRow(`SELECT status FROM tool_calls WHERE id = ?`, callID).Scan(&status))
		assert.Equal(t, "started", status)
	})

	t.Run("tool skipped", func(t *testing.T) {
		runRepo, submission := setupSubmittedRun(t, "rollback-tool-skipped")
		ctx := context.Background()
		_, err := runRepo.Claim(ctx, submission.Run.ID)
		require.NoError(t, err)
		recorder := &store.CallRepo{DB: runRepo.DB}
		_, err = runRepo.DB.Exec(`CREATE TRIGGER fail_tool_skipped_event BEFORE INSERT ON run_events
			WHEN NEW.event_type = 'tool_call_skipped' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		require.NoError(t, err)
		callID := uuid.NewString()
		require.Error(t, recorder.ToolSkipped(ctx, domain.ToolCallFinish{ID: callID, RunID: submission.Run.ID,
			Iteration: 1, Call: domain.ToolCall{ID: "tool", Name: "read", Arguments: json.RawMessage(`{}`)}, Reason: "truncated"}))
		var count int
		require.NoError(t, runRepo.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE id = ?`, callID).Scan(&count))
		assert.Zero(t, count)
	})
}

func TestInterruptedStartedToolRecoversRawArtifactAuditReferences(t *testing.T) {
	runRepo, submission := setupSubmittedRun(t, "recover-raw-artifacts")
	ctx := context.Background()
	_, err := runRepo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	recorder := &store.CallRepo{DB: runRepo.DB}
	recordID := uuid.NewString()
	call := domain.ToolCall{ID: "publish-call", Name: "publish_artifact", Arguments: json.RawMessage(`{"path":"result.csv"}`)}
	require.NoError(t, recorder.ToolStarted(ctx, domain.ToolCallStart{ID: recordID, RunID: submission.Run.ID,
		Iteration: 1, CallIndex: 0, Call: call}))
	var projectID string
	require.NoError(t, runRepo.DB.QueryRow(`SELECT project_id FROM sessions WHERE id=?`, submission.Run.SessionID).Scan(&projectID))
	_, err = runRepo.DB.Exec(`INSERT INTO artifacts
		(id,project_id,session_id,run_id,name,kind,mime_type,storage_path,size_bytes,sha256,metadata_json,
		 created_at,source_tool_call_id,source_kind,retention_class)
		VALUES('artifact-interrupted',?,?,?,?,?,?,?,?,?,?,?,?,'workspace_publish','project')`, projectID,
		submission.Run.SessionID, submission.Run.ID, "result.csv", domain.ArtifactKindTable, "text/csv",
		"blobs/test", 12, "artifact-sha", `{}`, time.Now().UTC().Format(time.RFC3339Nano), call.ID)
	require.NoError(t, err)
	require.NoError(t, runRepo.Interrupt(ctx, submission.Run.ID, "restart"))
	var raw, projected string
	require.NoError(t, runRepo.DB.QueryRow(`SELECT raw_artifact_refs_json,projected_artifact_refs_json
		FROM tool_calls WHERE id=?`, recordID).Scan(&raw, &projected))
	assert.JSONEq(t, `[{"artifactId":"artifact-interrupted","name":"result.csv","kind":"table","mimeType":"text/csv","sizeBytes":12,"sha256":"artifact-sha"}]`, raw)
	assert.JSONEq(t, `[]`, projected)
}

func TestTerminalTransitionClosesStartedModelAndToolCalls(t *testing.T) {
	runRepo, submission := setupSubmittedRun(t, "close-active-calls")
	ctx := context.Background()
	_, err := runRepo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	recorder := &store.CallRepo{DB: runRepo.DB}
	modelID := uuid.NewString()
	require.NoError(t, recorder.ModelStarted(ctx, domain.ModelCallStart{
		ID: modelID, RunID: submission.Run.ID, Iteration: 1, Attempt: 1,
		RequestedConfig: json.RawMessage(`{}`), EffectiveConfig: json.RawMessage(`{}`),
	}))
	toolRecordID := uuid.NewString()
	require.NoError(t, recorder.ToolStarted(ctx, domain.ToolCallStart{
		ID: toolRecordID, RunID: submission.Run.ID, Iteration: 1, CallIndex: 0,
		Call: domain.ToolCall{ID: "tool-1", Name: "read", Arguments: json.RawMessage(`{}`)},
	}))
	require.ErrorContains(t, runRepo.Succeed(ctx, submission.Run.ID), "active calls")
	stillRunning, err := runRepo.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunRunning, stillRunning.Status)
	require.NoError(t, runRepo.Cancel(ctx, submission.Run.ID))
	var modelStatus, toolStatus string
	require.NoError(t, runRepo.DB.QueryRow(`SELECT status FROM model_calls WHERE id = ?`, modelID).Scan(&modelStatus))
	require.NoError(t, runRepo.DB.QueryRow(`SELECT status FROM tool_calls WHERE id = ?`, toolRecordID).Scan(&toolStatus))
	assert.Equal(t, "cancelled", modelStatus)
	assert.Equal(t, "cancelled", toolStatus)
	events, err := (&store.EventRepo{DB: runRepo.DB}).After(ctx, submission.Run.ID, 0, 100)
	require.NoError(t, err)
	types := eventTypes(events)
	modelFailure := indexOf(types, "model_call_failed")
	toolCompletion := indexOf(types, "tool_call_completed")
	terminal := indexOf(types, "run_cancelled")
	assert.Greater(t, modelFailure, -1)
	assert.Greater(t, toolCompletion, -1)
	assert.Greater(t, terminal, modelFailure)
	assert.Greater(t, terminal, toolCompletion)
}

func TestSkippedToolCallStoresValidArgumentsAndRawFragment(t *testing.T) {
	runRepo, submission := setupSubmittedRun(t, "skipped-tool")
	ctx := context.Background()
	_, err := runRepo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	recorder := &store.CallRepo{DB: runRepo.DB}
	call := domain.ToolCall{ID: "tool-1", Name: "read", Arguments: json.RawMessage(`{}`),
		ArgumentsFragment: `{"path":`, Partial: true}
	require.NoError(t, recorder.ToolSkipped(ctx, domain.ToolCallFinish{
		ID: uuid.NewString(), RunID: submission.Run.ID, Iteration: 1, CallIndex: 0,
		Call: call, Reason: "output_truncated", Policy: domain.ToolPolicyMetadata{RiskClass: domain.RiskReadOnly},
	}))
	var status, arguments, fragment, preview, riskClass string
	require.NoError(t, runRepo.DB.QueryRow(`SELECT status, arguments_json, arguments_fragment, result_preview, risk_class
		FROM tool_calls WHERE run_id = ?`, submission.Run.ID).Scan(&status, &arguments, &fragment, &preview, &riskClass))
	assert.Equal(t, "skipped", status)
	assert.JSONEq(t, `{}`, arguments)
	assert.Equal(t, call.ArgumentsFragment, fragment)
	assert.Equal(t, "", preview)
	assert.Equal(t, string(domain.RiskReadOnly), riskClass)
}

func setupSubmittedRun(t *testing.T, requestID string) (*store.RunRepo, *domain.TurnSubmission) {
	t.Helper()
	// V2: Sessions live in per-Session SQLite files under the project store;
	// the returned RunRepo operates on the opened Session database with the
	// file-native provider/model/policy/role stores wired.
	ctx := context.Background()
	stack := newFileConfigStack(t)
	projects := &projectstore.Store{Root: filepath.Join(stack.Home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: requestID, HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: requestID})
	require.NoError(t, err)
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	repo := &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo,
		Policies: stack.Policies, RoleSources: stack.Sources}
	submission, err := repo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: requestID, Text: "run",
	})
	require.NoError(t, err)
	return repo, submission
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func eventTypes(events []domain.RunEvent) []string {
	values := make([]string, len(events))
	for index, event := range events {
		values[index] = event.EventType
	}
	return values
}
