package rolesource_test

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validRoleMarkdown = `---
schemaVersion: 1
handle: security-reviewer
name: Security Reviewer
description: Reviews security-sensitive changes
positioning: Use after authentication changes.
icon: shield-check
color: "#b91c1c"
model:
  ref: openai-main/gpt-5.4
  thinkingEffort: medium
  fallbacks: []
skills:
  - id: code-review
    mode: preload
authority: read_only
permissionCeiling: ask
allowedTools: [grep, read]
context:
  defaultMode: room
  allowedModes: [fresh, room]
  ownExecutionContinuity: none
delegation:
  admission: approval_required
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
Review the supplied evidence independently.

Distinguish evidence from assumptions.
`

func TestParsePortableRoleMarkdown(t *testing.T) {
	doc, err := rolesource.Parse([]byte(validRoleMarkdown))
	require.NoError(t, err)
	assert.Equal(t, 1, doc.SchemaVersion)
	assert.Equal(t, "security-reviewer", doc.Handle)
	assert.Equal(t, "openai-main/gpt-5.4", doc.Model.Ref)
	assert.Equal(t, domain.ThinkingMedium, doc.Model.ThinkingEffort)
	require.Len(t, doc.Skills, 1)
	assert.Equal(t, "code-review", doc.Skills[0].ID)
	assert.Equal(t, []string{"grep", "read"}, doc.AllowedTools)
	assert.Equal(t, []domain.RoleContextMode{domain.RoleContextFresh, domain.RoleContextRoom}, doc.Context.AllowedModes)
	assert.Equal(t, "Review the supplied evidence independently.\n\nDistinguish evidence from assumptions.", doc.Prompt)

	digest, err := rolesource.SourceDigest(doc)
	require.NoError(t, err)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, digest)
}

func TestEncodeRoundTripsRoleMarkdown(t *testing.T) {
	document, err := rolesource.Parse([]byte(validRoleMarkdown))
	require.NoError(t, err)
	encoded, err := rolesource.Encode(document)
	require.NoError(t, err)
	roundTripped, err := rolesource.Parse(encoded)
	require.NoError(t, err)
	assert.Equal(t, document, roundTripped)
}

func TestParseNormalizesCRLFAndCollectionOrderForDigest(t *testing.T) {
	first, err := rolesource.Parse([]byte(validRoleMarkdown))
	require.NoError(t, err)
	secondInput := strings.ReplaceAll(validRoleMarkdown, "allowedTools: [grep, read]", "allowedTools: [read, grep]")
	secondInput = strings.ReplaceAll(secondInput, "allowedModes: [fresh, room]", "allowedModes: [room, fresh]")
	secondInput = strings.ReplaceAll(secondInput, "\n", "\r\n")
	second, err := rolesource.Parse([]byte(secondInput))
	require.NoError(t, err)
	firstDigest, err := rolesource.SourceDigest(first)
	require.NoError(t, err)
	secondDigest, err := rolesource.SourceDigest(second)
	require.NoError(t, err)
	assert.Equal(t, firstDigest, secondDigest)
	assert.Equal(t, first.Prompt, second.Prompt)
}

func TestParseRejectsInvalidRoleMarkdown(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		message string
	}{
		{name: "opening delimiter", input: "schemaVersion: 1", message: "opening frontmatter delimiter"},
		{name: "closing delimiter", input: "---\nschemaVersion: 1\n", message: "closing frontmatter delimiter"},
		{name: "empty prompt", input: strings.Replace(validRoleMarkdown, "Review the supplied evidence independently.\n\nDistinguish evidence from assumptions.\n", "", 1), message: "prompt"},
		{name: "unknown field", input: strings.Replace(validRoleMarkdown, "schemaVersion: 1", "schemaVersion: 1\nunknownField: true", 1), message: "field unknownField not found"},
		{name: "schema", input: strings.Replace(validRoleMarkdown, "schemaVersion: 1", "schemaVersion: 2", 1), message: "schemaVersion"},
		{name: "model ref", input: strings.Replace(validRoleMarkdown, "openai-main/gpt-5.4", "gpt-5.4", 1), message: "model.ref"},
		{name: "skill mode", input: strings.Replace(validRoleMarkdown, "mode: preload", "mode: execute", 1), message: "skill mode"},
		{name: "duplicate skill", input: strings.Replace(validRoleMarkdown, "  - id: code-review\n    mode: preload", "  - id: code-review\n    mode: preload\n  - id: code-review\n    mode: available", 1), message: "duplicate skill"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := rolesource.Parse([]byte(test.input))
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestParseRejectsOversizedRoleFile(t *testing.T) {
	_, err := rolesource.Parse([]byte("---\n" + strings.Repeat("x", 128*1024) + "\n---\nprompt"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "128 KiB")
}
