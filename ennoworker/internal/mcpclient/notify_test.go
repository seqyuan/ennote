package mcpclient

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain already routes ENNOTE_MCP_TEST_SERVER=1 into runTestMCPServer. This
// test re-uses that binary with an extra marker that triggers a
// tools/list_changed notification shortly after initialize, so the client
// handler can be exercised end to end.

func TestListChangedNotificationMarksFutureCatalogStale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio integration in short mode")
	}
	var notified atomic.Bool
	version := &domain.MCPServerProfileVersion{
		Transport:   domain.MCPTransportStdio,
		Executable:  selfExec(t),
		EnvLiterals: map[string]string{"ENNOTE_MCP_TEST_SERVER": "1", "ENNOTE_MCP_TEST_NOTIFY": "1"},
		TimeoutMS:   10000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := Connect(ctx, version, ConnectOption{
		OnToolListChanged: func() { notified.Store(true) },
	})
	require.NoError(t, err)
	defer session.Close()

	// Wait for the server's post-initialize RemoveTools to deliver the
	// notification (server sleeps 400ms then removes a tool).
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if notified.Load() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.True(t, notified.Load(), "list_changed notification should have arrived")
}
