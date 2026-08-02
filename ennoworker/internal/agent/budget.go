package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const defaultToolResultBudget = 16 * 1024

func EstimateTokens(messages []domain.ChatMessage) int {
	bytes := 0
	for _, message := range messages {
		bytes += len(message.Role) + 8
		for _, block := range message.Content {
			bytes += len(block.Text) + 8
			if block.ToolCall != nil {
				bytes += len(block.ToolCall.Name) + len(block.ToolCall.Arguments) + 16
			}
			if block.ToolResult != nil {
				bytes += len(block.ToolResult.Content) + len(block.ToolResult.ToolName) + 16
				for _, artifact := range block.ToolResult.Artifacts {
					bytes += len(artifact.ArtifactID) + len(artifact.Name) + len(artifact.MIMEType) + 24
				}
			}
		}
	}
	return (bytes + 3) / 4
}

func PrepareContext(systemPrompt string, history []domain.ChatMessage, maxTokens int) []domain.ChatMessage {
	var system []domain.ChatMessage
	if systemPrompt != "" {
		system = append(system, domain.ChatMessage{
			Role:    domain.RoleSystem,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: systemPrompt}},
		})
	}
	messages := append(append([]domain.ChatMessage(nil), system...), history...)
	if maxTokens <= 0 || EstimateTokens(messages) <= maxTokens {
		return messages
	}

	units := contextUnits(history)
	protectedUnit := -1
	for index, unit := range units {
		for _, message := range unit {
			if message.Role == domain.RoleUser {
				protectedUnit = index
			}
		}
	}
	removed := false
	for len(units) > 0 && EstimateTokens(joinContext(system, units, removed)) > maxTokens {
		if protectedUnit == 0 {
			break
		}
		units = units[1:]
		if protectedUnit > 0 {
			protectedUnit--
		}
		removed = true
	}
	return joinContext(system, units, removed)
}

func contextUnits(history []domain.ChatMessage) [][]domain.ChatMessage {
	units := make([][]domain.ChatMessage, 0, len(history))
	for index := 0; index < len(history); {
		message := history[index]
		unit := []domain.ChatMessage{message}
		index++
		if message.Role == domain.RoleAssistant && messageHasToolCalls(message) {
			for index < len(history) && history[index].Role == domain.RoleTool {
				unit = append(unit, history[index])
				index++
			}
		}
		units = append(units, unit)
	}
	return units
}

func joinContext(system []domain.ChatMessage, units [][]domain.ChatMessage, truncated bool) []domain.ChatMessage {
	count := len(system)
	for _, unit := range units {
		count += len(unit)
	}
	if truncated {
		count++
	}
	messages := make([]domain.ChatMessage, 0, count)
	messages = append(messages, system...)
	if truncated {
		messages = append(messages, domain.ChatMessage{Role: domain.RoleSystem, Content: []domain.ContentBlock{{
			Kind: domain.ContentText, Text: "[Earlier complete conversation turns were truncated to fit the context window.]",
		}}})
	}
	for _, unit := range units {
		messages = append(messages, unit...)
	}
	return messages
}

func messageHasToolCalls(message domain.ChatMessage) bool {
	for _, block := range message.Content {
		if block.Kind == domain.ContentToolCall && block.ToolCall != nil {
			return true
		}
	}
	return false
}

func BudgetToolResult(result domain.ToolResult, budget int) domain.ToolResult {
	if budget <= 0 {
		budget = defaultToolResultBudget
	}
	if len(result.Content) <= budget {
		return result
	}
	// Reserve worst-case marker bytes so the final len(Content) <= budget.
	// The marker includes a variable digit count; reserve 8 extra bytes for
	// up to "99999999" (80 MB) omitted.
	markerBase := len("\n[...  bytes omitted; use a narrower command or read the saved output ...]\n")
	markerReserved := markerBase + 8
	avail := budget - markerReserved
	if avail < 4 {
		// Budget is too small for a meaningful marker; return a safe prefix.
		result.Content = validPrefix(result.Content, budget)
		return result
	}
	half := avail / 2
	head := validPrefix(result.Content, half)
	tail := validSuffix(result.Content, half)
	omitted := len(result.Content) - len(head) - len(tail)
	marker := fmt.Sprintf("\n[... %d bytes omitted; use a narrower command or read the saved output ...]\n", omitted)
	result.Content = head + marker + tail
	return result
}

func truncateMessage(message domain.ChatMessage, budget int) []domain.ContentBlock {
	if budget < 64 {
		budget = 64
	}
	blocks := append([]domain.ContentBlock(nil), message.Content...)
	for index := range blocks {
		if len(blocks[index].Text) > budget {
			blocks[index].Text = validPrefix(blocks[index].Text, budget) + "\n[truncated to context budget]"
		}
		if blocks[index].ToolResult != nil && len(blocks[index].ToolResult.Content) > budget {
			value := BudgetToolResult(*blocks[index].ToolResult, budget)
			blocks[index].ToolResult = &value
		}
	}
	return blocks
}

func validPrefix(value string, bytes int) string {
	if len(value) <= bytes {
		return value
	}
	value = value[:bytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func validSuffix(value string, bytes int) string {
	if len(value) <= bytes {
		return value
	}
	value = value[len(value)-bytes:]
	for !utf8.ValidString(value) {
		_, size := utf8.DecodeRuneInString(value)
		if size == 0 {
			break
		}
		value = value[size:]
	}
	return value
}

func messageText(message domain.ChatMessage) string {
	var builder strings.Builder
	for _, block := range message.Content {
		builder.WriteString(block.Text)
	}
	return builder.String()
}
