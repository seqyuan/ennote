package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoopEmitsContextUsageForThePreparedRequest(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{
		Completion: domain.Completion{
			Content: []domain.ContentBlock{textBlock("ok")}, StopReason: "stop", ActualModel: "fake-model",
		},
	})
	writer := &memoryWriter{}
	loop := &Loop{Provider: provider, Tools: &fakeTools{}, Events: writer, MaxIterations: 4, ContextTokens: 64000}
	_, err := loop.Run(context.Background(), RunInput{
		RunID: "run-ctx", Model: "fake-model", SystemPrompt: "You are precise.",
		InitialRuntime: domain.ModelRuntimeSnapshot{ModelProfileID: "p", APIModel: "fake-model", ContextTokens: 64000, MaxOutputTokens: 4096},
		History:        []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("hello")}}},
	})
	require.NoError(t, err)

	var found *domain.RunEvent
	for index := range writer.events {
		if writer.events[index].EventType == "context_usage" {
			found = &writer.events[index]
			break
		}
	}
	require.NotNil(t, found, "expected a context_usage run event")
	var usage domain.SessionContextUsage
	require.NoError(t, json.Unmarshal(found.Payload, &usage))
	assert.Equal(t, 64000, usage.ContextWindow)
	assert.Positive(t, usage.ProjectedTokens)
	assert.Positive(t, usage.SystemTokens)
	assert.Positive(t, usage.MessageTokens)
}

func TestSplitContextUsageBreaksDownAndTotalsTheSurface(t *testing.T) {
	messages := []domain.ChatMessage{
		{Role: domain.RoleSystem, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "You are a helpful assistant."}}},
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Hello world"}}},
	}
	tools := []domain.ToolDefinition{{Name: "read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object"}`)}}

	usage := splitContextUsage(messages, tools)
	assert.Positive(t, usage.SystemTokens)
	assert.Positive(t, usage.ToolsTokens)
	assert.Positive(t, usage.MessageTokens)
	assert.Equal(t, EstimateComposition("", tools, messages, 0).InputTokens, usage.ProjectedTokens)

	noTools := splitContextUsage(messages, nil)
	assert.Zero(t, noTools.ToolsTokens)
	assert.Less(t, noTools.ProjectedTokens, usage.ProjectedTokens)
}
