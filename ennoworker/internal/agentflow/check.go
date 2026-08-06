package agentflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// CheckSessionKey carries the flow run's session id into the sandbox builder.
type CheckSessionKey struct{}

// WithCheckSession attaches the flow session to the check execution context.
func WithCheckSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, CheckSessionKey{}, sessionID)
}

// CheckPolicy is the frozen tool policy the check gate evaluates against.
// It mirrors the ToolPolicyConfig dimensions relevant to command execution.
type CheckPolicy struct {
	Mode               string
	AllowedTools       []string
	AllowedExecutables []string
	MaxTimeoutSeconds  int
}

// CheckDecisionAction is the gate verdict for a check command.
type CheckDecisionAction string

const (
	CheckAllow      CheckDecisionAction = "allow"
	CheckDeny       CheckDecisionAction = "deny"
	CheckRequireAsk CheckDecisionAction = "ask"
)

// CheckDecision is the fail-loud verdict of the policy gate.
type CheckDecision struct {
	Action CheckDecisionAction `json:"action"`
	Code   string              `json:"code,omitempty"`
	Reason string              `json:"reason,omitempty"`
}

// CheckOutcome is the typed check result written to the task checkpoint.
type CheckOutcome struct {
	Pass       bool   `json:"pass"`
	ExitCode   int    `json:"exitCode"`
	Summary    string `json:"summary"`
	Command    string `json:"command"`
	ApprovalID string `json:"approvalId,omitempty"`
}

// CheckApprovalStatus is the durable approval state of one check task.
type CheckApprovalStatus string

const (
	CheckApprovalNone     CheckApprovalStatus = "none"
	CheckApprovalPending  CheckApprovalStatus = "pending"
	CheckApprovalApproved CheckApprovalStatus = "approved"
	CheckApprovalRejected CheckApprovalStatus = "rejected"
)

// CheckRunner executes deterministic check gates through the same policy,
// approval, sandbox, and audit boundaries as tools — no bypass because a
// check is a flow node.
type CheckRunner interface {
	CheckPolicyForSession(ctx context.Context, sessionID string) (*CheckPolicy, error)
	CreateCheckApproval(ctx context.Context, runID string, taskIndex int, command string) error
	CheckApprovalStatus(ctx context.Context, runID string, taskIndex int) (CheckApprovalStatus, error)
	ExecuteCheck(ctx context.Context, command string, timeoutSeconds int) (*CheckOutcome, error)
}

// EvaluateCheck applies the frozen tool policy gate to a check command. The
// decision mirrors the builtin tool policy for exec-class tools: Discuss mode
// allows read-only only (deny), Ask mode requires approval, Auto /
// allow_existing_behavior allow within the executable allowlist.
func EvaluateCheck(policy *CheckPolicy, argv []string) CheckDecision {
	if policy == nil {
		return CheckDecision{Action: CheckDeny, Code: "policy_unavailable", Reason: "tool policy is unavailable"}
	}
	if len(argv) == 0 {
		return CheckDecision{Action: CheckDeny, Code: "check_command_required", Reason: "check command is empty"}
	}
	executable := argv[0]
	switch policy.Mode {
	case string(domain.PermissionDiscuss):
		return CheckDecision{Action: CheckDeny, Code: "permission_mode_discuss",
			Reason: "Discuss mode allows read-only tools only; check commands cannot run"}
	case string(domain.PermissionAsk):
		return CheckDecision{Action: CheckRequireAsk, Code: "permission_mode_ask",
			Reason: "Ask mode requires approval for this check command"}
	case string(domain.PermissionAuto):
		// Auto mode allows exec-class commands within the executable allowlist.
		if len(policy.AllowedExecutables) > 0 && !containsString(policy.AllowedExecutables, executable) {
			return CheckDecision{Action: CheckDeny, Code: "executable_not_allowed",
				Reason: fmt.Sprintf("executable %q is not allowed by policy", executable)}
		}
		return CheckDecision{Action: CheckAllow, Reason: "auto mode"}
	case "allow_existing_behavior":
		return CheckDecision{Action: CheckAllow, Reason: "allow_existing_behavior mode"}
	default:
		return CheckDecision{Action: CheckDeny, Code: "policy_mode_invalid",
			Reason: fmt.Sprintf("unsupported tool policy mode %q", policy.Mode)}
	}
}

// ParseCheckCommand splits a check command string into an argv vector with
// basic quoting support (single/double quotes and backslash escapes), so a
// check command can express structured shell invocations like
// `sh -c "test -f x && touch x"`. Unbalanced quotes keep the raw text.
func ParseCheckCommand(command string) []string {
	var argv []string
	var current strings.Builder
	inSingle, inDouble := false, false
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			argv = append(argv, current.String())
			current.Reset()
		}
	}
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		switch {
		case r == '\\' && !inSingle:
			escaped = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case (r == ' ' || r == '\t' || r == '\n') && !inSingle && !inDouble:
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	flush()
	return argv
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
