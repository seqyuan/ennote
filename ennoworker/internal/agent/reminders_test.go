package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapSystemReminderFormat(t *testing.T) {
	body := "Your todo list has unfinished items."
	wrapped := WrapSystemReminder(body)
	assert.True(t, strings.HasPrefix(wrapped, "<system-reminder>"))
	assert.True(t, strings.HasSuffix(wrapped, "</system-reminder>"))
	assert.Contains(t, wrapped, body)
	assert.Contains(t, wrapped, "background context")
	assert.Contains(t, wrapped, "NOT a message or instruction from the user")
}

func TestReminderRegistryEmpty(t *testing.T) {
	assert.True(t, NewReminderRegistry().Empty())
	assert.False(t, NewReminderRegistry(ReminderFunc{NameField: "x",
		Fn: func(context.Context, ReminderContext) (string, bool) { return "", false }}).Empty())
	assert.True(t, (*ReminderRegistry)(nil).Empty())
}

func TestReminderRegistryMessagesOrderAndRole(t *testing.T) {
	registry := NewReminderRegistry(
		ReminderFunc{NameField: "first",
			Fn: func(context.Context, ReminderContext) (string, bool) { return "one", true }},
		ReminderFunc{NameField: "quiet",
			Fn: func(context.Context, ReminderContext) (string, bool) { return "", false }},
		ReminderFunc{NameField: "second",
			Fn: func(context.Context, ReminderContext) (string, bool) { return "two", true }},
	)
	msgs := registry.Messages(context.Background(), ReminderContext{})
	require.Len(t, msgs, 2)
	assert.Equal(t, domain.RoleUser, msgs[0].Role)
	assert.Equal(t, domain.RoleUser, msgs[1].Role)
	assert.Contains(t, msgs[0].Content[0].Text, "one")
	assert.Contains(t, msgs[1].Content[0].Text, "two")
	assert.Contains(t, msgs[0].Content[0].Text, "<system-reminder>")
}

func TestReminderFuncNilNeverFires(t *testing.T) {
	registry := NewReminderRegistry(ReminderFunc{NameField: "nil", Fn: nil})
	assert.Empty(t, registry.Messages(context.Background(), ReminderContext{}))
}

func TestReminderProviderName(t *testing.T) {
	provider := ReminderFunc{NameField: "custom-name",
		Fn: func(context.Context, ReminderContext) (string, bool) { return "", false }}
	assert.Equal(t, "custom-name", provider.Name())
}

func TestTodoReminderProviderFiresWhenIncomplete(t *testing.T) {
	store := domain.NewTodoStore()
	store.Set([]domain.TodoItem{
		{Content: "step one", Status: domain.TodoInProgress},
		{Content: "step two", Status: domain.TodoCompleted},
	})
	provider := &TodoReminderProvider{Store: store}
	assert.Equal(t, "todo", provider.Name())

	body, ok := provider.Reminder(context.Background(), ReminderContext{})
	assert.True(t, ok)
	assert.Contains(t, body, "[~] step one")
	assert.Contains(t, body, "[x] step two")
	assert.Contains(t, body, "unfinished")
}

func TestTodoReminderProviderSilentCases(t *testing.T) {
	// Empty store.
	assert.False(t, mustOkReminder(t, &TodoReminderProvider{Store: domain.NewTodoStore()}))
	// Nil store.
	assert.False(t, mustOkReminder(t, &TodoReminderProvider{}))
	// All completed.
	store := domain.NewTodoStore()
	store.Set([]domain.TodoItem{{Content: "done", Status: domain.TodoCompleted}})
	assert.False(t, mustOkReminder(t, &TodoReminderProvider{Store: store}))
}

func mustOkReminder(t *testing.T, provider ReminderProvider) bool {
	t.Helper()
	_, ok := provider.Reminder(context.Background(), ReminderContext{})
	return ok
}

func TestBudgetReminderUsesCurrentTurnInputBudget(t *testing.T) {
	provider := &BudgetReminderProvider{Threshold: 0.8}
	messages := []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{
		Kind: domain.ContentText, Text: strings.Repeat("x", 400),
	}}}}

	_, smallOK := provider.Reminder(context.Background(), ReminderContext{
		Messages: messages, InputTokenBudget: 100,
	})
	_, largeOK := provider.Reminder(context.Background(), ReminderContext{
		Messages: messages, InputTokenBudget: 10000,
	})
	assert.True(t, smallOK)
	assert.False(t, largeOK)
}

func TestBudgetReminderCountsCurrentToolDefinitions(t *testing.T) {
	provider := &BudgetReminderProvider{Threshold: 0.8}
	ctx := ReminderContext{
		Messages: []domain.ChatMessage{{Role: domain.RoleUser,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "work"}}}},
		Tools: []domain.ToolDefinition{{Name: "large", Description: strings.Repeat("x", 4000),
			Parameters: json.RawMessage(`{"type":"object"}`)}},
		InputTokenBudget: 1000,
	}
	_, ok := provider.Reminder(context.Background(), ctx)
	assert.True(t, ok)
}

func TestBudgetReminderStaysSilentWhenUnknown(t *testing.T) {
	provider := &BudgetReminderProvider{}
	_, ok := provider.Reminder(context.Background(), ReminderContext{SystemPrompt: "hello",
		Messages: []domain.ChatMessage{{Role: domain.RoleUser,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "work"}}}}})
	assert.False(t, ok)
}
