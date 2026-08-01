package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type persistHelperTools struct {
	defs []domain.ToolDefinition
}

func (t *persistHelperTools) Definitions() []domain.ToolDefinition { return t.defs }
func (t *persistHelperTools) Execute(ctx context.Context, call domain.ToolCall) domain.ToolResult {
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "ok"}
}

type persistHelperEvents struct{}

func (w *persistHelperEvents) Append(ctx context.Context, runID string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	return nil, nil
}

func TestReminderIsNotPersistedToMessages(t *testing.T) {
	db := store.SetupDB(t)
	ctx := context.Background()

	pRepo := &store.ProjectRepo{DB: db}
	project, _, err := pRepo.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "rp", HostPath: t.TempDir(),
	})
	require.NoError(t, err)

	sRepo := &store.SessionRepo{DB: db}
	session, err := sRepo.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "rp-session"})
	require.NoError(t, err)

	mRepo := &store.MessageRepo{DB: db}

	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content:    []domain.ContentBlock{{Kind: domain.ContentText, Text: "assistant reply"}},
		StopReason: "stop", ActualModel: "fake-model",
	}})
	tools := &persistHelperTools{defs: []domain.ToolDefinition{
		{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)},
	}}

	loop := &agent.Loop{
		Provider: provider, Tools: tools, Events: &persistHelperEvents{},
		MaxIterations: 4, ContextTokens: 8192,
		Reminders: agent.NewReminderRegistry(agent.ReminderFunc{NameField: "test",
			Fn: func(ctx context.Context, rc agent.ReminderContext) (string, bool) { return "ephemeral", true }}),
	}

	result, err := loop.Run(context.Background(), agent.RunInput{
		RunID: "rp-run", Model: "fake-model",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{
			{Kind: domain.ContentText, Text: "hello"},
		}}},
	})
	require.NoError(t, err)
	require.Greater(t, len(result.Generated), 0)

	// 1. Provider request contains reminder.
	require.Len(t, provider.Requests, 1)
	assert.True(t, containsText(provider.Requests[0].Messages, "<system-reminder>"))

	// 2. result.Generated does NOT contain reminder.
	for _, msg := range result.Generated {
		for _, b := range msg.Content {
			assert.False(t, strings.Contains(b.Text, "<system-reminder>"))
		}
	}

	// 3. Persist generated messages and verify they don't contain reminder.
	parent, err := mRepo.CreateUserMessage(ctx, session.ID, "", "hello")
	require.NoError(t, err)
	for _, msg := range result.Generated {
		if msg.Role == domain.RoleAssistant {
			for _, b := range msg.Content {
				if b.Text != "" {
					_, err := mRepo.CreateUserMessage(ctx, session.ID, parent.ID, b.Text)
					require.NoError(t, err)
				}
			}
		}
	}
	lineage, err := mRepo.Lineage(ctx, session.ID, parent.ID)
	require.NoError(t, err)
	for _, m := range lineage {
		d, _ := json.Marshal(m)
		assert.NotContains(t, string(d), "<system-reminder>")
	}
}

func containsText(msgs []domain.ChatMessage, want string) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, want) {
				return true
			}
		}
	}
	return false
}
