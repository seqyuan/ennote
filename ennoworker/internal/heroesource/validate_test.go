package heroesource_test

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cloneHeroDocument(document *heroesource.Document) *heroesource.Document {
	copy := *document
	copy.Model.Fallbacks = append([]string(nil), document.Model.Fallbacks...)
	copy.Skills = append([]heroesource.SkillBinding(nil), document.Skills...)
	copy.AllowedTools = append([]string(nil), document.AllowedTools...)
	copy.Context.AllowedModes = append([]domain.HeroContextMode(nil), document.Context.AllowedModes...)
	copy.Delegation.AllowedCallerKinds = append([]string(nil), document.Delegation.AllowedCallerKinds...)
	copy.Delegation.AllowedStrategies = append([]string(nil), document.Delegation.AllowedStrategies...)
	return &copy
}

// TestValidateForWriteRejectsEachContractRule pins every write-time constraint
// declared by contracts/openapi.yaml PortableHeroDocument. Each case mutates
// one field of an otherwise-valid document and asserts the returned error names
// the field path so the UI can point at it.
func TestValidateForWriteRejectsEachContractRule(t *testing.T) {
	base, err := heroesource.Parse([]byte(validHeroMarkdown))
	require.NoError(t, err)

	cases := []struct {
		name   string
		mutate func(*heroesource.Document)
		field  string
	}{
		{
			name:   "permissionCeiling empty",
			mutate: func(d *heroesource.Document) { d.PermissionCeiling = "" },
			field:  "permissionCeiling",
		},
		{
			name:   "permissionCeiling unknown enum",
			mutate: func(d *heroesource.Document) { d.PermissionCeiling = "supervisor" },
			field:  "permissionCeiling",
		},
		{
			name:   "delegation.admission empty",
			mutate: func(d *heroesource.Document) { d.Delegation.Admission = "" },
			field:  "delegation.admission",
		},
		{
			name:   "delegation.admission unknown enum",
			mutate: func(d *heroesource.Document) { d.Delegation.Admission = "sometimes" },
			field:  "delegation.admission",
		},
		{
			name:   "delegation.maxInvocationsPerParentRun below minimum",
			mutate: func(d *heroesource.Document) { d.Delegation.MaxInvocationsPerParentRun = 0 },
			field:  "delegation.maxInvocationsPerParentRun",
		},
		{
			name:   "delegation.maxConcurrentInstances below minimum",
			mutate: func(d *heroesource.Document) { d.Delegation.MaxConcurrentInstances = 0 },
			field:  "delegation.maxConcurrentInstances",
		},
		{
			name:   "budgetCeiling.maxModelCalls below minimum",
			mutate: func(d *heroesource.Document) { d.Delegation.BudgetCeiling.MaxModelCalls = 0 },
			field:  "delegation.budgetCeiling.maxModelCalls",
		},
		{
			name:   "budgetCeiling.maxToolCalls below minimum",
			mutate: func(d *heroesource.Document) { d.Delegation.BudgetCeiling.MaxToolCalls = -1 },
			field:  "delegation.budgetCeiling.maxToolCalls",
		},
		{
			name:   "budgetCeiling.maxTotalTokens below minimum",
			mutate: func(d *heroesource.Document) { d.Delegation.BudgetCeiling.MaxTotalTokens = 0 },
			field:  "delegation.budgetCeiling.maxTotalTokens",
		},
		{
			name:   "budgetCeiling.maxOutputTokens below minimum",
			mutate: func(d *heroesource.Document) { d.Delegation.BudgetCeiling.MaxOutputTokens = 0 },
			field:  "delegation.budgetCeiling.maxOutputTokens",
		},
		{
			name:   "budgetCeiling.maxCostUsdMicros below minimum",
			mutate: func(d *heroesource.Document) { d.Delegation.BudgetCeiling.MaxCostUSDMicros = -1 },
			field:  "delegation.budgetCeiling.maxCostUsdMicros",
		},
		{
			name:   "budgetCeiling.maxWallTimeMs below minimum",
			mutate: func(d *heroesource.Document) { d.Delegation.BudgetCeiling.MaxWallTimeMS = 0 },
			field:  "delegation.budgetCeiling.maxWallTimeMs",
		},
		{
			name:   "maxLoopIterations below minimum",
			mutate: func(d *heroesource.Document) { d.MaxLoopIterations = 0 },
			field:  "maxLoopIterations",
		},
		{
			name:   "context.allowedModes empty",
			mutate: func(d *heroesource.Document) { d.Context.AllowedModes = nil },
			field:  "context.allowedModes",
		},
		{
			name:   "context.defaultMode empty",
			mutate: func(d *heroesource.Document) { d.Context.DefaultMode = "" },
			field:  "context.defaultMode",
		},
		{
			name: "context.defaultMode not in allowedModes",
			mutate: func(d *heroesource.Document) {
				d.Context.AllowedModes = []domain.HeroContextMode{domain.HeroContextFresh, domain.HeroContextRoom}
				d.Context.DefaultMode = domain.HeroContextTask
			},
			field: "context.defaultMode",
		},
		{
			name:   "model.thinkingEffort unknown enum",
			mutate: func(d *heroesource.Document) { d.Model.ThinkingEffort = "maximum" },
			field:  "model.thinkingEffort",
		},
		{
			name:   "context.ownExecutionContinuity not none",
			mutate: func(d *heroesource.Document) { d.Context.OwnExecutionContinuity = "same" },
			field:  "context.ownExecutionContinuity",
		},
		{
			name:   "prompt empty",
			mutate: func(d *heroesource.Document) { d.Prompt = "" },
			field:  "prompt",
		},
		{
			name:   "prompt over max bytes",
			mutate: func(d *heroesource.Document) { d.Prompt = strings.Repeat("x", 65537) },
			field:  "prompt",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document := cloneHeroDocument(base)
			test.mutate(document)
			err := heroesource.ValidateForWrite(document)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.field)
		})
	}
}

// TestValidateForWriteAcceptsBoundaryValues pins that the two budget ceiling
// fields the contract allows to be zero pass at zero while the rest stay valid.
func TestValidateForWriteAcceptsBoundaryValues(t *testing.T) {
	document, err := heroesource.Parse([]byte(validHeroMarkdown))
	require.NoError(t, err)
	document.Delegation.BudgetCeiling.MaxToolCalls = 0
	document.Delegation.BudgetCeiling.MaxCostUSDMicros = 0
	require.NoError(t, heroesource.ValidateForWrite(document))
}

// TestParseRemainsLenientForEmptyPermissionCeiling pins the read/write split:
// Parse, SourceDigest, and Encode are the read/encode path used by revision
// readers, so they must keep accepting a legacy document whose permissionCeiling
// is empty. ValidateForWrite is the only place that rejects it. Putting this
// check in normalizeAndValidate would have made every existing Hero file and
// every already-published revision fail to load.
func TestParseRemainsLenientForEmptyPermissionCeiling(t *testing.T) {
	input := strings.Replace(validHeroMarkdown, "permissionCeiling: ask", "permissionCeiling: \"\"", 1)
	document, err := heroesource.Parse([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, domain.PermissionMode(""), document.PermissionCeiling)

	_, err = heroesource.SourceDigest(document)
	require.NoError(t, err)
	_, err = heroesource.Encode(document)
	require.NoError(t, err)
}
