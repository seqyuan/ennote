package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// DefaultPolicyChain builds the Stage 1 builtin chain: the four pre listeners
// that reproduce BuiltinToolPolicy.BeforeToolBatch in code order (mode gate →
// allowlist → shell AST validation → Ask require_approval) plus the redact post
// listener. Listeners read exec.RiskClass (resolved once by the caller) instead
// of re-resolving it (0.2-14). allow_existing_behavior keeps its skip
// semantics: the allowlist and shell listeners delegate through unchanged.
func DefaultPolicyChain(snapshot domain.PolicySnapshot) (*PolicyChain, error) {
	var config domain.ToolPolicyConfig
	if err := json.Unmarshal(snapshot.Config, &config); err != nil {
		return nil, fmt.Errorf("decode tool policy: %w", err)
	}
	chain := NewPolicyChain()
	if _, err := chain.RegisterPre(modeGatePreHook(config), false); err != nil {
		return nil, err
	}
	if _, err := chain.RegisterPre(allowlistPreHook(config), false); err != nil {
		return nil, err
	}
	if _, err := chain.RegisterPre(shellValidationPreHook(config), false); err != nil {
		return nil, err
	}
	if _, err := chain.RegisterPre(askApprovalPreHook(config), false); err != nil {
		return nil, err
	}
	redact, err := redactPostHook(config)
	if err != nil {
		return nil, err
	}
	if _, err := chain.RegisterPost(redact); err != nil {
		return nil, err
	}
	return chain, nil
}

// modeGatePreHook reproduces the two leading branches: Discuss mode denies
// non-readonly tools; Ask mode denies sensitive tools.
func modeGatePreHook(config domain.ToolPolicyConfig) PreToolHook {
	return func(_ context.Context, exec *ToolExecution, next func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		if config.Mode == string(domain.PermissionDiscuss) && exec.RiskClass != domain.RiskReadOnly {
			return PreToolDecision{Action: ToolDeny, Code: "permission_mode_discuss",
				Reason: "Discuss mode allows read-only tools only", RiskClass: exec.RiskClass}, nil
		}
		if config.Mode == string(domain.PermissionAsk) && exec.RiskClass == domain.RiskSensitive {
			return PreToolDecision{Action: ToolDeny, Code: "permission_mode_sensitive",
				Reason: "Sensitive or unknown tools cannot be approved", RiskClass: exec.RiskClass}, nil
		}
		return next(exec)
	}
}

// allowlistPreHook reproduces the allowed-tools branch. allow_existing_behavior
// delegates through unchanged (skips this and the shell validation branch).
func allowlistPreHook(config domain.ToolPolicyConfig) PreToolHook {
	return func(_ context.Context, exec *ToolExecution, next func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		if config.Mode == "allow_existing_behavior" {
			return next(exec)
		}
		if !containsString(config.AllowedTools, exec.Effective.Name) && len(config.AllowedTools) > 0 {
			return PreToolDecision{Action: ToolDeny, Code: "tool_not_allowed",
				Reason: "tool is not allowed by policy", RiskClass: exec.RiskClass}, nil
		}
		return next(exec)
	}
}

// shellValidationPreHook reproduces the exec/bash AST validation branch.
func shellValidationPreHook(config domain.ToolPolicyConfig) PreToolHook {
	return func(_ context.Context, exec *ToolExecution, next func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		if config.Mode == "allow_existing_behavior" {
			return next(exec)
		}
		var err error
		switch exec.Effective.Name {
		case "exec":
			err = validateExecArgs(config, exec.Effective.Arguments)
		case "bash":
			err = validateBashArgs(config, exec.Effective.Arguments)
		}
		if err != nil {
			return PreToolDecision{Action: ToolDeny, Code: "process_not_allowed",
				Reason: err.Error(), RiskClass: exec.RiskClass}, nil
		}
		return next(exec)
	}
}

// askApprovalPreHook reproduces the trailing Ask-mode require_approval branch.
func askApprovalPreHook(config domain.ToolPolicyConfig) PreToolHook {
	return func(_ context.Context, exec *ToolExecution, next func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		if config.Mode == string(domain.PermissionAsk) && exec.RiskClass != domain.RiskReadOnly {
			return PreToolDecision{Action: ToolRequireApproval, Code: "permission_mode_ask",
				Reason: "Ask mode requires approval for this tool", RiskClass: exec.RiskClass}, nil
		}
		return next(exec)
	}
}

// redactPostHook reproduces AfterToolCall's redaction projection. It composes
// over downstream post listeners (project hooks): delegate first, then apply its
// own patterns on top of the downstream projection (waterfall semantics).
func redactPostHook(config domain.ToolPolicyConfig) (PostToolHook, error) {
	var redact []*regexp.Regexp
	for _, pattern := range config.RedactPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("compile redaction pattern: %w", err)
		}
		redact = append(redact, re)
	}
	return func(ctx context.Context, exec *ToolExecution, result domain.ToolResult, next func(domain.ToolResult) (PostToolDecision, error)) (PostToolDecision, error) {
		d, err := next(result)
		if err != nil {
			return d, err
		}
		projected := d.Result
		for _, pattern := range redact {
			projected.Content = pattern.ReplaceAllString(projected.Content, "[REDACTED]")
		}
		d.Action = "replace"
		d.Result = projected
		return d, nil
	}, nil
}
