package heroesource_test

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validHeroMarkdown = `---
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

func TestParsePortableHeroMarkdown(t *testing.T) {
	doc, err := heroesource.Parse([]byte(validHeroMarkdown))
	require.NoError(t, err)
	assert.Equal(t, 1, doc.SchemaVersion)
	assert.Equal(t, "security-reviewer", doc.Handle)
	assert.Equal(t, "openai-main/gpt-5.4", doc.Model.Ref)
	assert.Equal(t, domain.ThinkingMedium, doc.Model.ThinkingEffort)
	require.Len(t, doc.Skills, 1)
	assert.Equal(t, "code-review", doc.Skills[0].ID)
	assert.Equal(t, []string{"grep", "read"}, doc.AllowedTools)
	assert.Equal(t, []domain.HeroContextMode{domain.HeroContextFresh, domain.HeroContextRoom}, doc.Context.AllowedModes)
	assert.Equal(t, "Review the supplied evidence independently.\n\nDistinguish evidence from assumptions.", doc.Prompt)

	digest, err := heroesource.SourceDigest(doc)
	require.NoError(t, err)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, digest)
}

func TestEncodeRoundTripsHeroMarkdown(t *testing.T) {
	document, err := heroesource.Parse([]byte(validHeroMarkdown))
	require.NoError(t, err)
	encoded, err := heroesource.Encode(document)
	require.NoError(t, err)
	roundTripped, err := heroesource.Parse(encoded)
	require.NoError(t, err)
	assert.Equal(t, document, roundTripped)
}

func TestParseNormalizesCRLFAndCollectionOrderForDigest(t *testing.T) {
	first, err := heroesource.Parse([]byte(validHeroMarkdown))
	require.NoError(t, err)
	secondInput := strings.ReplaceAll(validHeroMarkdown, "allowedTools: [grep, read]", "allowedTools: [read, grep]")
	secondInput = strings.ReplaceAll(secondInput, "allowedModes: [fresh, room]", "allowedModes: [room, fresh]")
	secondInput = strings.ReplaceAll(secondInput, "\n", "\r\n")
	second, err := heroesource.Parse([]byte(secondInput))
	require.NoError(t, err)
	firstDigest, err := heroesource.SourceDigest(first)
	require.NoError(t, err)
	secondDigest, err := heroesource.SourceDigest(second)
	require.NoError(t, err)
	assert.Equal(t, firstDigest, secondDigest)
	assert.Equal(t, first.Prompt, second.Prompt)
}

func TestParseRejectsInvalidHeroMarkdown(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		message string
	}{
		{name: "opening delimiter", input: "schemaVersion: 1", message: "opening frontmatter delimiter"},
		{name: "closing delimiter", input: "---\nschemaVersion: 1\n", message: "closing frontmatter delimiter"},
		{name: "empty prompt", input: strings.Replace(validHeroMarkdown, "Review the supplied evidence independently.\n\nDistinguish evidence from assumptions.\n", "", 1), message: "prompt"},
		{name: "unknown field", input: strings.Replace(validHeroMarkdown, "schemaVersion: 1", "schemaVersion: 1\nunknownField: true", 1), message: "field unknownField not found"},
		{name: "schema", input: strings.Replace(validHeroMarkdown, "schemaVersion: 1", "schemaVersion: 2", 1), message: "schemaVersion"},
		{name: "model ref", input: strings.Replace(validHeroMarkdown, "openai-main/gpt-5.4", "gpt-5.4", 1), message: "model.ref"},
		{name: "skill mode", input: strings.Replace(validHeroMarkdown, "mode: preload", "mode: execute", 1), message: "skill mode"},
		{name: "duplicate skill", input: strings.Replace(validHeroMarkdown, "  - id: code-review\n    mode: preload", "  - id: code-review\n    mode: preload\n  - id: code-review\n    mode: available", 1), message: "duplicate skill"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := heroesource.Parse([]byte(test.input))
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestParseValidatesHeroAuthority(t *testing.T) {
	cases := []struct {
		name      string
		authority string
		want      heroesource.Authority
		domain    domain.HeroAuthority
	}{
		{name: "read only", authority: "read_only", want: heroesource.AuthorityReadOnly, domain: domain.HeroAuthorityReadOnly},
		{name: "write workspace", authority: "write_workspace", want: heroesource.AuthorityWriteWorkspace, domain: domain.HeroAuthorityMutation},
		{name: "legacy mutation alias", authority: "mutation", want: heroesource.AuthorityMutation, domain: domain.HeroAuthorityMutation},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document, err := heroesource.Parse([]byte(strings.Replace(validHeroMarkdown, "authority: read_only", "authority: "+test.authority, 1)))
			require.NoError(t, err)
			// Authority is preserved verbatim so an existing revision keeps its digest.
			assert.Equal(t, test.want, document.Authority)
			assert.Equal(t, test.domain, document.Authority.Domain())
		})
	}

	// An omitted authority is tolerated for legacy files and fails closed.
	omitted, err := heroesource.Parse([]byte(strings.Replace(validHeroMarkdown, "authority: read_only\n", "", 1)))
	require.NoError(t, err)
	assert.Equal(t, heroesource.Authority(""), omitted.Authority)
	assert.Equal(t, domain.HeroAuthorityReadOnly, omitted.Authority.Domain())

	// Unknown values are rejected.
	_, err = heroesource.Parse([]byte(strings.Replace(validHeroMarkdown, "authority: read_only", "authority: admin", 1)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authority")
}

// TestHeroAuthorityIsDigestStable pins the compatibility invariant behind the
// Authority type: parsing and re-encoding a Hero must not rewrite the authority
// field, because ReadHeroRevision re-verifies the published digest.
func TestHeroAuthorityIsDigestStable(t *testing.T) {
	sources := []string{
		validHeroMarkdown,
		strings.Replace(validHeroMarkdown, "authority: read_only", "authority: write_workspace", 1),
		strings.Replace(validHeroMarkdown, "authority: read_only", "authority: mutation", 1),
		strings.Replace(validHeroMarkdown, "authority: read_only\n", "", 1),
	}
	for _, source := range sources {
		document, err := heroesource.Parse([]byte(source))
		require.NoError(t, err)
		before, err := heroesource.SourceDigest(document)
		require.NoError(t, err)
		// Simulate the publish/read cycle: encode the draft, then re-parse the
		// revision bytes and recompute the digest.
		encoded, err := heroesource.Encode(document)
		require.NoError(t, err)
		reparsed, err := heroesource.Parse(encoded)
		require.NoError(t, err)
		after, err := heroesource.SourceDigest(reparsed)
		require.NoError(t, err)
		assert.Equal(t, document.Authority, reparsed.Authority)
		assert.Equal(t, before, after)
	}
}

func TestParseRejectsOversizedHeroFile(t *testing.T) {
	_, err := heroesource.Parse([]byte("---\n" + strings.Repeat("x", 128*1024) + "\n---\nprompt"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "128 KiB")
}
