package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTodoToolStoresAndReplacesWholeList(t *testing.T) {
	store := domain.NewTodoStore()
	tool := &TodoTool{Store: store}

	first, err := tool.Execute(context.Background(), domain.ToolCall{ID: "c1", Name: "todo",
		Arguments: json.RawMessage(`{"todos":[{"content":"step one","status":"in_progress"},{"content":"step two","status":"pending"}]}`)})
	require.NoError(t, err)
	require.False(t, first.IsError, first.Content)
	assert.Contains(t, first.Content, "[~] step one")
	assert.Contains(t, first.Content, "(0/2 completed)")

	assert.Len(t, store.Snapshot(), 2)

	second, err := tool.Execute(context.Background(), domain.ToolCall{ID: "c2", Name: "todo",
		Arguments: json.RawMessage(`{"todos":[{"content":"done","status":"completed"}]}`)})
	require.NoError(t, err)
	require.False(t, second.IsError, second.Content)
	require.Len(t, store.Snapshot(), 1)
	assert.Equal(t, "done", store.Snapshot()[0].Content)
}

func TestTodoToolClearsWithEmptyList(t *testing.T) {
	store := domain.NewTodoStore()
	store.Set([]domain.TodoItem{{Content: "existing", Status: domain.TodoPending}})
	tool := &TodoTool{Store: store}

	result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "c", Name: "todo",
		Arguments: json.RawMessage(`{"todos":[]}`)})
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)
	assert.Empty(t, store.Snapshot())
	assert.Contains(t, result.Content, "(no tasks)")
}

func TestTodoToolRejectsInvalidListWithoutMutation(t *testing.T) {
	store := domain.NewTodoStore()
	store.Set([]domain.TodoItem{{Content: "existing", Status: domain.TodoPending}})
	tool := &TodoTool{Store: store}

	tests := []struct {
		name      string
		arguments string
		wantErr   string
	}{
		{"malformed json", `not json`, "invalid todo arguments"},
		{"blank content", `{"todos":[{"content":"  ","status":"pending"}]}`, "empty content"},
		{"invalid status", `{"todos":[{"content":"x","status":"done"}]}`, "invalid status"},
		{"too many items", buildTodoArgs(domain.MaxTodoItems + 1), "at most"},
		{"content too long", buildLongContentArgs(t), "exceeds"},
		{"two in_progress", `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`, "in_progress"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "bad", Name: "todo",
				Arguments: json.RawMessage(tt.arguments)})
			require.NoError(t, err)
			assert.True(t, result.IsError, "expected error for %q", tt.name)
			assert.Contains(t, result.Content, tt.wantErr, "error message mismatch for %q", tt.name)
			assert.Equal(t, "existing", store.Snapshot()[0].Content, "store mutated for %q", tt.name)
		})
	}
}

func buildTodoArgs(n int) string {
	items := make([]string, n)
	for i := 0; i < n; i++ {
		items[i] = `{"content":"item","status":"pending"}`
	}
	return `{"todos":[` + strings.Join(items, ",") + `]}`
}

func buildLongContentArgs(t *testing.T) string {
	t.Helper()
	long := strings.Repeat("x", domain.MaxTodoContentRunes+1)
	data, err := json.Marshal(struct {
		Todos []domain.TodoItem `json:"todos"`
	}{Todos: []domain.TodoItem{{Content: long, Status: domain.TodoPending}}})
	require.NoError(t, err)
	return string(data)
}

func TestTodoToolRejectsNilStore(t *testing.T) {
	result, err := (&TodoTool{}).Execute(context.Background(), domain.ToolCall{ID: "c", Name: "todo",
		Arguments: json.RawMessage(`{"todos":[{"content":"a","status":"pending"}]}`)})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "store")
}

func TestTodoToolDefinitionAndExecutionClass(t *testing.T) {
	tool := &TodoTool{Store: domain.NewTodoStore()}
	def := tool.Definition()
	assert.Equal(t, "todo", def.Name)
	assert.Contains(t, def.Description, "ENTIRE list")
	params := string(def.Parameters)
	assert.Contains(t, params, `"maxItems": 50`)
	assert.Contains(t, params, "in_progress")
	assert.Equal(t, domain.ExecutionExclusive, tool.ExecutionClass())
}

func TestTodoToolSchemaCompilesThroughRegistry(t *testing.T) {
	_, err := NewRegistry(&TodoTool{Store: domain.NewTodoStore()})
	require.NoError(t, err)
}

// compile-time check that MaxTodoContentRunes matches utf8 counting expectations.
func TestMaxTodoContentRunesConstant(t *testing.T) {
	assert.Equal(t, 500, domain.MaxTodoContentRunes)
	assert.True(t, utf8.ValidString(strings.Repeat("é", domain.MaxTodoContentRunes)))
}
