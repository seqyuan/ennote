package tools

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Which tools may run twice is a safety decision, not a default. A tool that
// retries runs again after a failure, against the same world: harmless for a
// reader, unsafe for anything that writes. These tests state the rule as a
// property of the registry, so a new tool cannot quietly inherit a retry it must
// never have.

func newDefaultRegistry(t *testing.T) *Registry {
	t.Helper()
	manager, err := workspace.NewManager(t.TempDir(), t.TempDir(), "", workspace.SandboxNone)
	require.NoError(t, err)
	registry, err := NewDefaultRegistry(manager)
	require.NoError(t, err)
	return registry
}

func TestOnlyReadOnlyToolsMayRetry(t *testing.T) {
	registry := newDefaultRegistry(t)

	for _, tool := range registry.Definitions() {
		policy := registry.RetryPolicy(tool.Name)
		if policy.Mode != domain.ToolRetryTransient {
			continue
		}
		// The property, not a name list: retrying is allowed exactly for the tools
		// that cannot change what the next attempt sees. `external` is a GET of
		// untrusted content — it needs approval, but re-running it is harmless.
		class := registry.RiskClass(tool.Name)
		assert.Contains(t, []domain.RiskClass{domain.RiskReadOnly, domain.RiskExternal}, class,
			"%q retries, so it must not be a writing tool", tool.Name)
		assert.Positive(t, policy.MaxRetries, "%q retries, so it needs a hard cap", tool.Name)
	}
}

func TestMutatingToolsNeverRetry(t *testing.T) {
	registry := newDefaultRegistry(t)

	// Named one by one: a rule that only walked Definitions() would pass while a
	// renamed or newly added writer went unmentioned.
	for _, name := range []string{"bash", "exec", "write", "edit", "ask_user", "todo"} {
		policy := registry.RetryPolicy(name)
		assert.Equal(t, domain.ToolRetryNever, policy.Mode, "%q must not retry", name)
		assert.Zero(t, policy.MaxRetries, "%q must not carry a retry budget", name)
	}
}

func TestTheDocumentedReadersDoRetry(t *testing.T) {
	registry := newDefaultRegistry(t)

	// The feature is only real if these opt in; a silent regression to "never"
	// would otherwise look identical to a correct, conservative default.
	for _, name := range []string{"read", "web_fetch"} {
		assert.Equal(t, domain.ToolRetryTransient, registry.RetryPolicy(name).Mode, "%q should retry", name)
	}
}

func TestUnknownToolNeverRetries(t *testing.T) {
	registry := newDefaultRegistry(t)

	// Fails closed: a hallucinated, restricted-away, or unregistered tool gets no
	// retry budget at all.
	policy := registry.RetryPolicy("no_such_tool")
	assert.Equal(t, domain.ToolRetryNever, policy.Mode)
	assert.Zero(t, policy.MaxRetries)
}
