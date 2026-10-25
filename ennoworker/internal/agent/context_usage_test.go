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
		PromptSections: []domain.PromptSection{{ID: "base", Kind: domain.PromptSectionBase, Bytes: 20}},
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
	require.NotNil(t, usage.Categories, "the loop must pass frozen sections into the breakdown")
	assert.Equal(t, contextCategoriesVersion, usage.CategoriesVersion)
	sum := 0
	for _, category := range usage.Categories {
		sum += category.Tokens
	}
	assert.Equal(t, usage.ProjectedTokens, sum)
}

func TestSplitContextUsageBreaksDownAndTotalsTheSurface(t *testing.T) {
	messages := []domain.ChatMessage{
		{Role: domain.RoleSystem, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "You are a helpful assistant."}}},
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Hello world"}}},
	}
	tools := []domain.ToolDefinition{{Name: "read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object"}`)}}

	usage := splitContextUsage(messages, tools, nil)
	assert.Positive(t, usage.SystemTokens)
	assert.Positive(t, usage.ToolsTokens)
	assert.Positive(t, usage.MessageTokens)
	assert.Equal(t, EstimateComposition("", tools, messages, 0).InputTokens, usage.ProjectedTokens)

	noTools := splitContextUsage(messages, nil, nil)
	assert.Zero(t, noTools.ToolsTokens)
	assert.Less(t, noTools.ProjectedTokens, usage.ProjectedTokens)
}

func TestSplitContextUsagePartitionsProjectedTokensExactly(t *testing.T) {
	messages := []domain.ChatMessage{
		{Role: domain.RoleSystem, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "You are a helpful assistant."}}},
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Hello world"}}},
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText,
			Text: "<system-reminder>background context</system-reminder>"}}},
	}
	tools := []domain.ToolDefinition{{Name: "read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object"}`)}}
	sections := []domain.PromptSection{
		{ID: "base", Kind: domain.PromptSectionBase, Bytes: 20},
		{ID: "role.definition", Kind: domain.PromptSectionRole, Bytes: 8},
		{ID: "project.agents", Kind: domain.PromptSectionContext, Bytes: 4},
		{ID: "skills.catalog", Kind: domain.PromptSectionSkillsCatalog, Bytes: 4},
	}

	usage := splitContextUsage(messages, tools, sections)
	require.NotNil(t, usage.Categories)
	assert.Equal(t, contextCategoriesVersion, usage.CategoriesVersion)

	sum := 0
	seen := map[string]bool{}
	for _, category := range usage.Categories {
		sum += category.Tokens
		seen[category.Key] = true
		assert.GreaterOrEqual(t, category.Tokens, 0, category.Key)
	}
	assert.Equal(t, usage.ProjectedTokens, sum, "categories must partition projected tokens")
	for _, key := range []string{
		ContextCategorySystem, ContextCategoryRole, ContextCategoryRules, ContextCategorySkills,
		ContextCategoryTools, ContextCategoryReminders, ContextCategoryConversation,
	} {
		assert.True(t, seen[key], "missing category %s", key)
	}
	assert.Positive(t, categoryTokens(usage, ContextCategoryReminders))
	assert.Positive(t, categoryTokens(usage, ContextCategoryConversation))
}

func TestSplitContextUsageOmitsCategoriesWithoutSections(t *testing.T) {
	usage := splitContextUsage([]domain.ChatMessage{
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Hello"}}},
	}, nil, nil)
	assert.Nil(t, usage.Categories)
	assert.Zero(t, usage.CategoriesVersion)
}

func categoryTokens(usage domain.SessionContextUsage, key string) int {
	for _, category := range usage.Categories {
		if category.Key == key {
			return category.Tokens
		}
	}
	return 0
}
