package systemprompt

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func digestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func TestComposeConcatenatesInOrderWithoutRewritingBytes(t *testing.T) {
	segments := []Segment{
		{ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Text: "base\n\n"},
		{ID: "context", Kind: domain.PromptSectionContext, Source: "project:MEMORY.md", Text: "\n## Memory\n\nbody"},
		{ID: "skills", Kind: domain.PromptSectionSkillsCatalog, Source: "skills:catalog", Text: "\n<catalog/>\n"},
	}

	text, sections := Compose(segments)

	assert.Equal(t, "base\n\n\n## Memory\n\nbody\n<catalog/>\n", text,
		"composition must never add, trim, or re-separate bytes")
	require.Len(t, sections, 3)
	assert.Equal(t, []string{"base", "context", "skills"}, []string{sections[0].ID, sections[1].ID, sections[2].ID})
	assert.Equal(t, domain.PromptSectionBase, sections[0].Kind)
	assert.Equal(t, domain.PromptSectionContext, sections[1].Kind)
	assert.Equal(t, domain.PromptSectionSkillsCatalog, sections[2].Kind)
}

func TestComposeRecordsDigestAndByteCountOfExactContribution(t *testing.T) {
	body := "\n## Project Instructions - AGENTS.md\n\nrules"
	text, sections := Compose([]Segment{
		{ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Text: "base"},
		{ID: "project.agents", Kind: domain.PromptSectionContext, Source: "project:AGENTS.md", Text: body},
	})

	require.Len(t, sections, 2)
	assert.Equal(t, len(body), sections[1].Bytes)
	assert.Equal(t, digestOf(body), sections[1].Digest)
	assert.Equal(t, len(text), sections[0].Bytes+sections[1].Bytes)
}

// A section that contributed nothing must not be advertised: a reader must be
// able to trust that every recorded section is part of the composed prompt.
func TestComposeOmitsEmptySegments(t *testing.T) {
	text, sections := Compose([]Segment{
		{ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Text: "base"},
		{ID: "absent", Kind: domain.PromptSectionContext, Source: "project:MEMORY.md", Text: ""},
	})

	assert.Equal(t, "base", text)
	require.Len(t, sections, 1)
	assert.Equal(t, "base", sections[0].ID)
}

func TestComposeWithNoSegmentsYieldsEmptyPrompt(t *testing.T) {
	text, sections := Compose(nil)
	assert.Empty(t, text)
	assert.Empty(t, sections)
}

func TestSkillSegmentWrapperIsStable(t *testing.T) {
	segment := SkillSegment("reporting", "how to report", "role:analyst@2")

	assert.Equal(t, "\n\n<preloaded_skill id=\"reporting\">\nhow to report\n</preloaded_skill>", segment.Text)
	assert.Equal(t, domain.PromptSectionSkillPreload, segment.Kind)
	assert.Equal(t, "reporting", segment.SkillID)
	assert.Equal(t, "role:analyst@2", segment.Source)
}

func TestSectionsDigestIsEmptyForNoSections(t *testing.T) {
	digest, err := SectionsDigest(nil)
	require.NoError(t, err)
	assert.Empty(t, digest)
}

func TestSectionsDigestDetectsEveryIdentityChange(t *testing.T) {
	base := []domain.PromptSection{{
		ID: "role.definition", Kind: domain.PromptSectionRole, Source: "role:analyst@2", Bytes: 12, Digest: "abc",
	}}
	baseline, err := SectionsDigest(base)
	require.NoError(t, err)
	require.NotEmpty(t, baseline)

	mutations := map[string]func(sections []domain.PromptSection){
		"id":      func(s []domain.PromptSection) { s[0].ID = "other" },
		"kind":    func(s []domain.PromptSection) { s[0].Kind = domain.PromptSectionContext },
		"source":  func(s []domain.PromptSection) { s[0].Source = "role:analyst@3" },
		"bytes":   func(s []domain.PromptSection) { s[0].Bytes = 13 },
		"digest":  func(s []domain.PromptSection) { s[0].Digest = "abd" },
		"skillId": func(s []domain.PromptSection) { s[0].SkillID = "reporting" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := append([]domain.PromptSection(nil), base...)
			mutate(changed)
			digest, err := SectionsDigest(changed)
			require.NoError(t, err)
			assert.NotEqual(t, baseline, digest, "%s must be covered by the sections digest", name)
		})
	}

	// Order is identity too: the same sections in a different order is a
	// different composition.
	reordered, err := SectionsDigest([]domain.PromptSection{{
		ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Bytes: 1, Digest: "d",
	}, base[0]})
	require.NoError(t, err)
	assert.NotEqual(t, baseline, reordered)
}

func TestSectionsDigestIsStableAcrossRepeatedCalls(t *testing.T) {
	sections := []domain.PromptSection{{
		ID: "skills.catalog", Kind: domain.PromptSectionSkillsCatalog, Source: "skills:catalog",
		Bytes: 4096, Digest: strings.Repeat("a", 64),
	}}
	first, err := SectionsDigest(sections)
	require.NoError(t, err)
	second, err := SectionsDigest(sections)
	require.NoError(t, err)
	assert.Equal(t, first, second)
}
