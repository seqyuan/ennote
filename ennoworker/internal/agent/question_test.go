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

func questionCompletion() domain.Completion {
	return domain.Completion{StopReason: domain.StopReasonToolCalls, ActualModel: "fake", ToolCalls: []domain.ToolCall{{
		ID: "question-call", Name: "ask_user",
		Arguments: json.RawMessage(`{"questions":[{"id":"scope","prompt":"Which samples?","options":[{"label":"All"},{"label":"Tumor only"}],"allowOther":true}]}`),
	}}}
}

func questionLoop(t *testing.T, provider *llm.FakeProvider, tools *fakeTools, allow bool) *Loop {
	t.Helper()
	policy := askPolicy(t)
	snapshot := domain.PolicySnapshot{ID: "ask", Kind: domain.PolicyKindTool, Version: 1, Config: policy.snapshot.Config}
	return &Loop{Provider: provider, Tools: tools, Events: &memoryWriter{}, ToolPolicy: policy,
		ToolPolicySnapshot: snapshot, MaxIterations: 4, AllowQuestions: allow}
}

func TestLoopSuspendsQuestionBeforeExecutingTheTool(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: questionCompletion()})
	tools := &fakeTools{}
	loop := questionLoop(t, provider, tools, true)
	_, err := loop.Run(context.Background(), RunInput{RunID: "question", Model: "fake", SystemPrompt: "frozen",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("ask")}}}})
	var required *QuestionRequiredError
	require.True(t, errors.As(err, &required))
	assert.NotEmpty(t, required.Digest)
	require.Len(t, required.Questions, 1)
	assert.Equal(t, "scope", required.Questions[0].ID)
	assert.Equal(t, ResumeStateVersion, required.State.Version)
	assert.Equal(t, ApprovalDigestV2, required.State.ApprovalDigestVersion)
	assert.Empty(t, tools.calls)
}

func TestLoopQuestionResumeFeedsAnswersBackToTheModel(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: questionCompletion()},
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop,
			Content: []domain.ContentBlock{textBlock("using tumor only")}}, TextDeltas: []string{"using tumor only"}},
	)
	tools := &fakeTools{}
	loop := questionLoop(t, provider, tools, true)
	input := RunInput{RunID: "question", Model: "fake", SystemPrompt: "frozen",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("ask")}}}}
	_, err := loop.Run(context.Background(), input)
	var required *QuestionRequiredError
	require.True(t, errors.As(err, &required))

	input.History = nil
	input.Resume = &required.State
	input.Approval = &ApprovalResolution{
		Decision:    domain.DecisionApproved,
		BatchDigest: required.Digest,
		Kind:        domain.ApprovalKindQuestion,
		Answers:     []domain.QuestionAnswer{{QuestionID: "scope", Values: []string{"Tumor only"}}},
	}
	result, err := loop.Run(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, tools.calls, "ask_user is resolved from the checkpoint, not executed")
	require.Len(t, provider.Requests, 2)
	assert.Contains(t, messageText(result.Messages[len(result.Messages)-1]), "using tumor only")

	var answered bool
	for _, message := range result.Messages {
		if message.Role != domain.RoleTool || len(message.Content) == 0 || message.Content[0].ToolResult == nil {
			continue
		}
		if message.Content[0].ToolResult.ToolCallID == "question-call" {
			answered = true
			assert.Contains(t, message.Content[0].ToolResult.Content, "Tumor only")
		}
	}
	assert.True(t, answered, "the model must receive the answer as a tool result")
}

func TestLoopRejectsQuestionWhenDisallowed(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: questionCompletion()},
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop,
			Content: []domain.ContentBlock{textBlock("continued")}}, TextDeltas: []string{"continued"}},
	)
	tools := &fakeTools{}
	loop := questionLoop(t, provider, tools, false)
	_, err := loop.Run(context.Background(), RunInput{RunID: "question-child", Model: "fake", SystemPrompt: "frozen",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("ask")}}}})
	var required *QuestionRequiredError
	assert.False(t, errors.As(err, &required), "a disallowed question must not pause the Run")
	assert.Empty(t, tools.calls)
}
