package heroesource_test

// Hero acceptanceKinds: the leaf kinds a Hero says it produces artifacts for.
// The list is normalised like allowedTools (trim, sort, dedupe, drop empties),
// it is only checked at write time, and an empty list means "the kernel's whole
// vocabulary" - which is why every Hero file written before this field existed
// keeps its exact behaviour and its exact digest.

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func heroMarkdownWithAcceptanceKinds(section string) string {
	return strings.Replace(validHeroMarkdown, "allowedTools: [grep, read]", "allowedTools: [grep, read]\n"+section, 1)
}

// The list is normalised the same way allowedTools is, so authoring order and
// stray whitespace never change the source digest.
func TestParseNormalizesAcceptanceKinds(t *testing.T) {
	unordered, err := heroesource.Parse([]byte(heroMarkdownWithAcceptanceKinds(
		"acceptanceKinds: [' json-valid', csv-columns, 'json-valid ', '']")))
	require.NoError(t, err)
	assert.Equal(t, []string{"csv-columns", "json-valid"}, unordered.AcceptanceKinds)

	ordered, err := heroesource.Parse([]byte(heroMarkdownWithAcceptanceKinds(
		"acceptanceKinds: [csv-columns, json-valid]")))
	require.NoError(t, err)

	unorderedDigest, err := heroesource.SourceDigest(unordered)
	require.NoError(t, err)
	orderedDigest, err := heroesource.SourceDigest(ordered)
	require.NoError(t, err)
	assert.Equal(t, orderedDigest, unorderedDigest)
}

// An absent list and an explicitly empty one are the same document: both mean
// "the whole vocabulary", so both must keep the digest a Hero had before the
// field existed.
func TestEmptyAcceptanceKindsIsDigestStable(t *testing.T) {
	absent, err := heroesource.Parse([]byte(validHeroMarkdown))
	require.NoError(t, err)
	require.Empty(t, absent.AcceptanceKinds)
	absentDigest, err := heroesource.SourceDigest(absent)
	require.NoError(t, err)

	for _, empty := range []string{"acceptanceKinds: []", "acceptanceKinds: ['', '  ']"} {
		document, err := heroesource.Parse([]byte(heroMarkdownWithAcceptanceKinds(empty)))
		require.NoError(t, err)
		assert.Empty(t, document.AcceptanceKinds, "%s must normalize to no restriction", empty)
		emptyDigest, err := heroesource.SourceDigest(document)
		require.NoError(t, err)
		assert.Equal(t, absentDigest, emptyDigest)
	}

	// A restricted Hero is a different document, so its digest moves.
	restricted, err := heroesource.Parse([]byte(heroMarkdownWithAcceptanceKinds("acceptanceKinds: [json-valid]")))
	require.NoError(t, err)
	restrictedDigest, err := heroesource.SourceDigest(restricted)
	require.NoError(t, err)
	assert.NotEqual(t, absentDigest, restrictedDigest)
}

// A kind the kernel cannot decide is refused at write time, naming the field.
// The read path stays lenient so an already-published revision still loads.
func TestValidateForWriteRejectsUnknownAcceptanceKind(t *testing.T) {
	input := heroMarkdownWithAcceptanceKinds("acceptanceKinds: [json-valid, tests-passed]")
	document, err := heroesource.Parse([]byte(input))
	require.NoError(t, err, "the read path must keep loading the document")
	_, err = heroesource.SourceDigest(document)
	require.NoError(t, err)

	err = heroesource.ValidateForWrite(document)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acceptanceKinds[1]")
	assert.Contains(t, err.Error(), "tests-passed")
	assert.Contains(t, err.Error(), "json-valid")
}

func TestValidateForWriteAcceptsKnownAcceptanceKinds(t *testing.T) {
	document, err := heroesource.Parse([]byte(heroMarkdownWithAcceptanceKinds(
		"acceptanceKinds: [exists, non-empty, csv-columns, json-valid, artifact-published]")))
	require.NoError(t, err)
	require.NoError(t, heroesource.ValidateForWrite(document))

	// No restriction at all stays valid: it is the pre-existing shape.
	unrestricted, err := heroesource.Parse([]byte(validHeroMarkdown))
	require.NoError(t, err)
	require.NoError(t, heroesource.ValidateForWrite(unrestricted))
}
