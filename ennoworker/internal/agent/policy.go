package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"mvdan.cc/sh/v3/syntax"
)

type BuiltinToolPolicy struct {
	snapshot domain.PolicySnapshot
	config   domain.ToolPolicyConfig
	risk     domain.ToolRiskClassifier
	redact   []*regexp.Regexp
}

// NewBuiltinToolPolicy builds the built-in tool policy from a frozen policy
// snapshot and the current Run's effective tool registry. risk is required:
// nil causes a construction error so policy evaluation can never panic.
func NewBuiltinToolPolicy(snapshot domain.PolicySnapshot, risk domain.ToolRiskClassifier) (*BuiltinToolPolicy, error) {
	if risk == nil {
		return nil, fmt.Errorf("tool risk classifier is required")
	}
	var config domain.ToolPolicyConfig
	if err := json.Unmarshal(snapshot.Config, &config); err != nil {
		return nil, fmt.Errorf("decode tool policy: %w", err)
	}
	policy := &BuiltinToolPolicy{snapshot: snapshot, config: config, risk: risk}
	for _, pattern := range config.RedactPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("compile redaction pattern: %w", err)
		}
		policy.redact = append(policy.redact, re)
	}
	return policy, nil
}

func (p *BuiltinToolPolicy) BeforeToolBatch(_ context.Context, _ ToolBatchContext, calls []domain.ToolCall) ([]ToolDecision, error) {
	decisions := make([]ToolDecision, len(calls))
	for index, call := range calls {
		risk := p.risk.RiskClass(call.Name)
		decisions[index] = ToolDecision{Action: ToolAllow, RiskClass: risk}
		if p.config.Mode == string(domain.PermissionDiscuss) && risk != domain.RiskReadOnly {
			decisions[index] = denyDecision("permission_mode_discuss", "Discuss mode allows read-only tools only")
			decisions[index].RiskClass = risk
			continue
		}
		if p.config.Mode == string(domain.PermissionAsk) && risk == domain.RiskSensitive {
			decisions[index] = denyDecision("permission_mode_sensitive", "Sensitive or unknown tools cannot be approved")
			decisions[index].RiskClass = risk
			continue
		}
		if p.config.Mode == "allow_existing_behavior" {
			continue
		}
		if !containsString(p.config.AllowedTools, call.Name) && len(p.config.AllowedTools) > 0 {
			decisions[index] = denyDecision("tool_not_allowed", "tool is not allowed by policy")
			decisions[index].RiskClass = risk
			continue
		}
		var err error
		switch call.Name {
		case "exec":
			err = p.validateExec(call.Arguments)
		case "bash":
			err = p.validateBash(call.Arguments)
		}
		if err != nil {
			decisions[index] = denyDecision("process_not_allowed", err.Error())
			decisions[index].RiskClass = risk
			continue
		}
		if p.config.Mode == string(domain.PermissionAsk) && risk != domain.RiskReadOnly {
			decisions[index] = ToolDecision{Action: ToolRequireApproval, Code: "permission_mode_ask",
				Reason: "Ask mode requires approval for this tool", RiskClass: risk}
		}
	}
	return decisions, nil
}

func (p *BuiltinToolPolicy) AfterToolCall(_ context.Context, _ ToolCallContext, _ domain.ToolCall, result domain.ToolResult) (AfterToolDecision, error) {
	projected := result
	for _, pattern := range p.redact {
		projected.Content = pattern.ReplaceAllString(projected.Content, "[REDACTED]")
	}
	return AfterToolDecision{Result: projected}, nil
}

func (p *BuiltinToolPolicy) validateExec(raw json.RawMessage) error {
	var args struct {
		Argv           []string `json:"argv"`
		TimeoutSeconds int      `json:"timeoutSeconds"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return err
	}
	if len(args.Argv) == 0 {
		return fmt.Errorf("argv is empty")
	}
	if err := p.validateCommand(args.Argv); err != nil {
		return err
	}
	return p.validateTimeout(args.TimeoutSeconds)
}

func (p *BuiltinToolPolicy) validateBash(raw json.RawMessage) error {
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return err
	}
	if err := p.validateTimeout(args.TimeoutSeconds); err != nil {
		return err
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(args.Command), "")
	if err != nil {
		return fmt.Errorf("parse shell command: %w", err)
	}
	var validationErr error
	syntax.Walk(file, func(node syntax.Node) bool {
		if validationErr != nil {
			return false
		}
		switch value := node.(type) {
		case *syntax.CmdSubst:
			if !p.config.AllowCommandSubstitution {
				validationErr = fmt.Errorf("command substitution is not allowed")
			}
		case *syntax.ProcSubst:
			validationErr = fmt.Errorf("process substitution is not allowed")
		case *syntax.BinaryCmd:
			if (value.Op == syntax.Pipe || value.Op == syntax.PipeAll) && p.config.AllowPipes {
				break
			}
			validationErr = fmt.Errorf("shell operator %s is not allowed", value.Op)
		case *syntax.CallExpr:
			if len(value.Args) == 0 {
				break
			}
			argv := make([]string, len(value.Args))
			for index, word := range value.Args {
				argv[index] = word.Lit()
				if argv[index] == "" {
					validationErr = fmt.Errorf("dynamic shell words are not allowed")
					return false
				}
			}
			validationErr = p.validateCommand(argv)
		case *syntax.Redirect:
			path := value.Word.Lit()
			if path == "" || !p.allowedPath(path) {
				validationErr = fmt.Errorf("redirection path is not allowed")
			}
		}
		return validationErr == nil
	})
	return validationErr
}

func (p *BuiltinToolPolicy) validateCommand(argv []string) error {
	executable := filepath.Base(argv[0])
	if len(p.config.AllowedExecutables) > 0 && !containsString(p.config.AllowedExecutables, executable) &&
		!containsString(p.config.AllowedExecutables, argv[0]) {
		return fmt.Errorf("executable %q is not allowed", executable)
	}
	if len(argv) > 1 && containsString(p.config.DeniedSubcommands[executable], argv[1]) {
		return fmt.Errorf("subcommand %s %s is denied", executable, argv[1])
	}
	return nil
}

func (p *BuiltinToolPolicy) validateTimeout(seconds int) error {
	if p.config.MaxTimeoutSeconds > 0 && seconds > p.config.MaxTimeoutSeconds {
		return fmt.Errorf("timeout exceeds policy maximum")
	}
	return nil
}

func (p *BuiltinToolPolicy) allowedPath(path string) bool {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
	}
	for _, root := range p.config.AllowedWriteRoots {
		root = filepath.Clean(root)
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func denyDecision(code, reason string) ToolDecision {
	return ToolDecision{Action: ToolDeny, Code: code, Reason: reason}
}

// AllowsTool checks whether a tool name is statically allowed by the frozen
// tool policy config. An empty AllowedTools list means all tools are allowed.
func AllowsTool(configJSON json.RawMessage, toolName string) bool {
	if len(configJSON) == 0 {
		return true
	}
	var config domain.ToolPolicyConfig
	if err := json.Unmarshal(configJSON, &config); err != nil {
		return true // fail open for unparseable config
	}
	if len(config.AllowedTools) == 0 {
		return true
	}
	for _, name := range config.AllowedTools {
		if name == toolName {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
