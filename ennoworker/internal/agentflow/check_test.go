package agentflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Matrix 10: check tasks pass the same ToolPolicy gate as tools.
func TestEvaluateCheckPolicyGate(t *testing.T) {
	argv := ParseCheckCommand("go test ./...")
	assert.Equal(t, []string{"go", "test", "./..."}, argv)

	// Discuss mode: check commands are exec-class mutations -> denied.
	discuss := &CheckPolicy{Mode: "discuss"}
	assert.Equal(t, CheckDeny, EvaluateCheck(discuss, argv).Action)
	assert.Equal(t, "permission_mode_discuss", EvaluateCheck(discuss, argv).Code)

	// Ask mode: check suspends on approval (never silently runs).
	ask := &CheckPolicy{Mode: "ask"}
	assert.Equal(t, CheckRequireAsk, EvaluateCheck(ask, argv).Action)

	// Auto mode: allowed within the executable allowlist.
	auto := &CheckPolicy{Mode: "auto"}
	assert.Equal(t, CheckAllow, EvaluateCheck(auto, argv).Action)
	restricted := &CheckPolicy{Mode: "auto", AllowedExecutables: []string{"python3"}}
	decision := EvaluateCheck(restricted, argv)
	assert.Equal(t, CheckDeny, decision.Action)
	assert.Equal(t, "executable_not_allowed", decision.Code)
	assert.Equal(t, CheckAllow, EvaluateCheck(restricted, ParseCheckCommand("python3 run.py")).Action)

	// allow_existing_behavior: allowed.
	assert.Equal(t, CheckAllow, EvaluateCheck(&CheckPolicy{Mode: "allow_existing_behavior"}, argv).Action)

	// Unknown mode: fail closed.
	assert.Equal(t, CheckDeny, EvaluateCheck(&CheckPolicy{Mode: "weird"}, argv).Action)

	// Empty command fails closed.
	assert.Equal(t, CheckDeny, EvaluateCheck(auto, nil).Action)
}
