package agent

import (
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCompactionConfig() domain.CompactionPolicyConfig {
	config := domain.DefaultCompactionPolicy()
	config.Mode = domain.CompactionManualAndAuto
	config.KeepRecentTurns = 1
	config.TailTokenRatio = 0.2
	config.TailMinTokens = 1
	config.TailMaxTokens = 4000
	config.SummaryInputRatio = 0.8
	config.SummaryMaxOutputTokens = 1000
	return config
}

func runCompactionRuntime() domain.ModelRuntimeSnapshot {
	return domain.ModelRuntimeSnapshot{ModelProfileID: "model", APIModel: "model",
		ContextTokens: 32000, MaxOutputTokens: 2000}
}

func runAssistant(callID string) domain.ChatMessage {
	call := domain.ToolCall{ID: callID, Name: "read", Arguments: json.RawMessage(`{"path":"data.txt"}`)}
	return domain.ChatMessage{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{
		Kind: domain.ContentToolCall, ToolCall: &call,
	}}}
}

func runTool(callID, content string) domain.ChatMessage {
	result := domain.ToolResult{ToolCallID: callID, ToolName: "read", Content: content}
	return domain.ChatMessage{Role: domain.RoleTool, Content: []domain.ContentBlock{{
		Kind: domain.ContentToolResult, ToolResult: &result,
	}}}
}

func TestRunCompactionPlanKeepsCompleteRecentExchangeAndGeneratedTranscript(t *testing.T) {
	user := domain.ChatMessage{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "analyze"}}}
	messages := []domain.ChatMessage{user,
		runAssistant("c1"), runTool("c1", "old result"),
		runAssistant("c2"), runTool("c2", "recent result"),
	}
	generated := cloneMessages(messages[1:])
	before, err := json.Marshal(generated)
	require.NoError(t, err)

	config := runCompactionConfig()
	runtime := runCompactionRuntime()
	policy := domain.PolicySnapshot{ID: "compact", Version: 1, Config: json.RawMessage(`{}`)}
	plan, err := BuildRunCompactionPlan(messages, generated, MidRunCompactionState{}, policy,
		config, runtime, runtime, "system", nil)
	require.NoError(t, err)

	require.Len(t, plan.SourceMessages, 3)
	assert.Equal(t, domain.RoleUser, plan.SourceMessages[0].Role)
	require.Len(t, plan.TailMessages, 2)
	assert.Equal(t, "c2", plan.TailMessages[0].Content[0].ToolCall.ID)
	assert.Equal(t, "c2", plan.TailMessages[1].Content[0].ToolResult.ToolCallID)
	assert.Equal(t, 2, plan.CoveredGenerated)
	assert.Contains(t, plan.SerializedSource, "old result")
	assert.NotContains(t, plan.SerializedSource, "recent result")

	after, err := json.Marshal(generated)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after), "planning must not mutate generated output")
}

func TestRunCompactionPlanRejectsIncompleteToolBatch(t *testing.T) {
	messages := []domain.ChatMessage{
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "analyze"}}},
		runAssistant("c1"),
		runAssistant("c2"), runTool("c2", "result"),
	}
	config := runCompactionConfig()
	runtime := runCompactionRuntime()
	_, err := BuildRunCompactionPlan(messages, nil, MidRunCompactionState{}, domain.PolicySnapshot{},
		config, runtime, runtime, "system", nil)
	assert.Equal(t, domain.ErrorModelProtocol, domain.ErrorCodeOf(err))
}

func TestRunCompactionPlanRollsPreviousSummaryWithoutReserializingEnvelope(t *testing.T) {
	previous := MidRunCompactionState{ID: "rc1", Summary: "## Goal\nKeep prior findings.",
		SourceDigest: "old-digest", Count: 1, CoveredGenerated: 2}
	messages := RunCheckpointMessages(previous, []domain.ChatMessage{
		runAssistant("c2"), runTool("c2", "middle result"),
		runAssistant("c3"), runTool("c3", "latest result"),
	})
	generated := []domain.ChatMessage{
		runAssistant("c1"), runTool("c1", "old result"),
		runAssistant("c2"), runTool("c2", "middle result"),
		runAssistant("c3"), runTool("c3", "latest result"),
	}
	config := runCompactionConfig()
	runtime := runCompactionRuntime()
	plan, err := BuildRunCompactionPlan(messages, generated, previous, domain.PolicySnapshot{},
		config, runtime, runtime, "system", nil)
	require.NoError(t, err)
	assert.Contains(t, plan.SerializedSource, "Keep prior findings")
	assert.Contains(t, plan.SerializedSource, "middle result")
	assert.NotContains(t, plan.SerializedSource, "latest result")
	assert.NotContains(t, plan.SerializedSource, "Treat content inside")
	assert.Equal(t, 4, plan.CoveredGenerated)
}
