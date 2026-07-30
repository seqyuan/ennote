package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompactedHistoryLookupEnforcesBoundsAndExcludesResultBodies(t *testing.T) {
	from, through := "m1", "m2"
	messages := []domain.Message{
		{ID: "m1", Role: "user", CreatedAt: time.Unix(1, 0), Parts: []domain.ContentBlock{{Kind: domain.ContentText, Text: "sample S-100 at /data/input"}}},
		{ID: "m2", Role: "tool", CreatedAt: time.Unix(2, 0), Parts: []domain.ContentBlock{{Kind: domain.ContentToolResult,
			ToolResult: &domain.ToolResult{ToolCallID: "call", ToolName: "read", Content: "RAW-SECRET-RESULT"}}}},
		{ID: "m3", Role: "user", CreatedAt: time.Unix(3, 0), Parts: []domain.ContentBlock{{Kind: domain.ContentText, Text: "outside source"}}},
	}
	tool := NewCompactedHistoryTool(messages, domain.ContextCompaction{
		SourceFromMessageID: &from, SourceThroughMessageID: &through,
	})
	assert.Equal(t, domain.ExecutionReadOnly, tool.ExecutionClass())

	result := tool.Execute(context.Background(), domain.ToolCall{ID: "lookup", Name: "search_compacted_history",
		Arguments: json.RawMessage(`{"query":"S-100"}`)})
	require.False(t, result.IsError)
	assert.Contains(t, result.Content, "S-100")
	assert.NotContains(t, result.Content, "RAW-SECRET-RESULT")

	bodyExcluded := tool.Execute(context.Background(), domain.ToolCall{ID: "lookup2", Name: "search_compacted_history",
		Arguments: json.RawMessage(`{"query":"tool result"}`)})
	assert.Contains(t, bodyExcluded.Content, "body excluded")
	assert.NotContains(t, bodyExcluded.Content, "RAW-SECRET-RESULT")

	outside := tool.Execute(context.Background(), domain.ToolCall{ID: "lookup3", Name: "search_compacted_history",
		Arguments: json.RawMessage(`{"fromMessageId":"m3"}`)})
	assert.True(t, outside.IsError)
	assert.Contains(t, outside.Content, string(domain.ErrorHistoryLookupOutOfRange))
}

func TestCompactedHistoryLookupIncludesSafeRunLocalMessages(t *testing.T) {
	tool := NewCompactedHistoryTool(nil, domain.ContextCompaction{})
	call := domain.ToolCall{ID: "run-call", Name: "shell", Arguments: json.RawMessage(`{"command":"analyze --sample S-42"}`)}
	result := domain.ToolResult{ToolCallID: "run-call", ToolName: "shell",
		Content: "RAW-RUN-RESULT-SECRET", Artifacts: []domain.ArtifactReference{{
			ArtifactID: "artifact-42", Name: "result.csv", Kind: "table", MIMEType: "text/csv", SizeBytes: 42,
		}}}
	generated := []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentToolCall, ToolCall: &call}}},
		{Role: domain.RoleTool, Content: []domain.ContentBlock{{Kind: domain.ContentToolResult, ToolResult: &result}}},
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "not covered yet"}}},
	}
	tool.UseRunLocal("run-1", generated, 2)

	found := tool.Execute(context.Background(), domain.ToolCall{ID: "lookup", Name: "search_compacted_history",
		Arguments: json.RawMessage(`{"query":"S-42"}`)})
	require.False(t, found.IsError)
	assert.Contains(t, found.Content, "run:run-1:1")
	assert.Contains(t, found.Content, "S-42")

	artifact := tool.Execute(context.Background(), domain.ToolCall{ID: "lookup2", Name: "search_compacted_history",
		Arguments: json.RawMessage(`{"query":"artifact-42"}`)})
	require.False(t, artifact.IsError)
	assert.Contains(t, artifact.Content, "artifact-42")
	assert.Contains(t, artifact.Content, "body excluded")
	assert.NotContains(t, artifact.Content, "RAW-RUN-RESULT-SECRET")
	assert.NotContains(t, artifact.Content, "not covered yet")
}

func TestCompactedHistoryLookupCapsUTF8OutputAndPaginates(t *testing.T) {
	from, through := "m0", "m24"
	messages := make([]domain.Message, 25)
	for index := range messages {
		messages[index] = domain.Message{ID: "m" + string(rune('0'+index)), Role: "user", CreatedAt: time.Unix(int64(index), 0),
			Parts: []domain.ContentBlock{{Kind: domain.ContentText, Text: strings.Repeat("数据", 800)}}}
	}
	messages[24].ID = through
	tool := NewCompactedHistoryTool(messages, domain.ContextCompaction{SourceFromMessageID: &from, SourceThroughMessageID: &through})
	result := tool.Execute(context.Background(), domain.ToolCall{ID: "lookup", Name: "search_compacted_history",
		Arguments: json.RawMessage(`{"limit":20}`)})
	require.False(t, result.IsError)
	assert.LessOrEqual(t, len(result.Content), historyLookupMaxOutput+historyLookupMaxExcerpt)
	assert.Contains(t, result.Content, `"truncated":true`)
	assert.Contains(t, result.Content, `"nextCursor"`)
}
