package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ProjectCompiledHook is the executable form of one ProjectToolPolicyHook.
type ProjectCompiledHook struct {
	Kind string
	Pre  PreToolHook  // deny / rewrite
	Post PostToolHook // project
}

// CompileProjectHook converts a frozen data hook into an executable listener.
// It fails loud on any invalid declaration; regexps compile once here, never per
// call. deny and rewrite become pre listeners; project becomes a post listener.
func CompileProjectHook(hook domain.ProjectToolPolicyHook) (ProjectCompiledHook, error) {
	switch hook.Kind {
	case "deny":
		if hook.Code == "" {
			return ProjectCompiledHook{}, fmt.Errorf("deny hook requires code")
		}
		return ProjectCompiledHook{Kind: hook.Kind, Pre: projectPreHook(hook)}, nil
	case "rewrite":
		if len(hook.Arguments) == 0 || !json.Valid(hook.Arguments) {
			return ProjectCompiledHook{}, fmt.Errorf("rewrite hook requires valid JSON arguments")
		}
		return ProjectCompiledHook{Kind: hook.Kind, Pre: projectPreHook(hook)}, nil
	case "project":
		var redact []*regexp.Regexp
		for _, pattern := range hook.RedactPatterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return ProjectCompiledHook{}, fmt.Errorf("project hook redact pattern: %w", err)
			}
			redact = append(redact, re)
		}
		return ProjectCompiledHook{Kind: hook.Kind, Post: projectPostHook(redact)}, nil
	default:
		return ProjectCompiledHook{}, fmt.Errorf("unknown project hook kind %q", hook.Kind)
	}
}

func projectHookMatches(when domain.ProjectHookWhen, exec *ToolExecution) bool {
	if when.ToolName != "" && when.ToolName != exec.Effective.Name {
		return false
	}
	if when.CommandContains != "" && !strings.Contains(string(exec.Effective.Arguments), when.CommandContains) {
		return false
	}
	return true
}

// projectPreHook implements deny (short-circuit on match) and rewrite (delegate
// first, then rewrite an allowed decision — never overrides a downstream
// denial, satisfying I7).
func projectPreHook(hook domain.ProjectToolPolicyHook) PreToolHook {
	return func(ctx context.Context, exec *ToolExecution, next func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		if hook.Kind == "deny" {
			if !projectHookMatches(hook.When, exec) {
				return next(exec)
			}
			return PreToolDecision{Action: ToolDeny, Code: hook.Code, Reason: hook.Reason, RiskClass: exec.RiskClass}, nil
		}
		// rewrite: delegate first.
		d, err := next(exec)
		if err != nil {
			return d, err
		}
		if isDenyDecision(d) || !projectHookMatches(hook.When, exec) {
			return d, nil
		}
		d.Arguments = append(json.RawMessage(nil), hook.Arguments...)
		return d, nil
	}
}

// projectPostHook composes over downstream projections: later hooks run first,
// then this hook applies its redact patterns on top (waterfall semantics).
func projectPostHook(redact []*regexp.Regexp) PostToolHook {
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
	}
}

// BuildRunPolicyChain assembles the per-run policy chain from the frozen
// effective config (design 一): the builtin four pre listeners + redact post,
// then the project hooks (deny/rewrite before delegation, project after redact),
// then an optional trailing delegation listener. It is the single assembly path
// shared by fresh and resume runs, so determinism (I6) is testable here.
func BuildRunPolicyChain(snapshot domain.PolicySnapshot, hooks []domain.ProjectToolPolicyHook, delegation PreToolHook) (*PolicyChain, error) {
	chain, err := DefaultPolicyChain(snapshot)
	if err != nil {
		return nil, err
	}
	for _, hook := range hooks {
		compiled, compileErr := CompileProjectHook(hook)
		if compileErr != nil {
			return nil, compileErr
		}
		switch compiled.Kind {
		case "deny", "rewrite":
			if _, regErr := chain.RegisterPre(compiled.Pre, false); regErr != nil {
				return nil, regErr
			}
		case "project":
			if _, regErr := chain.RegisterPost(compiled.Post); regErr != nil {
				return nil, regErr
			}
		}
	}
	if delegation != nil {
		if _, regErr := chain.RegisterPre(delegation, false); regErr != nil {
			return nil, regErr
		}
	}
	return chain, nil
}

// LoadWorkspaceToolPolicyHooks reads the `toolPolicyHooks` section (a flat
// array) from a trusted workspace's <canonicalRoot>/.ennote/config.json, or
// returns nil when absent. The caller owns the trust gate: this function MUST
// only be called after the workspace has been verified trusted.
func LoadWorkspaceToolPolicyHooks(canonicalRoot string) ([]domain.ProjectToolPolicyHook, error) {
	path := filepath.Join(canonicalRoot, ".ennote", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspace tool policy hooks %s: %w", path, err)
	}
	var doc struct {
		ToolPolicyHooks json.RawMessage `json:"toolPolicyHooks,omitempty"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse workspace tool policy hooks %s: %w", path, err)
	}
	if len(doc.ToolPolicyHooks) == 0 {
		return nil, nil
	}
	var hooks []domain.ProjectToolPolicyHook
	if err := json.Unmarshal(doc.ToolPolicyHooks, &hooks); err != nil {
		return nil, fmt.Errorf("decode workspace tool policy hooks: %w", err)
	}
	return hooks, nil
}
