package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const (
	historyLookupMaxMessages = 20
	historyLookupMaxExcerpt  = 1024
	historyLookupMaxOutput   = 8 * 1024
)

type CompactedHistoryTool struct {
	mu         sync.RWMutex
	messages   []domain.Message
	checkpoint domain.ContextCompaction
	runLocal   []domain.Message
}

func NewCompactedHistoryTool(messages []domain.Message, checkpoint domain.ContextCompaction) *CompactedHistoryTool {
	return &CompactedHistoryTool{messages: append([]domain.Message(nil), messages...), checkpoint: checkpoint}
}

func (t *CompactedHistoryTool) UseCheckpoint(checkpoint domain.ContextCompaction) {
	t.mu.Lock()
	t.checkpoint = checkpoint
	t.mu.Unlock()
}

func (t *CompactedHistoryTool) UseRunLocal(runID string, generated []domain.ChatMessage, covered int) {
	if covered < 0 {
		covered = 0
	}
	if covered > len(generated) {
		covered = len(generated)
	}
	messages := make([]domain.Message, covered)
	for index := 0; index < covered; index++ {
		messages[index] = domain.Message{ID: fmt.Sprintf("run:%s:%d", runID, index+1),
			Role: string(generated[index].Role), Parts: append([]domain.ContentBlock(nil), generated[index].Content...),
			CreatedAt: time.Unix(0, int64(index)).UTC()}
	}
	t.mu.Lock()
	t.runLocal = messages
	t.mu.Unlock()
}

func (t *CompactedHistoryTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "search_compacted_history",
		Description: "Search safe exact details in canonical or current-Run messages covered by context compaction. Use only when the compacted summary lacks a necessary identifier, path, value, or decision. Raw tool-result bodies are never returned.",
		Parameters:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string"},"fromMessageId":{"type":"string"},"throughMessageId":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":20},"cursor":{"type":"string"}}}`),
		RiskClass:    domain.RiskReadOnly}
}

func (t *CompactedHistoryTool) ExecutionClass() domain.ExecutionClass {
	return domain.ExecutionReadOnly
}

func (t *CompactedHistoryTool) Execute(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var input struct {
		Query            string `json:"query"`
		FromMessageID    string `json:"fromMessageId"`
		ThroughMessageID string `json:"throughMessageId"`
		Limit            int    `json:"limit"`
		Cursor           string `json:"cursor"`
	}
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		return errorResult(call, fmt.Errorf("%s: invalid arguments", domain.ErrorHistoryLookupForbidden)), nil
	}
	messages := t.searchableMessages()
	if len(messages) == 0 {
		return errorResult(call, fmt.Errorf("%s: selected compaction source is unavailable", domain.ErrorHistoryLookupForbidden)), nil
	}
	start, end := 0, len(messages)-1
	if input.FromMessageID != "" {
		position := historyMessagePosition(messages, input.FromMessageID)
		if position < start || position > end {
			return errorResult(call, fmt.Errorf("%s: fromMessageId is outside compaction sources", domain.ErrorHistoryLookupOutOfRange)), nil
		}
		start = position
	}
	if input.ThroughMessageID != "" {
		position := historyMessagePosition(messages, input.ThroughMessageID)
		if position < start || position > end {
			return errorResult(call, fmt.Errorf("%s: throughMessageId is outside compaction sources", domain.ErrorHistoryLookupOutOfRange)), nil
		}
		end = position
	}
	limit := input.Limit
	if limit <= 0 || limit > historyLookupMaxMessages {
		limit = historyLookupMaxMessages
	}
	offset, err := decodeHistoryCursor(input.Cursor)
	if err != nil {
		return errorResult(call, fmt.Errorf("%s: invalid cursor", domain.ErrorHistoryLookupForbidden)), nil
	}

	type match struct {
		MessageID string   `json:"messageId"`
		Role      string   `json:"role"`
		CreatedAt string   `json:"createdAt"`
		Excerpt   string   `json:"excerpt"`
		Artifacts []string `json:"artifactIds,omitempty"`
	}
	query := strings.ToLower(strings.TrimSpace(input.Query))
	candidates := make([]match, 0)
	for index := start; index <= end; index++ {
		excerpt, artifacts := safeHistoryExcerpt(messages[index])
		if query != "" && !strings.Contains(strings.ToLower(excerpt), query) {
			continue
		}
		candidates = append(candidates, match{MessageID: messages[index].ID, Role: messages[index].Role,
			CreatedAt: messages[index].CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			Excerpt:   excerpt, Artifacts: artifacts})
	}
	if offset > len(candidates) {
		return errorResult(call, fmt.Errorf("%s: cursor is outside result set", domain.ErrorHistoryLookupOutOfRange)), nil
	}
	selected := make([]match, 0, limit)
	outputBytes := 0
	index := offset
	for index < len(candidates) && len(selected) < limit {
		encoded, _ := json.Marshal(candidates[index])
		if outputBytes+len(encoded) > historyLookupMaxOutput && len(selected) > 0 {
			break
		}
		selected = append(selected, candidates[index])
		outputBytes += len(encoded)
		index++
	}
	var nextCursor string
	if index < len(candidates) {
		nextCursor = encodeHistoryCursor(index)
	}
	payload, _ := json.Marshal(map[string]any{"matches": selected, "noMatch": len(selected) == 0,
		"truncated": nextCursor != "", "nextCursor": nextCursor})
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: string(payload)}, nil
}

func (t *CompactedHistoryTool) searchableMessages() []domain.Message {
	t.mu.RLock()
	defer t.mu.RUnlock()
	messages := make([]domain.Message, 0, len(t.messages)+len(t.runLocal))
	start, end := t.canonicalSourceBoundsLocked()
	if start >= 0 && end >= start {
		messages = append(messages, t.messages[start:end+1]...)
	}
	messages = append(messages, t.runLocal...)
	return messages
}

func (t *CompactedHistoryTool) canonicalSourceBoundsLocked() (int, int) {
	if t.checkpoint.SourceFromMessageID == nil || t.checkpoint.SourceThroughMessageID == nil {
		return -1, -1
	}
	return historyMessagePosition(t.messages, *t.checkpoint.SourceFromMessageID),
		historyMessagePosition(t.messages, *t.checkpoint.SourceThroughMessageID)
}

func historyMessagePosition(messages []domain.Message, id string) int {
	for index := range messages {
		if messages[index].ID == id {
			return index
		}
	}
	return -1
}

func safeHistoryExcerpt(message domain.Message) (string, []string) {
	var text strings.Builder
	var artifacts []string
	for _, block := range message.Parts {
		switch block.Kind {
		case domain.ContentText:
			text.WriteString(block.Text)
			text.WriteByte('\n')
		case domain.ContentToolCall:
			if block.ToolCall != nil {
				fmt.Fprintf(&text, "[tool call id=%s name=%s arguments=%s]\n", block.ToolCall.ID,
					block.ToolCall.Name, block.ToolCall.Arguments)
			}
		case domain.ContentToolResult:
			if block.ToolResult != nil {
				fmt.Fprintf(&text, "[tool result id=%s name=%s status=%s; body excluded]\n",
					block.ToolResult.ToolCallID, block.ToolResult.ToolName, func() string {
						if block.ToolResult.IsError {
							return "error"
						}
						return "completed"
					}())
				for _, artifact := range block.ToolResult.Artifacts {
					artifacts = append(artifacts, artifact.ArtifactID)
					fmt.Fprintf(&text, "[artifact id=%s name=%q kind=%s mime=%s size=%d]\n",
						artifact.ArtifactID, artifact.Name, artifact.Kind, artifact.MIMEType, artifact.SizeBytes)
				}
			}
		case domain.ContentImage:
			if block.Image != nil {
				artifacts = append(artifacts, block.Image.ArtifactID)
				fmt.Fprintf(&text, "[image artifact=%s mime=%s dimensions=%dx%d]\n", block.Image.ArtifactID,
					block.Image.MIMEType, block.Image.Width, block.Image.Height)
			}
		case domain.ContentImageDescription:
			if block.ImageDescription != nil {
				artifacts = append(artifacts, block.ImageDescription.ArtifactID)
				text.WriteString(block.ImageDescription.Text)
				text.WriteByte('\n')
			}
		}
	}
	return utf8Prefix(strings.TrimSpace(text.String()), historyLookupMaxExcerpt), artifacts
}

func utf8Prefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "\n[truncated]"
}

func encodeHistoryCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("offset:" + strconv.Itoa(offset)))
}

func decodeHistoryCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || !strings.HasPrefix(string(decoded), "offset:") {
		return 0, fmt.Errorf("invalid cursor")
	}
	offset, err := strconv.Atoi(strings.TrimPrefix(string(decoded), "offset:"))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return offset, nil
}
