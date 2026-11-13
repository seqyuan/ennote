package graphsource

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validGraph = `schema_version: 1
id: rna-seq
name: RNA-seq
tasks:
  prepare_reference:
    name: Prepare reference
    hero: local/reference-preparer
    goal: |
      Prepare the genome index and annotation inputs.
  align_1:
    name: Align batch 1
    model: anthropic/claude-sonnet-4
    thinking: high
    skills:
      - local/alignment
      - global/bioinformatics-report
    goal: |
      Align reads from batch 1.
  align_2:
    name: Align batch 2
    model: anthropic/claude-sonnet-4
    thinking: high
    goal: Align reads from batch 2.
  align_3:
    name: Align batch 3
    model: anthropic/claude-sonnet-4
    goal: Align reads from batch 3.
  merge:
    name: Merge results
    model: anthropic/claude-sonnet-4
    thinking: medium
    skills:
      - global/bioinformatics-report
    goal: Merge and review all alignment results.
graph:
  prepare_reference: []
  align_1: [prepare_reference]
  align_2: [prepare_reference]
  align_3: [prepare_reference]
  merge: [align_1, align_2, align_3]
`

func TestParseAllowsEmptyEditableDraft(t *testing.T) {
	document, err := Parse([]byte("schema_version: 1\nid: empty-graph\nname: Empty Graph\ntasks: {}\ngraph: {}\n"))
	require.NoError(t, err)
	assert.Empty(t, document.Tasks)
	assert.Empty(t, document.Graph)
}

func TestParseTaskFirstGraph(t *testing.T) {
	document, err := Parse([]byte(validGraph))
	require.NoError(t, err)
	assert.Equal(t, "RNA-seq", document.Name)
	assert.Equal(t, "local/reference-preparer", document.Tasks["prepare_reference"].Hero)
	assert.Equal(t, "anthropic/claude-sonnet-4", document.Tasks["align_1"].Model)
	assert.Equal(t, []string{"align_1", "align_2", "align_3"}, document.Graph["merge"])
}

func TestParseRejectsHeroAndInlineConfiguration(t *testing.T) {
	input := strings.Replace(validGraph, "hero: local/reference-preparer", "hero: local/reference-preparer\n    model: anthropic/claude-sonnet-4", 1)
	_, err := Parse([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot declare model, thinking, or skills")
}

func TestParseRequiresInlineModel(t *testing.T) {
	input := strings.Replace(validGraph, "    model: anthropic/claude-sonnet-4\n    thinking: high\n    skills:", "    thinking: high\n    skills:", 1)
	_, err := Parse([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must use provider-name/model-name")
}

func TestParseRequiresExplicitScopedReferences(t *testing.T) {
	input := strings.Replace(validGraph, "local/alignment", "alignment", 1)
	_, err := Parse([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local/<id> or global/<id>")
}

func TestParseRequiresMatchingTaskAndGraphKeys(t *testing.T) {
	input := strings.Replace(validGraph, "  align_3: [prepare_reference]\n", "", 1)
	_, err := Parse([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly the same Task ids")
}

func TestParseRejectsUnknownDependency(t *testing.T) {
	input := strings.Replace(validGraph, "merge: [align_1, align_2, align_3]", "merge: [align_1, missing]", 1)
	_, err := Parse([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "depends on unknown Task")
}

func TestParseRejectsCycle(t *testing.T) {
	input := strings.Replace(validGraph, "prepare_reference: []", "prepare_reference: [merge]", 1)
	_, err := Parse([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dependency cycle")
}

func TestParseRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(validGraph, "name: RNA-seq", "name: RNA-seq\nunknown: true", 1)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field unknown not found")

	_, err = Parse([]byte(validGraph + "---\nid: another\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one YAML document")
}

func TestDigestIsStableAcrossMapAndDependencyOrder(t *testing.T) {
	first, err := Parse([]byte(validGraph))
	require.NoError(t, err)
	secondInput := strings.Replace(validGraph, "merge: [align_1, align_2, align_3]", "merge: [align_3, align_1, align_2]", 1)
	second, err := Parse([]byte(secondInput))
	require.NoError(t, err)
	firstDigest, err := SourceDigest(first)
	require.NoError(t, err)
	secondDigest, err := SourceDigest(second)
	require.NoError(t, err)
	assert.Equal(t, firstDigest, secondDigest)
}

// emptyDependencyDocument is one Task whose Graph entry is dependencies, which
// keeps the nil-versus-empty distinction the digest and encoder tests are about.
func emptyDependencyDocument(dependencies []string) *Document {
	return &Document{
		SchemaVersion: 1, ID: "rna-seq", Name: "RNA-seq",
		Tasks: map[string]Task{"align": {Name: "Align", Model: "anthropic/claude-sonnet-4", Goal: "Align reads."}},
		Graph: map[string][]string{"align": dependencies},
	}
}

// A Graph with no dependencies is the same Graph whether its in-memory list is nil
// or empty: the digest hashes both alike. That is why the empty slice is
// materialised in the encoder, not in clone or normalizeAndValidate.
func TestSourceDigestTreatsNilAndEmptyDependencyListsAlike(t *testing.T) {
	nilDigest, err := SourceDigest(emptyDependencyDocument(nil))
	require.NoError(t, err)
	emptyDigest, err := SourceDigest(emptyDependencyDocument([]string{}))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(nilDigest, "sha256:"))
	assert.Equal(t, nilDigest, emptyDigest)
}

// An empty dependency list is an empty array in the file, not null, so every
// reader of graph.yaml gets the array type the contract declares.
func TestEncodeWritesEmptyDependencyListAsArray(t *testing.T) {
	encoded, err := Encode(emptyDependencyDocument(nil))
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "align: []")
	assert.NotContains(t, string(encoded), "null")

	reparsed, err := Parse(encoded)
	require.NoError(t, err)
	require.Contains(t, reparsed.Graph, "align")
	assert.Empty(t, reparsed.Graph["align"])
	assert.NotNil(t, reparsed.Graph["align"], "the encoded Graph must parse back to an empty list, not nil")

	// A file that carried null before this change keeps its digest when it is
	// rewritten as an array, so no client has to reload over a phantom conflict.
	originalDigest, err := SourceDigest(emptyDependencyDocument(nil))
	require.NoError(t, err)
	reparsedDigest, err := SourceDigest(reparsed)
	require.NoError(t, err)
	assert.Equal(t, originalDigest, reparsedDigest)
}
