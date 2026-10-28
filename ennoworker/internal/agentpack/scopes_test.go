package agentpack

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestSkillScopeDefaultsToGlobal(t *testing.T) {
	manifest, err := ParseManifest([]byte(mixedManifest))
	require.NoError(t, err)
	require.Len(t, manifest.Skills, 2)
	assert.Equal(t, SkillScopeGlobal, manifest.Skills[0].Scope)
	// An external ref carries no scope at all.
	assert.Empty(t, manifest.Skills[1].Scope)

	// The inspect result reports the applied default, not the empty string.
	result := inspectFiles(t, mixedPackFiles())
	require.Len(t, result.Skills, 2)
	assert.Equal(t, SkillScopeGlobal, result.Skills[0].Scope)
	assert.Empty(t, result.Skills[1].Scope)
}

func TestManifestAcceptsFlowScopedVendoredSkill(t *testing.T) {
	manifest, err := ParseManifest([]byte(strings.Replace(mixedManifest,
		"  - id: cell-qc-tables\n    path: skills/cell-qc-tables/",
		"  - id: cell-qc-tables\n    path: skills/cell-qc-tables/\n    scope: flow", 1)))
	require.NoError(t, err)
	require.Len(t, manifest.Skills, 2)
	assert.Equal(t, SkillScopeFlow, manifest.Skills[0].Scope)

	result := inspectFiles(t, mixedPackFiles())
	assert.Equal(t, SkillScopeGlobal, result.Skills[0].Scope)
}

func TestManifestRejectsUnsupportedSkillScope(t *testing.T) {
	_, err := ParseManifest([]byte(strings.Replace(mixedManifest,
		"  - id: cell-qc-tables\n    path: skills/cell-qc-tables/",
		"  - id: cell-qc-tables\n    path: skills/cell-qc-tables/\n    scope: project", 1)))
	requireCode(t, err, CodePackSkillInvalid)
	assert.Contains(t, err.Error(), "scope")
}

func TestManifestRejectsScopeOnExternalSkillRef(t *testing.T) {
	_, err := ParseManifest([]byte(strings.Replace(mixedManifest,
		"  - ref: bio-workspace@0.1.0",
		"  - ref: bio-workspace@0.1.0\n    scope: flow", 1)))
	requireCode(t, err, CodePackSkillInvalid)
	assert.Contains(t, err.Error(), "must not declare a scope")
}
