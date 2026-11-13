package graphsource_test

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/require"
)

// TestModelRefSyntaxParity pins the shared portable model-ref syntax rule across
// both source parsers with one table, so the two packages cannot drift apart
// again. graphsource and heroesource both delegate to
// domain.SplitPortableModelRef and must return the same verdict and message for
// the same input.
func TestModelRefSyntaxParity(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		ok   bool
	}{
		{name: "two segments", ref: "provider/model", ok: true},
		{name: "multi segment model", ref: "openai-main/org/gpt-5.4", ok: true},
		{name: "empty model", ref: "provider/", ok: false},
		{name: "empty provider", ref: "/model", ok: false},
		{name: "no separator", ref: "provider", ok: false},
		{name: "whitespace in provider", ref: "provider /model", ok: false},
		{name: "whitespace in model", ref: "provider/ model", ok: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, graphErr := graphsource.Parse(graphWithModel(test.ref))
			_, heroErr := heroesource.Parse(heroWithModel(test.ref))
			if test.ok {
				require.NoError(t, graphErr)
				require.NoError(t, heroErr)
				return
			}
			require.Error(t, graphErr)
			require.Error(t, heroErr)
			require.Contains(t, graphErr.Error(), "must use provider-name/model-name")
			require.Contains(t, heroErr.Error(), "must use provider-name/model-name")
		})
	}
}

func graphWithModel(ref string) []byte {
	return []byte(`schema_version: 1
id: gg
name: GG
tasks:
  tt:
    name: TT
    model: ` + ref + `
    goal: g
graph:
  tt: []
`)
}

func heroWithModel(ref string) []byte {
	return []byte(`---
schemaVersion: 1
handle: hh
name: H
model:
  ref: ` + ref + `
  thinkingEffort: default
  fallbacks: []
skills: []
authority: read_only
permissionCeiling: discuss
allowedTools: [read]
context:
  defaultMode: room
  allowedModes: [room]
  ownExecutionContinuity: none
delegation:
  admission: deny
  allowedCallerKinds: [host]
  allowedStrategies: [single]
  maxInvocationsPerParentRun: 1
  maxConcurrentInstances: 1
  budgetCeiling:
    maxModelCalls: 4
    maxToolCalls: 8
    maxTotalTokens: 20000
    maxOutputTokens: 4000
    maxCostUsdMicros: 100000
    maxWallTimeMs: 120000
outputContract: text-v1
maxLoopIterations: 8
---
work`)
}
