package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type Tool interface {
	Definition() domain.ToolDefinition
	Execute(context.Context, domain.ToolCall) (domain.ToolResult, error)
}

type ClassifiedTool interface {
	ExecutionClass() domain.ExecutionClass
}

type Registry struct {
	mu          sync.RWMutex
	tools       map[string]Tool
	classes     map[string]domain.ExecutionClass
	validators  map[string]*jsonschema.Schema
	retryPolicy map[string]domain.ToolRetryPolicy
	risks       map[string]domain.RiskClass
}

func NewRegistry(tools ...Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool), classes: make(map[string]domain.ExecutionClass),
		validators: make(map[string]*jsonschema.Schema), retryPolicy: make(map[string]domain.ToolRetryPolicy),
		risks: make(map[string]domain.RiskClass)}
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) Register(tool Tool) error {
	if tool == nil {
		return fmt.Errorf("tool is nil")
	}
	definition := tool.Definition()
	if definition.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	// RiskClass is mandatory local metadata. Missing or invalid risk fails
	// registration before any map is written (fail closed, no partial state).
	if !domain.IsValidRiskClass(definition.RiskClass) {
		return fmt.Errorf("tool %s has invalid risk class %q", definition.Name, definition.RiskClass)
	}
	compiler := jsonschema.NewCompiler()
	resource := "mem://tool/" + definition.Name + ".json"
	if err := compiler.AddResource(resource, strings.NewReader(string(definition.Parameters))); err != nil {
		return fmt.Errorf("add schema for tool %s: %w", definition.Name, err)
	}
	validator, err := compiler.Compile(resource)
	if err != nil {
		return fmt.Errorf("compile schema for tool %s: %w", definition.Name, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[definition.Name]; exists {
		return fmt.Errorf("tool already registered: %s", definition.Name)
	}
	r.tools[definition.Name] = tool
	r.validators[definition.Name] = validator
	class := domain.ExecutionExclusive
	if classified, ok := tool.(ClassifiedTool); ok {
		class = classified.ExecutionClass()
	}
	r.classes[definition.Name] = class
	policy := domain.ToolRetryPolicy{Mode: domain.ToolRetryNever, MaxRetries: 0}
	if rp, ok := tool.(domain.RetryPolicyProvider); ok {
		policy = rp.RetryPolicy()
	}
	r.retryPolicy[definition.Name] = policy
	r.risks[definition.Name] = definition.RiskClass
	return nil
}

// Restrict removes every tool not present in the frozen allowlist. A Registry is
// scoped to one Run, so this cannot affect concurrent executions.
func (r *Registry) Restrict(allowed []string) {
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		set[name] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.tools {
		if set[name] {
			continue
		}
		delete(r.tools, name)
		delete(r.classes, name)
		delete(r.validators, name)
		delete(r.retryPolicy, name)
		delete(r.risks, name)
	}
}

func (r *Registry) Definitions() []domain.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	definitions := make([]domain.ToolDefinition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, r.tools[name].Definition())
	}
	return definitions
}

func (r *Registry) ExecutionClass(toolName string) domain.ExecutionClass {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if class, ok := r.classes[toolName]; ok {
		return class
	}
	return domain.ExecutionExclusive
}

func (r *Registry) ValidateArguments(toolName string, arguments json.RawMessage) error {
	r.mu.RLock()
	validator := r.validators[toolName]
	r.mu.RUnlock()
	if validator == nil {
		return fmt.Errorf("unknown tool: %s", toolName)
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode arguments for %s: %w", toolName, err)
	}
	if err := validator.Validate(value); err != nil {
		return fmt.Errorf("validate arguments for %s: %w", toolName, err)
	}
	return nil
}

// RetryPolicy implements domain.ToolRetryClassifier.
func (r *Registry) RetryPolicy(toolName string) domain.ToolRetryPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if policy, ok := r.retryPolicy[toolName]; ok {
		return policy
	}
	return domain.ToolRetryPolicy{Mode: domain.ToolRetryNever, MaxRetries: 0}
}

// RiskClass implements domain.ToolRiskClassifier. Unknown, unregistered, and
// Role-restricted tools resolve to RiskSensitive so policy fails closed.
func (r *Registry) RiskClass(toolName string) domain.RiskClass {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if risk, ok := r.risks[toolName]; ok {
		return risk
	}
	return domain.RiskSensitive
}

// ResolveStandingApprovalScope implements domain.StandingApprovalScopeResolver.
// It delegates to the tool's optional StandingApprovalScopeProvider; returns
// ok=false for tools that do not implement it.
func (r *Registry) ResolveStandingApprovalScope(toolName string, arguments json.RawMessage) (domain.StandingApprovalScope, bool, error) {
	r.mu.RLock()
	tool := r.tools[toolName]
	r.mu.RUnlock()
	if tool == nil {
		return domain.StandingApprovalScope{}, false, nil
	}
	provider, ok := tool.(domain.StandingApprovalScopeProvider)
	if !ok {
		return domain.StandingApprovalScope{}, false, nil
	}
	// Recover panics from provider implementations — fail closed.
	var scope domain.StandingApprovalScope
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("standing scope provider panic for tool %s", toolName)
			}
		}()
		scope, err = provider.StandingApprovalScope(arguments)
	}()
	if err != nil {
		return domain.StandingApprovalScope{}, false, err
	}
	// Validate the returned scope.
	if scope.Kind == "" || scope.Key == "" || scope.Display == "" {
		return domain.StandingApprovalScope{}, false, fmt.Errorf("tool %s returned empty standing scope field", toolName)
	}
	if scope.ScopeVersion < 1 {
		return domain.StandingApprovalScope{}, false, fmt.Errorf("tool %s returned invalid scope version %d", toolName, scope.ScopeVersion)
	}
	if len(scope.Kind) > 64 || len(scope.Key) > 512 || len(scope.Display) > 200 {
		return domain.StandingApprovalScope{}, false, fmt.Errorf("tool %s returned out-of-bounds standing scope field", toolName)
	}
	return scope, true, nil
}

func (r *Registry) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	r.mu.RLock()
	tool := r.tools[call.Name]
	r.mu.RUnlock()
	if tool == nil {
		err := fmt.Errorf("unknown tool: %s", call.Name)
		return errorResult(call, err), nil
	}
	result, err := tool.Execute(ctx, call)
	result.ToolCallID = call.ID
	result.ToolName = call.Name
	return result, err
}

// ExecuteStreaming implements domain.StreamingToolRunner: it forwards to the
// concrete tool when the tool itself supports streaming, otherwise it falls
// back to the standard Execute path.
func (r *Registry) ExecuteStreaming(ctx context.Context, call domain.ToolCall, sink domain.ToolOutputSink) (domain.ToolResult, error) {
	r.mu.RLock()
	tool := r.tools[call.Name]
	r.mu.RUnlock()
	if tool == nil {
		err := fmt.Errorf("unknown tool: %s", call.Name)
		return errorResult(call, err), nil
	}
	if streaming, ok := tool.(domain.StreamingToolRunner); ok {
		result, err := streaming.ExecuteStreaming(ctx, call, sink)
		result.ToolCallID = call.ID
		result.ToolName = call.Name
		return result, err
	}
	return r.Execute(ctx, call)
}

func errorResult(call domain.ToolCall, err error) domain.ToolResult {
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: err.Error(), IsError: true}
}
