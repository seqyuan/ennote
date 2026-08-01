package agent

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoopReminderReceivesRoutedRuntimeBudget(t *testing.T) {
	selected := domain.ModelRuntimeSnapshot{
		ProviderProfileID: "selected-provider",
		ModelProfileID:    "selected-model",
		APIModel:          "selected-api",
		ContextTokens:     16000,
		MaxOutputTokens:   4000,
	}
	var captured ReminderContext
	fakeProvider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content: []domain.ContentBlock{textBlock("ok")}, StopReason: "stop", ActualModel: "selected-api",
	}})
	router := &SnapshotModelRouter{Factory: func(snapshot domain.ModelRuntimeSnapshot) (llm.Provider, error) {
		assert.Equal(t, selected.ModelProfileID, snapshot.ModelProfileID)
		return fakeProvider, nil
	}}
	tools := &fakeTools{}

	loop := &Loop{
		Provider: fakeProvider, ModelRouter: router, Tools: tools, Events: &memoryWriter{},
		ContextTokens: 8192, MaxOutput: 2048, MaxIterations: 4,
		Reminders: NewReminderRegistry(ReminderFunc{NameField: "capture",
			Fn: func(ctx context.Context, rc ReminderContext) (string, bool) {
				captured = rc
				return "reminder", true
			}}),
	}
	_, err := loop.Run(context.Background(), RunInput{
		RunID: "routed", Model: "initial-model",
		InitialRuntime: selected,
		Routing: domain.FrozenRoutingConfig{
			Candidates: []domain.ModelRuntimeSnapshot{selected},
		},
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("test")}}},
	})
	require.NoError(t, err)

	assert.Equal(t, selected.ModelProfileID, captured.Runtime.ModelProfileID)
	assert.Equal(t, MainUsableTokens(selected), captured.InputTokenBudget)
	assert.Equal(t, tools.Definitions(), captured.Tools)
	assert.Equal(t, 1, captured.Iteration)
}
