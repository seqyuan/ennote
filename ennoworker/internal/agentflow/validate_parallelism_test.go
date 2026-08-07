package agentflow

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parallelDefYAML builds a flow with three independent writer tasks (A1-A3)
// plus a terminal, exercising the ready-set parallel dispatch surface.
func parallelDefYAML(parallelism string, writesA2 string) string {
	return `schemaVersion: 1
id: parallel-design
inputs:
  target: {type: path, required: true}
outputs:
  final: {type: string}
budget:
  max_total_tokens: 120000
parallelism:
` + parallelism + `
tasks:
  a0:
    role: reader@1
    goal: "Dispatch {inputs.target}"
  a1:
    role: writer@1
    goal: "Explore A1; write .docs/plan/design-a1.md"
    depends: [a0]
    writes: [".docs/plan/design-a1.md"]
  a2:
    role: writer@1
    goal: "Explore A2; write .docs/plan/design-a2.md"
    depends: [a0]
` + writesA2 + `
  a3:
    role: writer@1
    goal: "Explore A3; write .docs/plan/design-a3.md"
    depends: [a0]
    writes: [".docs/plan/design-a3.md"]
  accept:
    terminal: {status: success, output: final}
    output: final
    depends: [a1, a2, a3]
`
}

func TestParallelismDefaultsAndBounds(t *testing.T) {
	// Defaults: nil parallelism is fine and yields the default ceiling.
	def, err := ParseDefinition([]byte(parallelDefYAML("", "")))
	require.NoError(t, err)
	assert.Nil(t, def.Parallelism)
	assert.Equal(t, 10, def.EffectiveParallelismMax())
	assert.False(t, def.DisjointWritersEnabled())

	// Explicit max within range.
	def, err = ParseDefinition([]byte(parallelDefYAML("  max: 3", "    writes: [\".docs/plan/design-a2.md\"]")))
	require.NoError(t, err)
	require.NotNil(t, def.Parallelism)
	assert.Equal(t, 3, def.EffectiveParallelismMax())

	// Out of range -> publish failure.
	result := validator().Validate(context.Background(), def)
	require.True(t, result.Valid, "valid explicit max 3 must pass")

	def, err = ParseDefinition([]byte(parallelDefYAML("  max: 0", "")))
	require.NoError(t, err)
	result = validator().Validate(context.Background(), def)
	assert.False(t, result.Valid)
	assertHasDiagnostic(t, result, "parallelism_max_invalid")

	def, err = ParseDefinition([]byte(parallelDefYAML("  max: 99", "")))
	require.NoError(t, err)
	result = validator().Validate(context.Background(), def)
	assert.False(t, result.Valid)
	assertHasDiagnostic(t, result, "parallelism_max_invalid")
}

func TestDisjointWritersOptInValidation(t *testing.T) {
	// Flag off: writes are documented only; unscoped writers are fine.
	def, err := ParseDefinition([]byte(parallelDefYAML("", "")))
	require.NoError(t, err)
	result := validator().Validate(context.Background(), def)
	require.True(t, result.Valid, "no flag, unscoped writer a2 must pass")

	// Flag on with all writers scoped and pairwise disjoint -> pass.
	def, err = ParseDefinition([]byte(parallelDefYAML("  max: 3\n  allow_disjoint_writers: true", "    writes: [\".docs/plan/design-a2.md\"]")))
	require.NoError(t, err)
	assert.True(t, def.DisjointWritersEnabled())
	result = validator().Validate(context.Background(), def)
	require.True(t, result.Valid, "all writers disjoint-scoped must pass")
	require.NotEmpty(t, result.ConfigDigest)

	// Flag on but a writer without a scope -> rejected.
	def, err = ParseDefinition([]byte(parallelDefYAML("  max: 3\n  allow_disjoint_writers: true", "")))
	require.NoError(t, err)
	result = validator().Validate(context.Background(), def)
	assert.False(t, result.Valid)
	assertHasDiagnostic(t, result, "disjoint_writers_unscoped")

	// Flag on but overlapping scopes -> rejected.
	def, err = ParseDefinition([]byte(parallelDefYAML("  max: 3\n  allow_disjoint_writers: true", "    writes: [\".docs/plan/design-a1.md\"]")))
	require.NoError(t, err)
	result = validator().Validate(context.Background(), def)
	assert.False(t, result.Valid)
	assertHasDiagnostic(t, result, "disjoint_writers_overlap")

	// Wildcard overlapping a literal -> rejected (design/** matches design-a1.md).
	def, err = ParseDefinition([]byte(parallelDefYAML("  max: 3\n  allow_disjoint_writers: true", "    writes: [\".docs/plan/**\"]")))
	require.NoError(t, err)
	result = validator().Validate(context.Background(), def)
	assert.False(t, result.Valid)
	assertHasDiagnostic(t, result, "disjoint_writers_overlap")
}

func TestWritesGlobValidation(t *testing.T) {
	bad := []string{"/workspace/x.md", "../escape.md", "a/../b.md", "", "a;rm -rf"}
	for _, scope := range bad {
		require.Error(t, validateWritesGlob(scope), scope)
	}
	good := []string{".docs/plan/a1.md", "design/**", "src/*.go", "generated/"}
	for _, scope := range good {
		require.NoError(t, validateWritesGlob(scope), scope)
	}
}

func TestWritesDisjoint(t *testing.T) {
	assert.True(t, writesDisjoint([]string{".docs/plan/a1.md"}, []string{".docs/plan/a2.md"}))
	assert.False(t, writesDisjoint(nil, []string{".docs/plan/a1.md"}), "empty scope = whole workspace")
	assert.False(t, writesDisjoint([]string{".docs/plan/a1.md"}, []string{".docs/plan/*.md"}))
	assert.False(t, writesDisjoint([]string{".docs/plan/"}, []string{".docs/plan/a1.md"}))
	assert.False(t, writesDisjoint([]string{".docs/plan/a1.md"}, []string{".docs/plan/a1.md"}))
	assert.True(t, writesDisjoint([]string{".docs/plan/a1.md"}, []string{"design/a1.md"}))
}

func assertHasDiagnostic(t *testing.T, result *ValidationResult, code string) {
	t.Helper()
	for _, d := range result.Diagnostics {
		if d.Code == code {
			return
		}
	}
	t.Fatalf("expected diagnostic %q, got %v", code, result.Diagnostics)
}

// TestEffectiveParallelismMaxDirect pins the domain-level default constant.
func TestEffectiveParallelismMaxDirect(t *testing.T) {
	def := &domain.FlowDefinition{}
	assert.Equal(t, domain.DefaultFlowParallelismMax, def.EffectiveParallelismMax())
}
