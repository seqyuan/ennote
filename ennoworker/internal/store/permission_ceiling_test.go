package store

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveChildPermissionCeilingFailsClosed the convergence rule on its
// own. The child of a delegation may never run looser than its parent, and the
// mode the parent holds is not always expressible as a builtin profile, so the
// unrepresentable cases have to decide explicitly instead of falling through to
// whatever the Hero asked for.
func TestResolveChildPermissionCeilingFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		parent   domain.PermissionMode
		hero     domain.PermissionMode
		expected domain.PermissionMode
	}{
		{"parent looser than the Hero keeps the Hero", domain.PermissionAuto, domain.PermissionAsk, domain.PermissionAsk},
		{"parent stricter than the Hero wins", domain.PermissionAsk, domain.PermissionAuto, domain.PermissionAsk},
		{"equal modes stay put", domain.PermissionAuto, domain.PermissionAuto, domain.PermissionAuto},
		{"discuss parent pins an auto Hero", domain.PermissionDiscuss, domain.PermissionAuto, domain.PermissionDiscuss},
		// allow_existing_behavior permits every executable, so it can only be
		// looser than any Hero ceiling: it must not tighten anything.
		{"allow_existing_behavior never tightens", domain.PermissionMode("allow_existing_behavior"), domain.PermissionAuto, domain.PermissionAuto},
		// restricted is the strictest deployment mode but has no builtin profile;
		// the strictest builtin is the tightest ceiling a child can be given.
		{"restricted parent falls back to the strictest builtin", domain.PermissionMode("restricted"), domain.PermissionAuto, domain.PermissionDiscuss},
		// An unrecognized mode (an older frozen snapshot) must not silently leave
		// the child on the looser Hero ceiling.
		{"unknown parent mode fails closed", domain.PermissionMode(""), domain.PermissionAuto, domain.PermissionDiscuss},
		{"unknown parent mode does not loosen a strict Hero", domain.PermissionMode("future_mode"), domain.PermissionDiscuss, domain.PermissionDiscuss},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := resolveChildPermissionCeiling(testCase.parent, testCase.hero)
			require.NoError(t, err)
			assert.Equal(t, testCase.expected, got)
		})
	}

	_, err := resolveChildPermissionCeiling(domain.PermissionAuto, domain.PermissionMode("bogus"))
	require.Error(t, err, "an unexpressible Hero ceiling must fail rather than default")
}
