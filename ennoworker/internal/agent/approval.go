package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const approvalPreviewLimit = 400

var secretKeyPattern = regexp.MustCompile(`(?i)(api.?key|authorization|credential|password|secret|token)`)

const ResumeStateVersion = 2

type ResumeState struct {
	Version              int                         `json:"version"`
	Iteration            int                         `json:"iteration"`
	Messages             []domain.ChatMessage        `json:"messages"`
	Generated            []domain.ChatMessage        `json:"generated"`
	Completion           domain.Completion           `json:"completion"`
	Current              domain.ModelRuntimeSnapshot `json:"current"`
	Routing              domain.FrozenRoutingConfig  `json:"routing"`
	RequestGeneration    int                         `json:"requestGeneration"`
	TruncationRecoveries int                         `json:"truncationRecoveries"`
	StuckSignatures      []string                    `json:"stuckSignatures"`
	InitialSteering      []domain.ChatMessage        `json:"initialSteering"`
	SystemPrompt         string                      `json:"systemPrompt"`
	MidRunCompaction     MidRunCompactionState       `json:"midRunCompaction"`
}

type ApprovalResolution struct {
	Decision    domain.ApprovalDecision `json:"decision"`
	BatchDigest string                  `json:"batchDigest"`
}

type ApprovalRequiredError struct {
	BatchDigest string                `json:"batchDigest"`
	Items       []domain.ApprovalItem `json:"items"`
	State       ResumeState           `json:"state"`
}

func (e *ApprovalRequiredError) Error() string { return "tool batch requires approval" }

type approvalPreviewer interface {
	ApprovalPreview(domain.ToolCall) string
}

func approvalBatchDigest(plans []plannedToolCall, policy domain.PolicySnapshot) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%d\x00", policy.ID, policy.Version)
	for index, plan := range plans {
		fmt.Fprintf(hash, "%d\x00%s\x00%s\x00", index, plan.original.ID, plan.original.Name)
		hash.Write(plan.original.Arguments)
		hash.Write([]byte{0})
		hash.Write(plan.effective.Arguments)
		fmt.Fprintf(hash, "\x00%s\x00%s\xff", plan.decision.Action, plan.decision.RiskClass)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func approvalItems(plans []plannedToolCall, policy ToolPolicy) []domain.ApprovalItem {
	items := make([]domain.ApprovalItem, 0)
	previewer, _ := policy.(approvalPreviewer)
	for index, plan := range plans {
		if !plan.requiresApproval {
			continue
		}
		preview := genericApprovalPreview(plan.effective.Arguments)
		if previewer != nil {
			preview = previewer.ApprovalPreview(plan.effective)
		}
		items = append(items, domain.ApprovalItem{CallIndex: index, ToolCallID: plan.original.ID,
			ToolName: plan.effective.Name, RiskClass: plan.decision.RiskClass, ArgumentsPreview: preview})
	}
	return items
}

func (p *BuiltinToolPolicy) ApprovalPreview(call domain.ToolCall) string {
	preview := genericApprovalPreview(call.Arguments)
	for _, pattern := range p.redact {
		preview = pattern.ReplaceAllString(preview, "[REDACTED]")
	}
	return truncateApprovalPreview(preview)
}

func genericApprovalPreview(raw json.RawMessage) string {
	if len(raw) == 0 || !json.Valid(raw) {
		return "{}"
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "{}"
	}
	value = redactApprovalValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return truncateApprovalPreview(string(encoded))
}

func redactApprovalValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		redacted := make(map[string]any, len(typed))
		for _, key := range keys {
			if secretKeyPattern.MatchString(key) {
				redacted[key] = "[REDACTED]"
			} else {
				redacted[key] = redactApprovalValue(typed[key])
			}
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index := range typed {
			redacted[index] = redactApprovalValue(typed[index])
		}
		return redacted
	default:
		return value
	}
}

func truncateApprovalPreview(value string) string {
	if len(value) <= approvalPreviewLimit {
		return value
	}
	return value[:approvalPreviewLimit-3] + "..."
}
