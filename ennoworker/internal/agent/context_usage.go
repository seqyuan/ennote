package agent

import (
	"context"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// splitContextUsage computes the heuristic context-occupancy breakdown of a
// prepared request: the system prompt, the tool definitions, and the remaining
// conversation messages. ProjectedTokens is the full input estimate — the same
// figure the compaction planner uses — so a compaction that shrinks the
// message surface is reflected immediately on the next request, matching
// deepseek-harness's projectedTokens surface projection.
func splitContextUsage(messages []domain.ChatMessage, tools []domain.ToolDefinition) domain.SessionContextUsage {
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
	return domain.SessionContextUsage{
		SystemTokens:    systemTokens,
		ToolsTokens:     toolDefinitionTokens(tools),
		MessageTokens:   messageTokens,
		ProjectedTokens: EstimateComposition("", tools, messages, 0).InputTokens,
	}
}

// recordContextUsage appends the durable context_usage run event that feeds the
// client's context meter. It writes through the same run-event sink as the
// other loop events (context_pruned, output_truncated, …), so it is replayed on
// reconnect and persisted for hydration.
func (l *Loop) recordContextUsage(ctx context.Context, runID string, usage domain.SessionContextUsage) error {
	return l.appendEvent(ctx, runID, "context_usage", usage)
}
