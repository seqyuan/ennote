package domain

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"syscall"
)

type ExecutionClass string

const (
	ExecutionReadOnly       ExecutionClass = "read_only"
	ExecutionWorkspaceWrite ExecutionClass = "workspace_write"
	ExecutionExclusive      ExecutionClass = "exclusive"
)

// ToolRetryMode controls whether a tool opts into automatic retry on transient errors.
type ToolRetryMode string

const (
	// ToolRetryNever means the tool is never retried automatically.
	ToolRetryNever ToolRetryMode = "never"
	// ToolRetryTransient means the tool may be retried on typed transient errors.
	ToolRetryTransient ToolRetryMode = "transient"
)

// ToolRetryPolicy describes retry behaviour for a single tool.
type ToolRetryPolicy struct {
	Mode       ToolRetryMode
	MaxRetries int // Tool-level hard cap on retry attempts.
}

// RetryPolicyProvider lets a tool declare its retry policy.
// Tools that do not implement this interface are treated as ToolRetryNever.
type RetryPolicyProvider interface {
	RetryPolicy() ToolRetryPolicy
}

// ToolRetryClassifier resolves a tool's retry policy by name.
// The agent Loop uses this to decide whether to attempt retries.
type ToolRetryClassifier interface {
	RetryPolicy(toolName string) ToolRetryPolicy
}

// ToolRunner executes tool calls. The error return signals an execution
// infrastructure failure that did not produce a final ToolResult. A
// non-nil ToolResult (even with IsError true) is always a terminal
// outcome that stops the retry loop.
type ToolRunner interface {
	Definitions() []ToolDefinition
	Execute(context.Context, ToolCall) (ToolResult, error)
}

type ToolExecutionClassifier interface {
	ExecutionClass(toolName string) ExecutionClass
}

type ToolArgumentValidator interface {
	ValidateArguments(toolName string, arguments json.RawMessage) error
}

// ToolExecutionConfig holds batch-level tool execution settings.
type ToolExecutionConfig struct {
	Mode                   string `json:"mode"`
	MaxConcurrentReadTools int    `json:"maxConcurrentReadTools"`
	MaxToolRetries         int    `json:"maxToolRetries"`
}

// ToolOutcomeKind classifies how a tool execution ended.
type ToolOutcomeKind string

const (
	// ToolReturned means the tool ran to completion and returned a ToolResult.
	ToolReturned ToolOutcomeKind = "returned"
	// ToolInfrastructureFailed means the tool could not run (network, exec, etc).
	ToolInfrastructureFailed ToolOutcomeKind = "infrastructure_failed"
	// ToolPanicked means the tool implementation recovered a panic.
	ToolPanicked ToolOutcomeKind = "panicked"
	// ToolCancelled means the context was cancelled before or during execution.
	ToolCancelled ToolOutcomeKind = "cancelled"
)

// ToolExecutionOutcome is the structured result of a tool execution attempt.
// It separates terminal results (ToolReturned, ToolPanicked) from retryable
// failures (ToolInfrastructureFailed) and cancellation.
type ToolExecutionOutcome struct {
	Result       ToolResult
	AttemptCount int
	Kind         ToolOutcomeKind
	Cause        error // non-nil for infrastructure_failed / panicked / cancelled
}

// ToolOutputUpdate is a single chunk of streaming output from a long-running tool.
type ToolOutputUpdate struct {
	ToolCallID string
	Stream     string // "stdout" | "stderr" | "progress"
	Data       []byte
}

// ToolOutputSink receives streaming output chunks. Implementations must be
// non-blocking — a full/dropped chunk must not affect tool execution.
type ToolOutputSink interface {
	TryEmit(ToolOutputUpdate) bool
}

// StreamingToolRunner is an optional interface that tools may implement to
// provide live output streaming. Tools that do not implement this interface
// use the standard ToolRunner.Execute path.
type StreamingToolRunner interface {
	ExecuteStreaming(context.Context, ToolCall, ToolOutputSink) (ToolResult, error)
}

// TransientToolError wraps an error that may be retried when the tool
// opts into automatic retry and the concrete error passes classification.
type TransientToolError struct {
	Err error
}

func (e *TransientToolError) Error() string { return "transient tool error: " + e.Err.Error() }
func (e *TransientToolError) Unwrap() error { return e.Err }

// IsToolErrorTransient checks whether an error qualifies for automatic retry
// using only type / unwrap inspection, never string matching.
func IsToolErrorTransient(err error) bool {
	if err == nil {
		return false
	}
	// Explicit transient marker.
	var transient *TransientToolError
	if errors.As(err, &transient) {
		return true
	}
	// Syscall errors that represent temporary infrastructure failures.
	if errors.Is(err, syscall.EAGAIN) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	// Deadline exceeded (os and net variants).
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	// net.Error with Timeout() == true.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// IsToolErrorTerminal checks whether an error must NOT be retried regardless
// of the tool's retry policy.
func IsToolErrorTerminal(err error) bool {
	if err == nil {
		return false
	}
	// Cancellation and outer deadline exceeded are always terminal.
	if errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}
