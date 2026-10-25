package agent

import (
	"context"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// Context category keys. The set is ennote-native: the platform base prompt,
// the frozen Role, project instruction files, Skills, tool schemas, ephemeral
// reminders, and the conversation itself. Delegated child Runs keep their own
// accounting and are deliberately not folded into the parent.
const (
	ContextCategorySystem       = "system"
	ContextCategoryRole         = "role"
	ContextCategoryRules        = "rules"
	ContextCategorySkills       = "skills"
	ContextCategoryTools        = "tools"
	ContextCategoryReminders    = "reminders"
	ContextCategoryConversation = "conversation"
	contextCategoriesVersion    = 1
)

// splitContextUsage computes the heuristic context-occupancy breakdown of a
// prepared request: the system prompt, the tool definitions, and the remaining
// conversation messages. ProjectedTokens is the full input estimate — the same
// figure the compaction planner uses — so a compaction that shrinks the
// message surface is reflected immediately on the next request, matching
// deepseek-harness's projectedTokens surface projection.
//
// The legacy system/tools/message fields are kept byte-for-byte compatible
// with earlier Workers. Categories are a parallel projection: they partition
// ProjectedTokens exactly, and are omitted when the caller has no frozen
// prompt sections to attribute.
func splitContextUsage(messages []domain.ChatMessage, tools []domain.ToolDefinition,
	sections []domain.PromptSection) domain.SessionContextUsage {
	base := EstimateComposition("", nil, nil, 0).InputTokens
	var systemMessages, otherMessages []domain.ChatMessage
	for _, message := range messages {
		if message.Role == domain.RoleSystem {
			systemMessages = append(systemMessages, message)
		} else {
			otherMessages = append(otherMessages, message)
		}
	}
	systemTokens := EstimateComposition("", nil, systemMessages, 0).InputTokens - base
	if systemTokens < 0 {
		systemTokens = 0
	}
	messageTokens := EstimateComposition("", nil, otherMessages, 0).InputTokens - base
	if messageTokens < 0 {
		messageTokens = 0
	}
	projected := EstimateComposition("", tools, messages, 0).InputTokens
	usage := domain.SessionContextUsage{
		SystemTokens:    systemTokens,
		ToolsTokens:     toolDefinitionTokens(tools),
		MessageTokens:   messageTokens,
		ProjectedTokens: projected,
	}
	if len(sections) > 0 {
		usage.Categories = contextCategories(messages, tools, sections, projected)
		usage.CategoriesVersion = contextCategoriesVersion
	}
	return usage
}

// contextCategories partitions the projected input across the seven ennote
// categories. The identity is enforced by construction:
//
//	system + role + rules + skills + tools + reminders + conversation == projected
//
// Section bytes decide the split inside the system prompt; the last bucket
// (system) absorbs the fixed estimate overhead and integer rounding.
func contextCategories(messages []domain.ChatMessage, tools []domain.ToolDefinition,
	sections []domain.PromptSection, projected int) []domain.ContextCategory {
	base := EstimateComposition("", nil, nil, 0).InputTokens
	var reminderMessages, conversationMessages []domain.ChatMessage
	for _, message := range messages {
		if message.Role == domain.RoleSystem {
			continue
		}
		if isSystemReminderMessage(message) {
			reminderMessages = append(reminderMessages, message)
		} else {
			conversationMessages = append(conversationMessages, message)
		}
	}
	toolsTokens := toolDefinitionTokens(tools)
	reminderTokens := messageContentTokens(reminderMessages, base)
	conversationTokens := messageContentTokens(conversationMessages, base)
	systemTotal := projected - toolsTokens - reminderTokens - conversationTokens
	if systemTotal < 0 {
		systemTotal = 0
	}

	weights := map[string]int{
		ContextCategorySystem: 0,
		ContextCategoryRole:   0,
		ContextCategoryRules:  0,
		ContextCategorySkills: 0,
	}
	for _, section := range sections {
		key := categoryForSection(section.Kind)
		if key == "" {
			continue
		}
		weights[key] += section.Bytes
	}
	if weights[ContextCategorySystem] == 0 && weights[ContextCategoryRole] == 0 &&
		weights[ContextCategoryRules] == 0 && weights[ContextCategorySkills] == 0 {
		weights[ContextCategorySystem] = 1
	}
	// System is last so it absorbs the fixed overhead and every rounding
	// remainder; the other three are proportional to their frozen bytes.
	order := []string{ContextCategoryRole, ContextCategoryRules, ContextCategorySkills, ContextCategorySystem}
	totalWeight := 0
	for _, key := range order {
		totalWeight += weights[key]
	}
	allocated := map[string]int{}
	remaining := systemTotal
	for index, key := range order {
		if index == len(order)-1 {
			allocated[key] = remaining
			break
		}
		share := 0
		if totalWeight > 0 {
			share = systemTotal * weights[key] / totalWeight
		}
		if share > remaining {
			share = remaining
		}
		allocated[key] = share
		remaining -= share
	}

	return []domain.ContextCategory{
		{Key: ContextCategorySystem, Tokens: allocated[ContextCategorySystem]},
		{Key: ContextCategoryRole, Tokens: allocated[ContextCategoryRole]},
		{Key: ContextCategoryRules, Tokens: allocated[ContextCategoryRules]},
		{Key: ContextCategorySkills, Tokens: allocated[ContextCategorySkills]},
		{Key: ContextCategoryTools, Tokens: toolsTokens},
		{Key: ContextCategoryReminders, Tokens: reminderTokens},
		{Key: ContextCategoryConversation, Tokens: conversationTokens},
	}
}

func categoryForSection(kind domain.PromptSectionKind) string {
	switch kind {
	case domain.PromptSectionBase:
		return ContextCategorySystem
	case domain.PromptSectionRole:
		return ContextCategoryRole
	case domain.PromptSectionContext:
		return ContextCategoryRules
	case domain.PromptSectionSkillsCatalog, domain.PromptSectionSkillPreload:
		return ContextCategorySkills
	default:
		return ""
	}
}

func messageContentTokens(messages []domain.ChatMessage, base int) int {
	if len(messages) == 0 {
		return 0
	}
	tokens := EstimateComposition("", nil, messages, 0).InputTokens - base
	if tokens < 0 {
		return 0
	}
	return tokens
}

func isSystemReminderMessage(message domain.ChatMessage) bool {
	if message.Role != domain.RoleUser || len(message.Content) != 1 {
		return false
	}
	block := message.Content[0]
	return block.Kind == domain.ContentText && strings.HasPrefix(block.Text, "<system-reminder>")
}

// recordContextUsage appends the durable context_usage run event that feeds the
// client's context meter. It writes through the same run-event sink as the
// other loop events (context_pruned, output_truncated, …), so it is replayed on
// reconnect and persisted for hydration.
func (l *Loop) recordContextUsage(ctx context.Context, runID string, usage domain.SessionContextUsage) error {
	return l.appendEvent(ctx, runID, "context_usage", usage)
}
