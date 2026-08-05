package mcpclient

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runLoopTools is a minimal ToolRunner harness for the agent Loop.
type runLoopTools struct {
	reg *tools.Registry
}

func (t *runLoopTools) Definitions() []domain.ToolDefinition { return t.reg.Definitions() }
func (t *runLoopTools) ExecutionClass(name string) domain.ExecutionClass {
	return t.reg.ExecutionClass(name)
}
func (t *runLoopTools) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	return t.reg.Execute(ctx, call)
}

type loopEventWriter struct {
	mu     sync.Mutex
	types  []string
	text   []string
	errors []string
}

func (w *loopEventWriter) Append(_ context.Context, _ string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []domain.RunEvent
	for _, item := range pending {
		event := domain.RunEvent{RunID: "e2e-mcp", EventType: item.EventType, Payload: item.Payload}
		w.types = append(w.types, item.EventType)
		out = append(out, event)
	}
	return out, nil
}

func (w *loopEventWriter) eventTypes() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.types))
	copy(out, w.types)
	return out
}

// TestEndToEndMCPToolThroughAgentLoop drives a REAL MCP stdio server through
// the full chain: Registry registration -> agent Loop -> McpTool.Execute ->
// tool result projection. This is the vertical-slice proof that an MCP tool
// behaves like a first-class Ennote tool in a Run.
func TestEndToEndMCPToolThroughAgentLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio integration in short mode")
	}
	version := &domain.MCPServerProfileVersion{
		Transport:   domain.MCPTransportStdio,
		Executable:  selfExec(t),
		EnvLiterals: map[string]string{"ENNOTE_MCP_TEST_SERVER": "1"},
		TimeoutMS:   10000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Connect the server (a fresh, dedicated connection for this Run).
	session, err := Connect(ctx, version, ConnectOption{})
	require.NoError(t, err)
	defer session.Close()

	// Normalize its catalog and register a frozen McpTool.
	rawTools, err := session.ListTools(ctx)
	require.NoError(t, err)
	entries, err := NormalizeCatalog("bio", rawTools)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	recorder := &fakeRecorder{}
	reg, err := tools.NewRegistry()
	require.NoError(t, err)
	for _, entry := range entries {
		mcpTool := &Tool{
			DefinitionSnapshot: BuildToolDefinition(entry, domain.RiskExternal),
			ServerSlug:         "bio",
			RemoteName:         entry.RemoteName,
			Recorder:           recorder,
			ConnectionProvider: func() *Session { return session },
		}
		require.NoError(t, reg.Register(mcpTool))
	}

	// Provider: first turn asks for the MCP tool; second turn ends.
	provider := llm.NewFakeProvider(
		llm.FakeStep{
			Completion: domain.Completion{
				ToolCalls: []domain.ToolCall{{
					ID: "mcp-call-1", Name: "bio__echo",
					Arguments: json.RawMessage(`{"text":"end-to-end"}`),
				}},
				StopReason: "tool_calls", ActualModel: "fake-model",
			},
		},
		llm.FakeStep{
			Completion: domain.Completion{
				Content:    []domain.ContentBlock{{Kind: domain.ContentText, Text: "Done"}},
				StopReason: "stop", ActualModel: "fake-model",
			},
		},
	)

	// Policy: restricted mode with the MCP tool allow-listed so the tool can
	// execute through the Loop. (Ask mode requiring approval is covered by the
	// policy unit tests; here we prove the execution path end to end.)
	policy, err := agent.NewBuiltinToolPolicy(domain.PolicySnapshot{
		ID: "restricted", Kind: domain.PolicyKindTool, Version: 1,
		Config: mustJSON(t, domain.ToolPolicyConfig{Mode: "restricted",
			AllowedTools: []string{"bio__echo", "bio__fail"}}),
	}, reg)
	require.NoError(t, err)

	writer := &loopEventWriter{}
	loop := &agent.Loop{
		Provider: provider, Tools: &runLoopTools{reg: reg}, Events: writer,
		ToolPolicy: policy, ToolPolicySnapshot: domain.PolicySnapshot{ID: "ask"},
		MaxIterations: 4, ContextTokens: 4096, MaxOutput: 2048,
	}
	result, err := loop.Run(ctx, agent.RunInput{
		RunID: "e2e-mcp", Model: "fake-model", SystemPrompt: "You can use MCP tools.",
		InitialRuntime: domain.ModelRuntimeSnapshot{ModelProfileID: "fake", APIModel: "fake-model",
			ContextTokens: 4096, MaxOutputTokens: 2048, ThinkingDialect: domain.ThinkingDialectOpenAIReasoningEffort},
		ThinkingEffort: domain.ThinkingMedium,
		History:        []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Use the MCP tool"}}}},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Iterations)

	// The tool result must be projected back into the transcript.
	last := result.Messages[len(result.Messages)-1]
	lastText := ""
	for _, block := range last.Content {
		if block.Kind == domain.ContentText {
			lastText += block.Text
		}
	}
	assert.Contains(t, lastText, "Done")

	// The MCP request state machine must show dispatched -> completed.
	statuses := recorder.statuses()
	require.Len(t, statuses, 2)
	assert.Equal(t, domain.MCPRequestDispatched, statuses[0])
	assert.Equal(t, domain.MCPRequestCompleted, statuses[1])
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

// TestEndToEndMCPAskRequiresApproval proves that in Ask mode the RiskExternal
// MCP tool suspends for approval instead of executing.
func TestEndToEndMCPAskRequiresApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio integration in short mode")
	}
	version := &domain.MCPServerProfileVersion{
		Transport:   domain.MCPTransportStdio,
		Executable:  selfExec(t),
		EnvLiterals: map[string]string{"ENNOTE_MCP_TEST_SERVER": "1"},
		TimeoutMS:   10000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := Connect(ctx, version, ConnectOption{})
	require.NoError(t, err)
	defer session.Close()

	rawTools, err := session.ListTools(ctx)
	require.NoError(t, err)
	entries, err := NormalizeCatalog("bio", rawTools)
	require.NoError(t, err)
	reg, err := tools.NewRegistry()
	require.NoError(t, err)
	for _, entry := range entries {
		require.NoError(t, reg.Register(&Tool{
			DefinitionSnapshot: BuildToolDefinition(entry, domain.RiskExternal),
			RemoteName:         entry.RemoteName,
			ConnectionProvider: func() *Session { return session },
		}))
	}
	policy, err := agent.NewBuiltinToolPolicy(domain.PolicySnapshot{
		ID: "ask", Kind: domain.PolicyKindTool, Version: 1,
		Config: mustJSON(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionAsk)}),
	}, reg)
	require.NoError(t, err)

	provider := llm.NewFakeProvider(llm.FakeStep{
		Completion: domain.Completion{
			ToolCalls:  []domain.ToolCall{{ID: "c1", Name: "bio__echo", Arguments: json.RawMessage(`{"text":"x"}`)}},
			StopReason: "tool_calls", ActualModel: "fake",
		},
	})
	loop := &agent.Loop{
		Provider: provider, Tools: &runLoopTools{reg: reg}, Events: &loopEventWriter{},
		ToolPolicy: policy, ToolPolicySnapshot: domain.PolicySnapshot{ID: "ask"},
		MaxIterations: 2, ContextTokens: 4096, MaxOutput: 2048,
	}
	_, err = loop.Run(ctx, agent.RunInput{
		RunID: "e2e-ask", Model: "fake-model", SystemPrompt: "s",
		InitialRuntime: domain.ModelRuntimeSnapshot{ModelProfileID: "fake", APIModel: "fake-model",
			ContextTokens: 4096, MaxOutputTokens: 2048, ThinkingDialect: domain.ThinkingDialectOpenAIReasoningEffort},
		ThinkingEffort: domain.ThinkingMedium,
		History:        []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "go"}}}},
	})
	// The tool must NOT execute: approval suspension surfaces as this error.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approval")
}
