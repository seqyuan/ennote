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
	Execute(context.Context, domain.ToolCall) domain.ToolResult
}

type ClassifiedTool interface {
	ExecutionClass() domain.ExecutionClass
}

type Registry struct {
	mu         sync.RWMutex
	tools      map[string]Tool
	classes    map[string]domain.ExecutionClass
	validators map[string]*jsonschema.Schema
}

func NewRegistry(tools ...Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool), classes: make(map[string]domain.ExecutionClass),
		validators: make(map[string]*jsonschema.Schema)}
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
	return nil
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

func (r *Registry) Execute(ctx context.Context, call domain.ToolCall) domain.ToolResult {
	r.mu.RLock()
	tool := r.tools[call.Name]
	r.mu.RUnlock()
	if tool == nil {
		return errorResult(call, fmt.Errorf("unknown tool: %s", call.Name))
	}
	result := tool.Execute(ctx, call)
	result.ToolCallID = call.ID
	result.ToolName = call.Name
	return result
}

func errorResult(call domain.ToolCall, err error) domain.ToolResult {
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: err.Error(), IsError: true}
}
