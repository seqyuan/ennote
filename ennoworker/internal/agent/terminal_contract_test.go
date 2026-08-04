package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type terminalContractToolRunner struct{}

type testBudgetController struct {
	allowedOutput int
	admitErr      error
	modelCalls    int
	toolCalls     int
	completed     []domain.Usage
}

func (b *testBudgetController) AdmitModelCall(context.Context, string, string, int64, int) (int, error) {
	b.modelCalls++
	if b.admitErr != nil {
		return 0, b.admitErr
	}
	return b.allowedOutput, nil
}
func (b *testBudgetController) CompleteModelCall(_ context.Context, _, _ string, usage domain.Usage) error {
	b.completed = append(b.completed, usage)
	return nil
}
func (b *testBudgetController) AdmitToolCalls(_ context.Context, _ string, count int) error {
	b.toolCalls += count
	return b.admitErr
}

func (terminalContractToolRunner) Definitions() []domain.ToolDefinition { return nil }
func (terminalContractToolRunner) Execute(context.Context, domain.ToolCall) (domain.ToolResult, error) {
	return domain.ToolResult{}, nil
}

func TestInterceptSubmitResultCapturesStructuredContract(t *testing.T) {
	gate := &SubmitResultGate{}
	loop := &Loop{
		Provider: llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
			StopReason: domain.StopReasonToolCalls,
			ToolCalls: []domain.ToolCall{{ID: "call-1", Name: "submit_result",
				Arguments: json.RawMessage(`{"status":"completed","summary":"inspected workspace"}`)}},
		}}),
		Tools:            terminalContractToolRunner{},
		Events:           &memoryWriter{},
		SubmitResultGate: gate,
	}
	result, err := loop.Run(context.Background(), RunInput{
		RunID: "child-run", SystemPrompt: "explorer", History: []domain.ChatMessage{},
		InitialRuntime: domain.ModelRuntimeSnapshot{ModelProfileID: "m", APIModel: "model", ContextTokens: 32000, MaxOutputTokens: 2048},
	})
	require.NoError(t, err)
	require.NotNil(t, gate.Result)
	assert.Equal(t, domain.SubmitCompleted, gate.Result.Status)
	assert.Equal(t, "inspected workspace", gate.Result.Summary)
	// The terminal tool call must be part of the generated transcript.
	var found bool
	for _, message := range result.Messages {
		for _, block := range message.Content {
			if block.ToolResult != nil && block.ToolResult.ToolName == "submit_result" {
				found = true
			}
		}
	}
	assert.True(t, found, "submit_result tool result must be in the child transcript")
}

func TestInterceptSubmitResultRejectsMalformedContracts(t *testing.T) {
	cases := []string{
		`{"status":"hacked","summary":"x"}`,
		`{"summary":"no status"}`,
		`{"status":"completed","summary":"x","unknown":true}`,
		`{"status":"completed","summary":"x","payload":["not-an-object"]}`,
		`{"status":"completed","summary":"x","artifactRefs":[{"artifactId":"a","name":"n","kind":"file","mimeType":"text/plain","sha256":""}]}`,
	}
	for _, args := range cases {
		gate := &SubmitResultGate{}
		loop := &Loop{
			Provider: llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
				StopReason: domain.StopReasonToolCalls,
				ToolCalls:  []domain.ToolCall{{ID: "c", Name: "submit_result", Arguments: json.RawMessage(args)}},
			}}),
			Tools:            terminalContractToolRunner{},
			Events:           &memoryWriter{},
			SubmitResultGate: gate,
		}
		_, err := loop.Run(context.Background(), RunInput{
			RunID: "child", SystemPrompt: "x", History: []domain.ChatMessage{},
			InitialRuntime: domain.ModelRuntimeSnapshot{ModelProfileID: "m", APIModel: "model"},
		})
		require.Error(t, err)
		assert.Nil(t, gate.Result)
	}

	// submit_result alongside another tool must be rejected.
	gate := &SubmitResultGate{}
	loop := &Loop{
		Provider: llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
			StopReason: domain.StopReasonToolCalls,
			ToolCalls: []domain.ToolCall{
				{ID: "c1", Name: "submit_result", Arguments: json.RawMessage(`{"status":"completed","summary":"x"}`)},
				{ID: "c2", Name: "read", Arguments: json.RawMessage(`{"path":"/x"}`)},
			},
		}}),
		Tools:            terminalContractToolRunner{},
		Events:           &memoryWriter{},
		SubmitResultGate: gate,
	}
	_, err := loop.Run(context.Background(), RunInput{
		RunID: "child", SystemPrompt: "x", History: []domain.ChatMessage{},
		InitialRuntime: domain.ModelRuntimeSnapshot{ModelProfileID: "m", APIModel: "model"},
	})
	require.Error(t, err)
	assert.Nil(t, gate.Result)
}

func TestLoopBudgetAdmissionStopsBeforeProvider(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop}})
	budget := &testBudgetController{admitErr: errors.New("exhausted")}
	loop := &Loop{Provider: provider, Tools: terminalContractToolRunner{}, Events: &memoryWriter{},
		BudgetController: budget, MaxIterations: 1}
	_, err := loop.Run(context.Background(), RunInput{RunID: "budget-child", Model: "fake",
		InitialRuntime: domain.ModelRuntimeSnapshot{ModelProfileID: "m", APIModel: "fake", MaxOutputTokens: 100}})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorDelegationBudgetExceeded, domain.ErrorCodeOf(err))
	assert.Empty(t, provider.Requests)
	assert.Equal(t, 1, budget.modelCalls)
}

func TestLoopBudgetAdmissionClampsProviderOutputAndRecordsUsage(t *testing.T) {
	usage := domain.Usage{InputTokens: 5, OutputTokens: 3}
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		StopReason: domain.StopReasonStop, Usage: usage,
	}})
	budget := &testBudgetController{allowedOutput: 7}
	loop := &Loop{Provider: provider, Tools: terminalContractToolRunner{}, Events: &memoryWriter{},
		BudgetController: budget, MaxIterations: 1}
	_, err := loop.Run(context.Background(), RunInput{RunID: "budget-child", Model: "fake",
		InitialRuntime: domain.ModelRuntimeSnapshot{ModelProfileID: "m", APIModel: "fake", MaxOutputTokens: 100}})
	require.NoError(t, err)
	require.Len(t, provider.Requests, 1)
	assert.Equal(t, 7, provider.Requests[0].MaxTokens)
	assert.Equal(t, []domain.Usage{usage}, budget.completed)
}

func TestValidateSubmitResultToolSchema(t *testing.T) {
	result, err := tools.ValidateSubmitResult(json.RawMessage(`{"status":"needs_input","summary":"need approval","payload":{"q":"x"}}`))
	require.NoError(t, err)
	assert.Equal(t, domain.SubmitNeedsInput, result.Status)
	_, err = tools.ValidateSubmitResult(json.RawMessage(`{"status":"completed"}`))
	require.Error(t, err)
}
