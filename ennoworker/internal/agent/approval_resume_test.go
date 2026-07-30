package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func askPolicy(t *testing.T) *BuiltinToolPolicy {
	t.Helper()
	raw, err := json.Marshal(domain.ToolPolicyConfig{Mode: string(domain.PermissionAsk)})
	require.NoError(t, err)
	policy, err := NewBuiltinToolPolicy(domain.PolicySnapshot{ID: "ask", Kind: domain.PolicyKindTool, Version: 1, Config: raw})
	require.NoError(t, err)
	return policy
}

func approvalCompletion() domain.Completion {
	return domain.Completion{StopReason: domain.StopReasonToolCalls, ActualModel: "fake", ToolCalls: []domain.ToolCall{
		{ID: "read-call", Name: "read", Arguments: json.RawMessage(`{"path":"notes.txt"}`)},
		{ID: "write-call", Name: "write", Arguments: json.RawMessage(`{"path":"notes.txt","content":"updated"}`)},
	}}
}

func TestLoopSuspendsMixedAskBatchBeforeAnyToolStarts(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: approvalCompletion()})
	tools := &fakeTools{}
	writer := &memoryWriter{}
	policy := askPolicy(t)
	loop := &Loop{Provider: provider, Tools: tools, Events: writer, ToolPolicy: policy,
		ToolPolicySnapshot: domain.PolicySnapshot{ID: "ask", Kind: domain.PolicyKindTool, Version: 1, Config: policy.snapshot.Config},
		MaxIterations:      4}
	_, err := loop.Run(context.Background(), RunInput{RunID: "approval", Model: "fake", SystemPrompt: "frozen",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("update")}}}})
	var required *ApprovalRequiredError
	require.True(t, errors.As(err, &required))
	assert.NotEmpty(t, required.BatchDigest)
	require.Len(t, required.Items, 1)
	assert.Equal(t, "write", required.Items[0].ToolName)
	assert.Equal(t, ResumeStateVersion, required.State.Version)
	assert.Equal(t, "frozen", required.State.SystemPrompt)
	assert.Empty(t, tools.calls)
	assert.NotContains(t, writer.types(), "tool_call_started")
	require.Len(t, provider.Requests, 1)
}

func TestLoopResumeApproveExecutesSavedBatchWithoutRepeatingModelCall(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: approvalCompletion()},
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop,
			Content: []domain.ContentBlock{textBlock("done")}}, TextDeltas: []string{"done"}},
	)
	tools := &fakeTools{result: domain.ToolResult{Content: "ok"}}
	policy := askPolicy(t)
	snapshot := domain.PolicySnapshot{ID: "ask", Kind: domain.PolicyKindTool, Version: 1, Config: policy.snapshot.Config}
	loop := &Loop{Provider: provider, Tools: tools, Events: &memoryWriter{}, ToolPolicy: policy,
		ToolPolicySnapshot: snapshot, MaxIterations: 4}
	input := RunInput{RunID: "approval", Model: "fake", SystemPrompt: "frozen",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("update")}}}}
	_, err := loop.Run(context.Background(), input)
	var required *ApprovalRequiredError
	require.True(t, errors.As(err, &required))

	input.History = nil
	required.State.Version = 1 // Upgrade compatibility: v1 has no mid-run compaction field.
	input.Resume = &required.State
	input.Approval = &ApprovalResolution{Decision: domain.DecisionApproved, BatchDigest: required.BatchDigest}
	result, err := loop.Run(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, tools.calls, 2)
	assert.Equal(t, []string{"read", "write"}, []string{tools.calls[0].Name, tools.calls[1].Name})
	require.Len(t, provider.Requests, 2, "resume must use only the next model request")
	assert.Equal(t, "done", messageText(result.Messages[len(result.Messages)-1]))
}

func TestLoopResumeRejectRunsReadOnlyAndContinuesWithDeniedToolResult(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: approvalCompletion()},
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop,
			Content: []domain.ContentBlock{textBlock("used read-only fallback")}}},
	)
	tools := &fakeTools{result: domain.ToolResult{Content: "read ok"}}
	policy := askPolicy(t)
	snapshot := domain.PolicySnapshot{ID: "ask", Kind: domain.PolicyKindTool, Version: 1, Config: policy.snapshot.Config}
	loop := &Loop{Provider: provider, Tools: tools, Events: &memoryWriter{}, ToolPolicy: policy,
		ToolPolicySnapshot: snapshot, MaxIterations: 4}
	input := RunInput{RunID: "reject", Model: "fake", History: []domain.ChatMessage{{Role: domain.RoleUser,
		Content: []domain.ContentBlock{textBlock("update")}}}}
	_, err := loop.Run(context.Background(), input)
	var required *ApprovalRequiredError
	require.True(t, errors.As(err, &required))
	input.History = nil
	input.Resume = &required.State
	input.Approval = &ApprovalResolution{Decision: domain.DecisionRejected, BatchDigest: required.BatchDigest}
	result, err := loop.Run(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, tools.calls, 1)
	assert.Equal(t, "read", tools.calls[0].Name)
	require.Len(t, provider.Requests, 2)
	foundRejected := false
	for _, message := range result.Generated {
		for _, block := range message.Content {
			if block.ToolResult != nil && block.ToolResult.ToolCallID == "write-call" {
				foundRejected = block.ToolResult.IsError && block.ToolResult.Content == "Tool call rejected by the user"
			}
		}
	}
	assert.True(t, foundRejected)
}

func TestLoopApprovalResumePreservesMidRunCompactionState(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: oneReadCompletion()},
		llm.FakeStep{Completion: approvalCompletion()},
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop,
			Content: []domain.ContentBlock{textBlock("done after approval")}}},
	)
	tools := &fakeTools{result: domain.ToolResult{Content: "ok"}}
	policy := askPolicy(t)
	snapshot := domain.PolicySnapshot{ID: "ask", Kind: domain.PolicyKindTool, Version: 1, Config: policy.snapshot.Config}
	var previousIDs []string
	compactor := midRunCompactorFunc(func(_ context.Context, request MidRunCompactionRequest) (MidRunCompactionResult, error) {
		previousIDs = append(previousIDs, request.Previous.ID)
		if request.Previous.ID != "" {
			return MidRunCompactionResult{Messages: request.Messages, State: request.Previous}, nil
		}
		return compactedRunContext(request, "approval-compaction"), nil
	})
	loop := &Loop{Provider: provider, Tools: tools, Events: &memoryWriter{}, ToolPolicy: policy,
		ToolPolicySnapshot: snapshot, MidRunCompactor: compactor, MaxIterations: 5}
	input := RunInput{RunID: "approval-after-compaction", Model: "fake", SystemPrompt: "frozen",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("update")}}}}
	_, err := loop.Run(context.Background(), input)
	var required *ApprovalRequiredError
	require.True(t, errors.As(err, &required))
	assert.Equal(t, "approval-compaction", required.State.MidRunCompaction.ID)
	assert.Equal(t, ResumeStateVersion, required.State.Version)
	require.Len(t, provider.Requests, 2)

	input.History = nil
	input.Resume = &required.State
	input.Approval = &ApprovalResolution{Decision: domain.DecisionApproved, BatchDigest: required.BatchDigest}
	result, err := loop.Run(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, []string{"", "approval-compaction"}, previousIDs)
	require.Len(t, provider.Requests, 3, "resume executes the saved batch before only the next model request")
	assert.Equal(t, "done after approval", messageText(result.Generated[len(result.Generated)-1]))
	assert.Equal(t, []string{"read", "read", "write"}, []string{tools.calls[0].Name, tools.calls[1].Name, tools.calls[2].Name})
}

func TestLoopResumeRejectsChangedBatchDigest(t *testing.T) {
	policy := askPolicy(t)
	loop := &Loop{Provider: llm.NewFakeProvider(), Tools: &fakeTools{}, Events: &memoryWriter{}, ToolPolicy: policy,
		ToolPolicySnapshot: domain.PolicySnapshot{ID: "ask", Version: 1, Config: policy.snapshot.Config}, MaxIterations: 4}
	state := ResumeState{Version: ResumeStateVersion, Iteration: 1, Completion: approvalCompletion()}
	_, err := loop.Run(context.Background(), RunInput{RunID: "bad", Resume: &state,
		Approval: &ApprovalResolution{Decision: domain.DecisionApproved, BatchDigest: "changed"}})
	assert.Equal(t, domain.ErrorApprovalCheckpointInvalid, domain.ErrorCodeOf(err))
}
