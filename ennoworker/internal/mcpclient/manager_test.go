package mcpclient

import (
	"context"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunConnectionSetsAreIsolated(t *testing.T) {
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

	// Two Runs using the same profile version get INDEPENDENT connections.
	runA := NewRunConnectionSet("run-a")
	defer runA.Close()
	runB := NewRunConnectionSet("run-b")
	defer runB.Close()

	sessA, genA, err := runA.GetOrConnect(ctx, "server-1", version, ConnectOption{}, nil)
	require.NoError(t, err)
	require.NotNil(t, sessA)
	assert.Equal(t, 1, genA)

	sessB, genB, err := runB.GetOrConnect(ctx, "server-1", version, ConnectOption{}, nil)
	require.NoError(t, err)
	require.NotNil(t, sessB)
	assert.Equal(t, 1, genB)

	// Distinct sessions: closing one Run must not affect the other.
	assert.NotSame(t, sessA, sessB)
	runA.Close()
	// Run B's session must still be usable.
	tools, err := sessB.ListTools(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, tools)
}

func TestRunConnectionSetGenerationBumpsOnReconnect(t *testing.T) {
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

	set := NewRunConnectionSet("run-x")
	defer set.Close()

	sess, gen, err := set.GetOrConnect(ctx, "srv", version, ConnectOption{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, gen)

	// Kill the transport: the set detects death and reconnects with a NEW
	// generation (late responses from the old generation are discarded).
	sess.Close()
	time.Sleep(100 * time.Millisecond)

	_, gen2, err := set.GetOrConnect(ctx, "srv", version, ConnectOption{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, gen2)
	assert.Equal(t, 2, set.CurrentGeneration("srv"))
}
