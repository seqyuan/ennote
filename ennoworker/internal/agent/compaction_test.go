package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCompactionPlanKeepsCompleteRecentTurns(t *testing.T) {
	lineage := compactionLineage(5)
	config := domain.DefaultCompactionPolicy()
	config.KeepRecentTurns = 2
	config.TailMinTokens = 1
	config.TailMaxTokens = 800
	config.TailTokenRatio = 0.01
	runtime := domain.ModelRuntimeSnapshot{ModelProfileID: "m", APIModel: "model", ContextTokens: 32000, MaxOutputTokens: 2000}
	policy := domain.PolicySnapshot{ID: "policy", Kind: domain.PolicyKindCompaction, Version: 1, Config: []byte(`{"mode":"manual_only"}`)}

	plan, err := BuildCompactionPlan(lineage, nil, policy, config, runtime, runtime, "system", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "u-3", plan.FirstKeptMessageID)
	assert.Equal(t, "a-2", plan.SourceThroughMessageID)
	assert.Len(t, plan.TailMessages, 4)
	assert.NotEmpty(t, plan.SourceDigest)
}

func TestSummaryRequestReusesMainPrefixAndEmbedsContract(t *testing.T) {
	config := domain.DefaultCompactionPolicy()
	plan := CompactionPlan{SerializedSource: "serialized-source"}
	runtime := domain.ModelRuntimeSnapshot{APIModel: "m", MaxOutputTokens: 4096}
	tools := []domain.ToolDefinition{{Name: "read", Parameters: json.RawMessage(`{}`)}}

	request := SummaryRequest(plan, config, runtime, "MAIN SYSTEM PROMPT", tools)

	require.Len(t, request.Messages, 2)
	assert.Equal(t, domain.RoleSystem, request.Messages[0].Role)
	assert.Equal(t, "MAIN SYSTEM PROMPT", request.Messages[0].Content[0].Text)
	assert.Equal(t, tools, request.Tools)

	user := request.Messages[1]
	assert.Equal(t, domain.RoleUser, user.Role)
	assert.Contains(t, user.Content[0].Text, "serialized-source")
	assert.Contains(t, user.Content[0].Text, "## Goal")
}

func TestRepeatedCompactionIncludesFormerTail(t *testing.T) {
	lineage := compactionLineage(7)
	config := domain.DefaultCompactionPolicy()
	config.KeepRecentTurns = 2
	config.TailMinTokens = 1
	config.TailMaxTokens = 800
	config.TailTokenRatio = 0.01
	runtime := domain.ModelRuntimeSnapshot{ModelProfileID: "m", APIModel: "model", ContextTokens: 32000, MaxOutputTokens: 2000}
	policy := domain.PolicySnapshot{ID: "policy", Kind: domain.PolicyKindCompaction, Version: 1, Config: []byte(`{}`)}
	previous := &domain.ContextCompaction{ID: "old", FirstKeptMessageID: "u-2", Summary: validSummary("old"), SummaryDigest: DigestText(validSummary("old"))}

	plan, err := BuildCompactionPlan(lineage, previous, policy, config, runtime, runtime, "system", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "u-2", plan.SourceFromMessageID)
	assert.Contains(t, plan.SerializedSource, "[Previous context checkpoint]")
	assert.Contains(t, plan.SerializedSource, "id=u-2")
}

func TestProjectionAndSerializationDoNotLeakImageBytesOrFullToolResult(t *testing.T) {
	raw := strings.Repeat("secret-result-", 1000)
	messages := []domain.Message{{ID: "a", Role: "assistant", Parts: []domain.ContentBlock{{
		Kind: domain.ContentToolCall, ToolCall: &domain.ToolCall{ID: "call", Name: "read", Arguments: []byte(`{"path":"x"}`)},
	}}}, {ID: "t", Role: "tool", Parts: []domain.ContentBlock{{
		Kind: domain.ContentToolResult, ToolResult: &domain.ToolResult{ToolCallID: "call", ToolName: "read", Content: raw},
	}, {Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: "image", MIMEType: "image/png", SHA256: "abc", Data: []byte("IMAGE-BYTES")}}}}}
	config := domain.DefaultCompactionPolicy()

	projected := ProjectCanonicalMessages(messages, len(messages), config)
	encoded := projected[1].Content[0].ToolResult.Content
	assert.Less(t, len(encoded), len(raw))
	assert.Contains(t, encoded, DigestText(raw))
	serialized := SerializeCompactionSource("", messages, false, "")
	assert.NotContains(t, serialized, "IMAGE-BYTES")
	assert.Contains(t, serialized, "bytes omitted")
	assert.Less(t, len(serialized), len(raw))
}

func TestValidateCompactionSummaryRequiresContract(t *testing.T) {
	require.NoError(t, ValidateCompactionSummary(validSummary(strings.Repeat("detail ", 20)), 2000))
	err := ValidateCompactionSummary("## Goal\nshort", 2000)
	assert.Error(t, err)
	assert.Equal(t, domain.ErrorCompactionOutputInvalid, domain.ErrorCodeOf(err))
}

func TestCheckpointMessagesUseSyntheticUserEnvelopeAndRetainTail(t *testing.T) {
	lineage := compactionLineage(3)
	summary := validSummary("state")
	checkpoint := &domain.ContextCompaction{ID: "cp", SourceDigest: "digest", Summary: summary,
		SummaryDigest: DigestText(summary), FirstKeptMessageID: "u-2"}
	messages, err := CheckpointMessages(checkpoint, lineage)
	require.NoError(t, err)
	require.Len(t, messages, 3)
	assert.Equal(t, domain.RoleUser, messages[0].Role)
	assert.Equal(t, domain.ContentContextSummary, messages[0].Content[0].Kind)
	assert.Contains(t, messages[0].Content[0].Text, `checkpoint_id="cp"`)
}

func compactionLineage(turns int) []domain.Message {
	messages := make([]domain.Message, 0, turns*2)
	for index := 0; index < turns; index++ {
		messages = append(messages,
			domain.Message{ID: "u-" + string(rune('0'+index)), Role: "user", CreatedAt: time.Unix(int64(index), 0),
				Parts: []domain.ContentBlock{{Kind: domain.ContentText, Text: strings.Repeat("user data ", 120)}}},
			domain.Message{ID: "a-" + string(rune('0'+index)), Role: "assistant", CreatedAt: time.Unix(int64(index), 0),
				Parts: []domain.ContentBlock{{Kind: domain.ContentText, Text: strings.Repeat("assistant data ", 120)}}},
		)
	}
	return messages
}

func validSummary(detail string) string {
	return "## Goal\n" + detail + "\n\n## Constraints & Preferences\nnone\n\n## Critical Data\nids\n\n" +
		"## Progress\n### Done\ndone\n### In Progress\nwork\n### Blocked\nnone\n\n" +
		"## Key Decisions\nkeep\n\n## Files & Artifacts\n/path\n\n## Next Steps\ncontinue"
}
