package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareContextPreservesSystemAndLatestMessage(t *testing.T) {
	history := []domain.ChatMessage{
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: strings.Repeat("old ", 500)}}},
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: strings.Repeat("answer ", 500)}}},
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "latest request"}}},
	}
	prepared := PrepareContext("system contract", history, 100)
	require.GreaterOrEqual(t, len(prepared), 2)
	assert.Equal(t, domain.RoleSystem, prepared[0].Role)
	assert.Equal(t, "latest request", messageText(prepared[len(prepared)-1]))
	assert.LessOrEqual(t, EstimateTokens(prepared), 120)
}

func TestPrepareContextNeverSplitsToolExchange(t *testing.T) {
	call := domain.ToolCall{ID: "call-1", Name: "read"}
	result := domain.ToolResult{ToolCallID: "call-1", ToolName: "read", Content: strings.Repeat("result ", 100)}
	history := []domain.ChatMessage{
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: strings.Repeat("old ", 100)}}},
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentToolCall, ToolCall: &call}}},
		{Role: domain.RoleTool, Content: []domain.ContentBlock{{Kind: domain.ContentToolResult, ToolResult: &result}}},
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "latest request"}}},
	}
	prepared := PrepareContext("system", history, 50)
	var calls, results int
	for _, message := range prepared {
		for _, block := range message.Content {
			if block.ToolCall != nil {
				calls++
			}
			if block.ToolResult != nil {
				results++
			}
		}
	}
	assert.Equal(t, calls, results)
	assert.Contains(t, messageText(prepared[1]), "Earlier complete conversation turns")
	assert.Equal(t, "latest request", messageText(prepared[len(prepared)-1]))
}

func TestPrepareContextDoesNotTruncateLatestUserInput(t *testing.T) {
	latest := strings.Repeat("critical ", 200)
	prepared := PrepareContext("system", []domain.ChatMessage{{Role: domain.RoleUser,
		Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: latest}}}}, 10)
	assert.Equal(t, latest, messageText(prepared[len(prepared)-1]))
}

func TestBudgetToolResultKeepsUTF8HeadAndTail(t *testing.T) {
	content := strings.Repeat("前部数据", 1000) + "TAIL"
	result := BudgetToolResult(domain.ToolResult{Content: content}, 128)
	assert.True(t, utf8.ValidString(result.Content))
	assert.Contains(t, result.Content, "omitted")
	assert.True(t, strings.HasSuffix(result.Content, "TAIL"))
	assert.Less(t, len(result.Content), len(content))
}

func TestEstimateTokensIncreasesWithContent(t *testing.T) {
	short := []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "short"}}}}
	long := []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: strings.Repeat("long", 100)}}}}
	assert.Greater(t, EstimateTokens(long), EstimateTokens(short))
}

func TestBudgetToolResultNeverExceedsBudget(t *testing.T) {
	// Strict invariant: len(result.Content) <= budget even after marker insertion.
	for _, budget := range []int{64, 128, 256, 512, 1024, 4096, 16384} {
		content := strings.Repeat("0123456789", 5000) // ~50KB, well above any budget
		result := BudgetToolResult(domain.ToolResult{Content: content}, budget)
		assert.LessOrEqual(t, len(result.Content), budget,
			"budget=%d but result len=%d", budget, len(result.Content))
	}
}

func TestBudgetToolResultSmallBudget(t *testing.T) {
	// Budget smaller than marker → returns safe prefix only, no broken marker.
	content := strings.Repeat("hello world ", 100)
	result := BudgetToolResult(domain.ToolResult{Content: content}, 10)
	assert.True(t, utf8.ValidString(result.Content))
	// Budget 10 < marker ~90 bytes → should return a safe prefix.
	assert.LessOrEqual(t, len(result.Content), 10)
}

func TestBudgetToolResultOmittedCountIsCorrect(t *testing.T) {
	content := strings.Repeat("A", 2000) + "UNIQUE_TAIL"
	result := BudgetToolResult(domain.ToolResult{Content: content}, 500)
	// Head + tail should form the bulk of the budget.
	assert.Contains(t, result.Content, "AAAA")
	assert.Contains(t, result.Content, "UNIQUE_TAIL")
	assert.Contains(t, result.Content, "omitted")
	assert.LessOrEqual(t, len(result.Content), 500)
}
