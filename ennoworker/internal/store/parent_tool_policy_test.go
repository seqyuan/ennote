package store

import (
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Graph flow anchor is an orchestration vehicle with no agent loop, so it has
// no permission mode for a child to converge on. Every Graph task child carries a
// frozen Hero and a parent Run id, so before this rule the child's freeze failed
// on the anchor's empty tool policy and no Graph run could start at all.
func TestParentToolPolicyModeForAFlowAnchorKeepsTheHeroCeiling(t *testing.T) {
	for name, config := range map[string]json.RawMessage{
		"absent":    nil,
		"null":      json.RawMessage(`null`),
		"mode-less": json.RawMessage(`{"allowedTools":["read"]}`),
	} {
		t.Run(name, func(t *testing.T) {
			mode, err := parentToolPolicyMode(true, config)
			require.NoError(t, err, "an anchor must not fail the child's freeze")
			assert.Equal(t, deploymentModeAllowExistingBehavior, mode)

			// The headline behaviour: the Hero ceiling decides alone.
			ceiling, err := resolveChildPermissionCeiling(mode, domain.PermissionAuto)
			require.NoError(t, err)
			assert.Equal(t, domain.PermissionAuto, ceiling)
			ceiling, err = resolveChildPermissionCeiling(mode, domain.PermissionDiscuss)
			require.NoError(t, err)
			assert.Equal(t, domain.PermissionDiscuss, ceiling)
		})
	}
}

// The anchor branch must not swallow an anchor that does freeze a mode: the
// convergence rule still applies whenever there is something to converge on.
func TestParentToolPolicyModeForAFrozenAnchorStillConverges(t *testing.T) {
	mode, err := parentToolPolicyMode(true, json.RawMessage(`{"mode":"ask"}`))
	require.NoError(t, err)
	assert.Equal(t, domain.PermissionAsk, mode)

	ceiling, err := resolveChildPermissionCeiling(mode, domain.PermissionAuto)
	require.NoError(t, err)
	assert.Equal(t, domain.PermissionAsk, ceiling, "a mode-bearing parent still tightens the child")
}

// A real parent Run without a frozen tool policy is an inconsistency, not an
// anchor: 896b129 exists so a parent running in Ask cannot spawn a looser child,
// and treating a missing snapshot as "no constraint" would undo it.
func TestParentToolPolicyModeForARealParentWithoutToolPolicyFails(t *testing.T) {
	for name, config := range map[string]json.RawMessage{
		"absent":    nil,
		"null":      json.RawMessage(`null`),
		"mode-less": json.RawMessage(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parentToolPolicyMode(false, config)
			require.Error(t, err, "a delegated child must not inherit a missing parent policy as permission")
			assert.Contains(t, err.Error(), "no frozen tool policy")
		})
	}

	_, err := parentToolPolicyMode(false, json.RawMessage(`{`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode parent Run tool policy")
}

func TestParentToolPolicyModeDecodesAMode(t *testing.T) {
	mode, err := parentToolPolicyMode(false, json.RawMessage(`{"mode":"discuss","allowedTools":["read"]}`))
	require.NoError(t, err)
	assert.Equal(t, domain.PermissionDiscuss, mode)
}
