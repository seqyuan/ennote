package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const (
	approvalPreviewLimit             = 400
	delegationAssignmentPreviewLimit = 240
)

var secretKeyPattern = regexp.MustCompile(`(?i)(api.?key|authorization|credential|password|secret|token)`)

const ResumeStateVersion = 5

// ApprovalDigestVersion tracks the hash algorithm used for batch digest.
// V1 = legacy (no RuleID), V2 = includes StandingRuleID.
const (
	ApprovalDigestV1 = 1
	ApprovalDigestV2 = 2
)

type ResumeState struct {
	Version                int                                    `json:"version"`
	ApprovalDigestVersion  int                                    `json:"approvalDigestVersion,omitempty"`
	Iteration              int                                    `json:"iteration"`
	Messages               []domain.ChatMessage                   `json:"messages"`
	Generated              []domain.ChatMessage                   `json:"generated"`
	Completion             domain.Completion                      `json:"completion"`
	Current                domain.ModelRuntimeSnapshot            `json:"current"`
	Routing                domain.FrozenRoutingConfig             `json:"routing"`
	RequestGeneration      int                                    `json:"requestGeneration"`
	TruncationRecoveries   int                                    `json:"truncationRecoveries"`
	StuckSignatures        []string                               `json:"stuckSignatures"`
	InitialSteering        []domain.ChatMessage                   `json:"initialSteering"`
	SystemPrompt           string                                 `json:"systemPrompt"`
	MidRunCompaction       MidRunCompactionState                  `json:"midRunCompaction"`
	Todos                  []domain.TodoItem                      `json:"todos,omitempty"`
	SkillCatalogState      string                                 `json:"skillCatalogState,omitempty"`
	SkillCatalogDigest     string                                 `json:"skillCatalogDigest,omitempty"`
	StandingAuthorizations []domain.StandingAuthorizationSnapshot `json:"standingAuthorizations,omitempty"`
}

type ApprovalResolution struct {
	Decision                 domain.ApprovalDecision `json:"decision"`
	BatchDigest              string                  `json:"batchDigest"`
	StandingGrantCallIndexes []int                   `json:"standingGrantCallIndexes,omitempty"`
}

type ApprovalRequiredError struct {
	BatchDigest            string                                 `json:"batchDigest"`
	ApprovalDigestVersion  int                                    `json:"approvalDigestVersion"`
	Items                  []domain.ApprovalItem                  `json:"items"`
	StandingCandidates     []domain.StandingGrantCandidate        `json:"standingCandidates,omitempty"`
	StandingAuthorizations []domain.StandingAuthorizationSnapshot `json:"standingAuthorizations,omitempty"`
	State                  ResumeState                            `json:"state"`
}

func (e *ApprovalRequiredError) Error() string { return "tool batch requires approval" }

type approvalPreviewer interface {
	ApprovalPreview(domain.ToolCall) string
}

type delegationApprovalPreviewer interface {
	DelegationApprovalPreview(domain.ToolCall) []domain.DelegationApprovalPreview
}

func approvalBatchDigest(plans []plannedToolCall, policy domain.PolicySnapshot, digestVersion int) string {
	hash := sha256.New()
	writeArguments := func(arguments json.RawMessage) {
		if digestVersion == ApprovalDigestV2 {
			var compact bytes.Buffer
			if err := json.Compact(&compact, arguments); err == nil {
				hash.Write(compact.Bytes())
				return
			}
		}
		hash.Write(arguments)
	}
	fmt.Fprintf(hash, "%s\x00%d\x00", policy.ID, policy.Version)
	for index, plan := range plans {
		fmt.Fprintf(hash, "%d\x00%s\x00%s\x00", index, plan.original.ID, plan.original.Name)
		writeArguments(plan.original.Arguments)
		hash.Write([]byte{0})
		writeArguments(plan.effective.Arguments)
		switch digestVersion {
		case ApprovalDigestV1:
			fmt.Fprintf(hash, "\x00%s\x00%s\xff", plan.decision.Action, plan.decision.RiskClass)
		case ApprovalDigestV2:
			fmt.Fprintf(hash, "\x00%s\x00%s\x00%s\xff", plan.decision.Action, plan.decision.RiskClass, plan.decision.RuleID)
		default:
			// Fall back to V1 for zero-value (legacy) states.
			fmt.Fprintf(hash, "\x00%s\x00%s\xff", plan.decision.Action, plan.decision.RiskClass)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// standingAuthorizationSnapshot freezes which calls in a batch were covered by
// standing rules. It runs over ALL plans (not only require_approval ones):
// calls already released by the standing gate carry a non-empty RuleID.
func standingAuthorizationSnapshot(plans []plannedToolCall) []domain.StandingAuthorizationSnapshot {
	var snapshots []domain.StandingAuthorizationSnapshot
	for index, plan := range plans {
		if plan.decision.RuleID == "" {
			continue
		}
		snapshots = append(snapshots, domain.StandingAuthorizationSnapshot{
			CallIndex:    index,
			ToolCallID:   plan.effective.ID,
			ToolName:     plan.effective.Name,
			ScopeKind:    plan.decision.StandingScopeKind,
			ScopeVersion: plan.decision.StandingScopeVersion,
			ScopeKey:     plan.decision.StandingScopeKey,
			RuleID:       plan.decision.RuleID,
		})
	}
	return snapshots
}

func approvalItems(plans []plannedToolCall, policy ToolPolicy) []domain.ApprovalItem {
	items := make([]domain.ApprovalItem, 0)
	previewer, _ := policy.(approvalPreviewer)
	delegationPreviewer, _ := policy.(delegationApprovalPreviewer)
	for index, plan := range plans {
		if !plan.requiresApproval {
			continue
		}
		preview := genericApprovalPreview(plan.effective.Arguments)
		if previewer != nil {
			preview = previewer.ApprovalPreview(plan.effective)
		}
		item := domain.ApprovalItem{CallIndex: index, ToolCallID: plan.original.ID,
			ToolName: plan.effective.Name, RiskClass: plan.decision.RiskClass, ArgumentsPreview: preview}
		if delegationPreviewer != nil && plan.effective.Name == "delegate_roles" {
			item.Delegations = delegationPreviewer.DelegationApprovalPreview(plan.effective)
		}
		items = append(items, item)
	}
	return items
}

// attachStandingScopes decorates approval items with the safe standing-scope
// summary for eligible calls, matching the dispatcher's candidate resolution.
func attachStandingScopes(items []domain.ApprovalItem, plans []plannedToolCall,
	resolver domain.StandingApprovalScopeResolver) {
	if resolver == nil {
		return
	}
	for i := range items {
		index := items[i].CallIndex
		if index < 0 || index >= len(plans) {
			continue
		}
		plan := plans[index]
		if plan.decision.RiskClass != domain.RiskExternal {
			continue
		}
		scope, ok, err := resolver.ResolveStandingApprovalScope(plan.effective.Name, plan.effective.Arguments)
		if err != nil || !ok {
			continue
		}
		items[i].StandingScope = &domain.StandingScopeInfo{
			Kind:         scope.Kind,
			ScopeVersion: scope.ScopeVersion,
			Display:      scope.Display,
		}
	}
}

func (p *BuiltinToolPolicy) ApprovalPreview(call domain.ToolCall) string {
	preview := genericApprovalPreview(call.Arguments)
	for _, pattern := range p.redact {
		preview = pattern.ReplaceAllString(preview, "[REDACTED]")
	}
	return truncateApprovalPreview(preview)
}

func (p *BuiltinToolPolicy) DelegationApprovalPreview(call domain.ToolCall) []domain.DelegationApprovalPreview {
	var input struct {
		Delegations []struct {
			Name           string                   `json:"name"`
			RoleHandle     string                   `json:"roleHandle"`
			Assignment     string                   `json:"assignment"`
			OutputContract string                   `json:"outputContract"`
			Budget         domain.BudgetCeilingJSON `json:"budget"`
		} `json:"delegations"`
	}
	if call.Name != "delegate_roles" || json.Unmarshal(call.Arguments, &input) != nil {
		return nil
	}
	previews := make([]domain.DelegationApprovalPreview, 0, len(input.Delegations))
	for _, delegation := range input.Delegations {
		assignment := delegation.Assignment
		for _, pattern := range p.redact {
			assignment = pattern.ReplaceAllString(assignment, "[REDACTED]")
		}
		outputContract := delegation.OutputContract
		if outputContract == "" {
			outputContract = "text-v1"
		}
		previews = append(previews, domain.DelegationApprovalPreview{
			Name: delegation.Name, RoleHandle: delegation.RoleHandle,
			AssignmentPreview: truncateApprovalRunes(assignment, delegationAssignmentPreviewLimit),
			OutputContract:    outputContract, Budget: delegation.Budget,
		})
	}
	return previews
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
	return truncateApprovalRunes(value, approvalPreviewLimit)
}

func truncateApprovalRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-3]) + "..."
}
