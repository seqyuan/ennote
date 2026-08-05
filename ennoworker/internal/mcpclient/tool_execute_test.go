package mcpclient

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRecorder records MCP request steps for assertions.
type fakeRecorder struct {
	mu    sync.Mutex
	steps []recordedStep
}

type recordedStep struct {
	toolCallID string
	status     domain.MCPRequestStatus
	errorCode  string
}

func (r *fakeRecorder) RecordMCPStep(runServerID, runToolID, toolCallID string, generation int,
	status domain.MCPRequestStatus, requestDigest, responseDigest, errorCode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, recordedStep{toolCallID: toolCallID, status: status, errorCode: errorCode})
}

func (r *fakeRecorder) statuses() []domain.MCPRequestStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.MCPRequestStatus, len(r.steps))
	for i, s := range r.steps {
		out[i] = s.status
	}
	return out
}

func (r *fakeRecorder) lastErrorCode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steps) == 0 {
		return ""
	}
	return r.steps[len(r.steps)-1].errorCode
}

func TestToolDispatchAfterLossIsOutcomeUnknown(t *testing.T) {
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

	recorder := &fakeRecorder{}
	tool := &Tool{
		DefinitionSnapshot: domain.ToolDefinition{Name: "bio__echo", RiskClass: domain.RiskExternal},
		RemoteName:         "echo",
		Recorder:           recorder,
		RunServerID:        "server-1",
		RunToolID:          "tool-1",
		ConnectionProvider: func() *Session { return session },
	}

	// First call succeeds.
	result, err := tool.Execute(ctx, domain.ToolCall{ID: "tc-1", Name: "bio__echo",
		Arguments: json.RawMessage(`{"text":"hello"}`)})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content, "echo:hello")

	// Simulate transport death (kill the server process), then dispatch again.
	// The outcome is unknown: we must NOT resend or retry.
	session.Close()
	time.Sleep(50 * time.Millisecond) // let the process die

	result, err = tool.Execute(ctx, domain.ToolCall{ID: "tc-2", Name: "bio__echo",
		Arguments: json.RawMessage(`{"text":"again"}`)})
	require.NoError(t, err)
	assert.True(t, result.IsError)

	statuses := recorder.statuses()
	assert.Equal(t, domain.MCPRequestDispatched, statuses[0])
	// tc-2 ends outcome_unknown (dispatch happened, transport failed after).
	assert.Equal(t, domain.MCPRequestOutcomeUnknown, statuses[len(statuses)-1])
}

func TestToolConnectionUnavailableFailsClosed(t *testing.T) {
	recorder := &fakeRecorder{}
	tool := &Tool{
		DefinitionSnapshot: domain.ToolDefinition{Name: "bio__echo", RiskClass: domain.RiskExternal},
		RemoteName:         "echo",
		Recorder:           recorder,
		ConnectionProvider: func() *Session { return nil },
	}
	result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "tc-1", Name: "bio__echo", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "not available")
}

func TestToolInvalidArgumentsFailClosed(t *testing.T) {
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

	recorder := &fakeRecorder{}
	tool := &Tool{
		DefinitionSnapshot: domain.ToolDefinition{Name: "bio__echo", RiskClass: domain.RiskExternal},
		RemoteName:         "echo",
		Recorder:           recorder,
		ConnectionProvider: func() *Session { return session },
	}
	result, err := tool.Execute(ctx, domain.ToolCall{ID: "tc-1", Name: "bio__echo",
		Arguments: json.RawMessage(`{not valid json`)})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "invalid tool arguments")
	assert.Equal(t, "invalid_arguments", recorder.lastErrorCode())
}
