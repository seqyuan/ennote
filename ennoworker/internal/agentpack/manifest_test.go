package agentpack

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const agentFlowSource = `schemaVersion: 1
id: qc-review
version: 1
description: QC review with a deterministic gate.
outputs:
  report:
    type: file
budget:
  max_total_tokens: 400000
tasks:
  load:
    role: qc-reviewer
    goal: Load the samplesheet
  gate:
    type: check
    command: "test -s results/qc.csv"
    depends: [load]
  review:
    role: qc-reviewer
    goal: Review QC metrics
    depends: [gate]
    fan_out:
      min: 1
      max: 4
    skills: [local/cell-qc-tables]
  done:
    terminal:
      status: success
      output: report
    depends: [review]
`

func mixedManifestWithFlowFormat() string {
	return strings.Replace(mixedManifest,
		"    path: flows/qc-review/graph.yaml\n    format: graph",
		"    path: flows/qc-review/flow.yaml\n    format: flow", 1)
}

func mixedFlowFiles() []entry {
	files := mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: mixedManifestWithFlowFormat()}
	files[2] = entry{path: "flows/qc-review/flow.yaml", body: agentFlowSource}
	return files
}

func TestInspectAgentFlowFormatKeepsChecksAndFanOut(t *testing.T) {
	result := inspectFiles(t, mixedFlowFiles())
	require.Len(t, result.Flows, 1)
	flow := result.Flows[0]
	assert.Equal(t, FlowFormatFlow, flow.Format)
	assert.Equal(t, 4, flow.TaskCount)
	assert.Equal(t, "load", flow.EntryTask)
	assert.Equal(t, 1, flow.CheckTasks)
	assert.Equal(t, 1, flow.FanOutTasks)
	assert.Equal(t, int64(400000), flow.MaxTotalTokens)

	envelope := result.CapabilityEnvelope
	assert.Equal(t, []string{"test -s results/qc.csv"}, envelope.CheckCommands)
	assert.Equal(t, int64(400000), envelope.FlowMaxTotalTokens)
	assert.Equal(t, 4, envelope.TaskCount)
}

func TestInspectAgentFlowWarnsAboutGlobalSkillRefs(t *testing.T) {
	files := mixedFlowFiles()
	files[2] = entry{path: "flows/qc-review/flow.yaml", body: strings.Replace(agentFlowSource, "skills: [local/cell-qc-tables]", "skills: [global/cell-qc-tables]", 1)}
	result := inspectFiles(t, files)
	assert.Contains(t, warningCodes(result.Warnings), WarningFlowRefsGlobalSkill)
}

func TestInspectAgentFlowRejectsUnknownLocalSkillRef(t *testing.T) {
	files := mixedFlowFiles()
	files[2] = entry{path: "flows/qc-review/flow.yaml", body: strings.Replace(agentFlowSource, "skills: [local/cell-qc-tables]", "skills: [local/ghost]", 1)}
	_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	requireCode(t, err, CodePackRefUnknown)
}

func TestInspectGraphFlowWarnsAboutGlobalRoleRefs(t *testing.T) {
	files := mixedPackFiles()
	manifest := strings.Replace(mixedManifest, "roles: [qc-reviewer]", "roles: []", 1)
	manifest = strings.Replace(manifest, "    scope: flow", "    scope: global", 1)
	files[0] = entry{path: "pack.yaml", body: manifest}
	files[2] = entry{path: "flows/qc-review/graph.yaml", body: strings.ReplaceAll(graphFlow, "local/qc-reviewer", "global/qc-reviewer")}
	result := inspectFiles(t, files)
	assert.Contains(t, warningCodes(result.Warnings), WarningFlowRefsGlobalRole)
}

func TestInspectGraphFlowRejectsUnknownLocalRoleRef(t *testing.T) {
	files := mixedPackFiles()
	files[2] = entry{path: "flows/qc-review/graph.yaml", body: strings.Replace(graphFlow, "local/qc-reviewer", "local/ghost", 1)}
	_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	requireCode(t, err, CodePackRefUnknown)
}

const flowOnlyManifest = `schema_version: 1
id: seqyuan/qc-flow
name: QC Flow
version: 1.0.0
model_requirements:
  defaults:
    capabilities: [tool_use]
flows:
  - id: qc-review
    path: flows/qc-review/flow.yaml
    format: flow
skills:
  - id: cell-qc-tables
    path: skills/cell-qc-tables/
`

func TestInspectFlowOnlyPack(t *testing.T) {
	// No roles: the flow's bare handle resolves against the Worker's global
	// Roles at freeze time, which is exactly what a Flow-only pack ships.
	files := []entry{
		{path: "pack.yaml", body: flowOnlyManifest},
		{path: "flows/qc-review/flow.yaml", body: agentFlowSource},
		{path: "skills/cell-qc-tables/skill.json", body: skillManifestJSON},
		{path: "skills/cell-qc-tables/SKILL.md", body: "# Cell QC Tables\n"},
	}
	result := inspectFiles(t, files)
	assert.Empty(t, result.Roles)
	assert.Contains(t, warningCodes(result.Warnings), WarningFlowOnlyPack)
	assert.NotContains(t, warningCodes(result.Warnings), WarningRoleOnlyPack)
	require.Len(t, result.Flows, 1)
	assert.Equal(t, FlowFormatFlow, result.Flows[0].Format)
}

func TestInspectRoleOnlyPack(t *testing.T) {
	files := mixedPackFiles()
	manifest := mixedManifest
	manifest = strings.Replace(manifest, `flows:
  - id: qc-review
    path: flows/qc-review/graph.yaml
    format: graph
    roles: [qc-reviewer]
`, "", 1)
	manifest = strings.Replace(manifest, "    scope: flow", "    scope: global", 1)
	files[0] = entry{path: "pack.yaml", body: manifest}
	files = append(files[:2], files[3:]...) // drop the graph flow file

	result := inspectFiles(t, files)
	assert.Empty(t, result.Flows)
	require.Len(t, result.Roles, 1)
	assert.Equal(t, RoleScopeGlobal, result.Roles[0].Scope)
	assert.Contains(t, warningCodes(result.Warnings), WarningRoleOnlyPack)
	assert.NotContains(t, warningCodes(result.Warnings), WarningFlowOnlyPack)
}

func TestInspectRoleOnlyPackRejectsFlowScopedRole(t *testing.T) {
	files := mixedPackFiles()
	manifest := strings.Replace(mixedManifest, `flows:
  - id: qc-review
    path: flows/qc-review/graph.yaml
    format: graph
    roles: [qc-reviewer]
`, "", 1)
	files[0] = entry{path: "pack.yaml", body: manifest}
	_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	requireCode(t, err, CodePackRoleInvalid)
}

func TestInspectRejectsUnreferencedFlowRole(t *testing.T) {
	// report-writer is flow-scoped but neither a flow entry nor a flow task
	// references it: the pack would install an unreachable Role.
	manifest := strings.Replace(mixedManifest, `roles:
  - handle: qc-reviewer
    path: roles/qc-reviewer/role.md
    scope: flow
`, `roles:
  - handle: qc-reviewer
    path: roles/qc-reviewer/role.md
    scope: flow
  - handle: report-writer
    path: roles/report-writer/role.md
    scope: flow
`, 1)
	files := mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: manifest}
	files = append(files, entry{
		path: "roles/report-writer/role.md",
		body: strings.Replace(reviewerRole, "handle: qc-reviewer", "handle: report-writer", 1),
	})
	_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	requireCode(t, err, CodePackRoleUnreferenced)
}

func TestInspectRejectsUndeclaredFlowRole(t *testing.T) {
	files := mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: strings.Replace(mixedManifest, "roles: [qc-reviewer]", "roles: [ghost]", 1)}
	_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	requireCode(t, err, CodePackRefUnknown)
}

func TestInspectManifestErrors(t *testing.T) {
	cases := map[string]struct {
		manifest string
		code     string
	}{
		"schema":        {manifest: strings.Replace(mixedManifest, "schema_version: 1", "schema_version: 2", 1), code: CodePackSchemaUnsupported},
		"unknown-field": {manifest: mixedManifest + "bogus_field: true\n", code: CodePackManifestUnknownField},
		"identity":      {manifest: strings.Replace(mixedManifest, "id: seqyuan/cell-qc-review", "id: not-a-pack-id", 1), code: CodePackIdentityInvalid},
		"version":       {manifest: strings.Replace(mixedManifest, "version: 1.3.0", "version: latest", 1), code: CodePackIdentityInvalid},
		"empty": {manifest: `schema_version: 1
id: seqyuan/empty
name: Empty
version: 1.0.0
flows: []
roles: []
`, code: CodePackEmpty},
		"duplicate-flow": {manifest: strings.Replace(mixedManifest, "    roles: [qc-reviewer]\n", "    roles: [qc-reviewer]\n  - id: qc-review\n    path: flows/qc-review/graph.yaml\n    format: graph\n", 1),
			code: CodePackDuplicateID},
		"duplicate-role": {manifest: strings.Replace(mixedManifest, `skills:`, `  - handle: qc-reviewer
    path: roles/qc-reviewer/role.md
    scope: global
skills:`, 1), code: CodePackDuplicateID},
		"duplicate-skill": {manifest: strings.Replace(mixedManifest, "  - ref: bio-workspace@0.1.0", "  - id: cell-qc-tables\n    path: skills/cell-qc-tables/\n  - ref: bio-workspace@0.1.0", 1),
			code: CodePackDuplicateID},
		"missing-format": {manifest: strings.Replace(mixedManifest, "    format: graph\n", "", 1), code: CodePackFlowInvalid},
		"bad-format":     {manifest: strings.Replace(mixedManifest, "format: graph", "format: dag", 1), code: CodePackFlowInvalid},
		"anonymous-flow": {manifest: strings.Replace(mixedManifest, "id: qc-review\n    path:", "path:", 1), code: CodePackFlowInvalid},
		"capability": {manifest: strings.Replace(mixedManifest, "capabilities: [tool_use, thinking]", "capabilities: [telepathy]", 1),
			code: CodePackCapabilityUnknown},
		"input-type":    {manifest: strings.Replace(mixedManifest, "type: path", "type: dataset", 1), code: CodePackManifestInvalid},
		"bad-requires":  {manifest: strings.Replace(mixedManifest, "requires: []", "requires: [not a ref]", 1), code: CodePackManifestInvalid},
		"signature-alg": {manifest: strings.Replace(mixedManifest, "requires: []", "requires: []\nsignature:\n  algorithm: rsa\n  path: pack.sig\n", 1), code: CodePackSignatureUnsupported},
		"signature-missing": {manifest: strings.Replace(mixedManifest, "requires: []", "requires: []\nsignature:\n  algorithm: ed25519\n  path: pack.sig\n", 1),
			code: CodePackPathMissing},
		"missing-readme": {manifest: strings.Replace(mixedManifest, "readme: README.md", "readme: MISSING.md", 1), code: CodePackPathMissing},
		"model-req-role": {manifest: strings.Replace(mixedManifest, "    qc-reviewer:\n      capabilities: [tool_use, thinking]", "    ghost:\n      capabilities: [tool_use]", 1),
			code: CodePackRefUnknown},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			files := mixedPackFiles()
			files[0] = entry{path: "pack.yaml", body: testCase.manifest}
			_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
			requireCode(t, err, testCase.code)
		})
	}
}

func TestInspectSignatureIsReportedButNeverVerified(t *testing.T) {
	files := mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: strings.Replace(mixedManifest, "requires: []", "requires: []\nsignature:\n  algorithm: ed25519\n  path: pack.sig\n", 1)}
	files = append(files, entry{path: "pack.sig", body: "not a real signature"})

	result := inspectFiles(t, files)
	assert.True(t, result.Signature.Present)
	require.NotNil(t, result.Signature.Algorithm)
	assert.Equal(t, "ed25519", *result.Signature.Algorithm)
	assert.False(t, result.Signature.Verified)
	assert.False(t, result.Signed)
	assert.Contains(t, warningCodes(result.Warnings), WarningSignatureUnverified)
	assert.NotContains(t, warningCodes(result.Warnings), WarningUnsignedPack)
}

func TestParseManifestAcceptsExternalMCPRefs(t *testing.T) {
	manifest, err := ParseManifest([]byte(strings.Replace(mixedManifest, "requires: []", "mcp:\n  - ref: pubmed@1\nrequires: []", 1)))
	require.NoError(t, err)
	require.Len(t, manifest.MCP, 1)
	assert.Equal(t, "pubmed@1", manifest.MCP[0].Ref)
}

func TestParseManifestDedupesMCPRefsDeterministically(t *testing.T) {
	source := strings.Replace(mixedManifest, "requires: []",
		"mcp:\n  - ref: pubmed@1\n  - ref: bio-tools@0.2.0\n  - ref: pubmed@1\nrequires: []", 1)

	first, err := ParseManifest([]byte(source))
	require.NoError(t, err)
	require.Len(t, first.MCP, 2)
	assert.Equal(t, []MCPEntry{{Ref: "bio-tools@0.2.0"}, {Ref: "pubmed@1"}}, first.MCP)

	// The same manifest always parses to the same sorted, de-duplicated set.
	second, err := ParseManifest([]byte(source))
	require.NoError(t, err)
	assert.Equal(t, first.MCP, second.MCP)
}

func TestManifestRejectsInvalidMCPDeclarations(t *testing.T) {
	cases := map[string]struct {
		mcp  string
		code string
	}{
		"blank-ref":      {mcp: "mcp:\n  - ref: \"\"\n", code: CodePackManifestInvalid},
		"missing-ref":    {mcp: "mcp:\n  - {}\n", code: CodePackManifestInvalid},
		"no-version":     {mcp: "mcp:\n  - ref: pubmed\n", code: CodePackManifestInvalid},
		"uppercase-name": {mcp: "mcp:\n  - ref: PubMed@1\n", code: CodePackManifestInvalid},
		"blank-version":  {mcp: "mcp:\n  - ref: pubmed@\n", code: CodePackManifestInvalid},
		"inline-command": {mcp: "mcp:\n  - ref: pubmed@1\n    command: npx\n", code: CodePackManifestUnknownField},
		"inline-args":    {mcp: "mcp:\n  - ref: pubmed@1\n    args: [-y, pubmed]\n", code: CodePackManifestUnknownField},
		"inline-env":     {mcp: "mcp:\n  - ref: pubmed@1\n    env: {API_KEY: x}\n", code: CodePackManifestUnknownField},
		"inline-block":   {mcp: "mcp:\n  - ref: pubmed@1\n    inline: {command: npx}\n", code: CodePackManifestUnknownField},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(strings.Replace(mixedManifest, "requires: []", testCase.mcp+"requires: []", 1)))
			requireCode(t, err, testCase.code)
		})
	}
}

func TestInspectReportsExternalMCPRefs(t *testing.T) {
	files := mixedPackFiles()
	files[0] = entry{path: "pack.yaml", body: strings.Replace(mixedManifest, "requires: []",
		"mcp:\n  - ref: pubmed@1\n  - ref: bio-tools@0.2.0\nrequires: []", 1)}

	result := inspectFiles(t, files)
	assert.Equal(t, []MCPSummary{{Ref: "bio-tools@0.2.0"}, {Ref: "pubmed@1"}}, result.MCP)
	assert.Equal(t, []string{"bio-tools@0.2.0", "pubmed@1"}, result.CapabilityEnvelope.RequiresMCP)

	// One info warning per ref, never an error.
	mcpWarnings := make([]string, 0, 2)
	for _, warning := range result.Warnings {
		if warning.Code != WarningExternalMCPRef {
			continue
		}
		assert.Equal(t, SeverityInfo, warning.Severity)
		mcpWarnings = append(mcpWarnings, warning.Message)
	}
	require.Len(t, mcpWarnings, 2)
	assert.Contains(t, mcpWarnings[0], "bio-tools@0.2.0")
	assert.Contains(t, mcpWarnings[1], "pubmed@1")
}

func TestInspectWithoutMCPRefsReportsEmptySlices(t *testing.T) {
	result := inspectFiles(t, mixedPackFiles())
	assert.Empty(t, result.MCP)
	assert.NotNil(t, result.MCP)
	assert.Empty(t, result.CapabilityEnvelope.RequiresMCP)
	assert.NotContains(t, warningCodes(result.Warnings), WarningExternalMCPRef)
}

func TestParseManifestRejectsEmptyAndMultiDocument(t *testing.T) {
	_, err := ParseManifest(nil)
	requireCode(t, err, CodePackManifestInvalid)
	_, err = ParseManifest([]byte(mixedManifest + "---\nid: other/pack\n"))
	requireCode(t, err, CodePackManifestInvalid)
	_, err = ParseManifest([]byte("schema_version: 1\n"))
	requireCode(t, err, CodePackIdentityInvalid)
}

func TestParseVendoredSkillRequiresReadableContent(t *testing.T) {
	files := mixedPackFiles()
	files[4] = entry{path: "skills/cell-qc-tables/skill.json", body: "{not json"}
	_, err := Inspect(Request{Source: Source{Path: writeDirectoryPack(t, files)}})
	requireCode(t, err, CodePackSkillInvalid)

	files = mixedPackFiles()
	files = files[:5] // keep skill.json only, drop SKILL.md
	result := inspectFiles(t, files)
	require.Len(t, result.Skills, 2)
	assert.Equal(t, SkillKindVendored, result.Skills[0].Kind)
	assert.Zero(t, result.Skills[0].PromptBytes)
}
