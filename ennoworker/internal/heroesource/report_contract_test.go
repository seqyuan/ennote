package heroesource_test

// Hero report contracts in the Portable Hero document. The field is optional and
// absent from every Hero written before it existed, so the first test pins the
// source digest of an existing fixture: adding an optional YAML field must not
// move the digest of a document that does not use it.

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// validHeroMarkdownDigest is the digest of validHeroMarkdown as it was before
// HeroReportContract existed. Pinned so an optional field (or its normalisation)
// can never silently rewrite the identity of a published revision.
const validHeroMarkdownDigest = "sha256:5e0ab2a64957d76964581451bff5710b88ee98896b03335f5a79afd7b2485a2c"

func heroMarkdownWithReportContract(section string) string {
	return strings.Replace(validHeroMarkdown, "outputContract: text-v1",
		"outputContract: text-v1\nreportContract:\n"+section, 1)
}

// rawHeroDocument decodes the frontmatter the way an API body decode does:
// without the read-path normalisation. A duplicate payloadFields list is only
// observable here, because normalisation is what collapses it.
func rawHeroDocument(t *testing.T, section string) *heroesource.Document {
	t.Helper()
	markdown := heroMarkdownWithReportContract(section)
	normalized := strings.ReplaceAll(markdown, "\r\n", "\n")
	closing := strings.Index(normalized[4:], "\n---\n")
	require.GreaterOrEqual(t, closing, 0)
	var document heroesource.Document
	require.NoError(t, yaml.Unmarshal([]byte(normalized[4:closing+4]), &document))
	document.Prompt = "prompt"
	return &document
}

func TestAbsentReportContractKeepsDocumentAndDigestUnchanged(t *testing.T) {
	document, err := heroesource.Parse([]byte(validHeroMarkdown))
	require.NoError(t, err)
	assert.Nil(t, document.ReportContract)

	digest, err := heroesource.SourceDigest(document)
	require.NoError(t, err)
	assert.Equal(t, validHeroMarkdownDigest, digest)

	// The encoded document carries no reportContract key at all, so a Hero file
	// that predates the field re-encodes byte-for-byte.
	encoded, err := heroesource.Encode(document)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "reportContract")
}

func TestParseNormalizesReportContractPayloadFields(t *testing.T) {
	document, err := heroesource.Parse([]byte(heroMarkdownWithReportContract(
		"  version: 1\n  summaryMaxBytes: 512\n  requireArtifacts: true\n" +
			"  payloadFields: [' changedFiles', testsRun, 'changedFiles ', '']\n")))
	require.NoError(t, err)
	require.NotNil(t, document.ReportContract)
	assert.Equal(t, 1, document.ReportContract.Version)
	assert.Equal(t, 512, document.ReportContract.SummaryMaxBytes)
	assert.True(t, document.ReportContract.RequireArtifacts)
	assert.Equal(t, []string{"changedFiles", "testsRun"}, document.ReportContract.PayloadFields,
		"payloadFields must be trimmed, sorted and duplicate-free")

	// Authoring order and stray whitespace must not change the source digest.
	ordered, err := heroesource.Parse([]byte(heroMarkdownWithReportContract(
		"  version: 1\n  summaryMaxBytes: 512\n  requireArtifacts: true\n" +
			"  payloadFields: [testsRun, changedFiles]\n")))
	require.NoError(t, err)
	firstDigest, err := heroesource.SourceDigest(document)
	require.NoError(t, err)
	secondDigest, err := heroesource.SourceDigest(ordered)
	require.NoError(t, err)
	assert.Equal(t, firstDigest, secondDigest)

	// The YAML round-trip keeps the documented camelCase key names.
	encoded, err := heroesource.Encode(document)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "summaryMaxBytes: 512")
	assert.Contains(t, string(encoded), "requireArtifacts: true")
	assert.Contains(t, string(encoded), "payloadFields:")

	roundTripped, err := heroesource.Parse(encoded)
	require.NoError(t, err)
	assert.Equal(t, document, roundTripped)
}

func TestValidateForWriteAcceptsAUsableReportContract(t *testing.T) {
	document, err := heroesource.Parse([]byte(heroMarkdownWithReportContract(
		"  version: 1\n  summaryMaxBytes: 4096\n  requireArtifacts: true\n  payloadFields: [changedFiles]\n")))
	require.NoError(t, err)
	require.NoError(t, heroesource.ValidateForWrite(document))

	// A contract that only bounds the summary is a complete promise too.
	summaryOnly, err := heroesource.Parse([]byte(heroMarkdownWithReportContract(
		"  version: 1\n  summaryMaxBytes: 1\n")))
	require.NoError(t, err)
	require.NoError(t, heroesource.ValidateForWrite(summaryOnly))
}

func TestValidateForWriteRejectsEachUnusableReportContract(t *testing.T) {
	tests := []struct {
		name    string
		section string
		message string
	}{
		{name: "unsupported version", section: "  version: 2\n  summaryMaxBytes: 128\n", message: "reportContract.version must be 1"},
		{name: "missing version", section: "  summaryMaxBytes: 128\n", message: "reportContract.version must be 1"},
		{name: "summary zero", section: "  version: 1\n  summaryMaxBytes: 0\n",
			message: "reportContract.summaryMaxBytes must be 1..4096"},
		{name: "summary omitted", section: "  version: 1\n",
			message: "reportContract.summaryMaxBytes must be 1..4096"},
		{name: "summary above the global cap", section: "  version: 1\n  summaryMaxBytes: 4097\n",
			message: "reportContract.summaryMaxBytes must be 1..4096"},
		{name: "negative summary", section: "  version: 1\n  summaryMaxBytes: -1\n",
			message: "reportContract.summaryMaxBytes must be 1..4096"},
		{name: "too many payload fields", section: "  version: 1\n  summaryMaxBytes: 128\n  payloadFields: [" +
			"f1, f2, f3, f4, f5, f6, f7, f8, f9, f10, f11, f12, f13, f14, f15, f16, f17]\n",
			message: "at most 16 entries"},
		{name: "payload field is not an identifier", section: "  version: 1\n  summaryMaxBytes: 128\n  payloadFields: ['changed-files']\n",
			message: "must match ^[A-Za-z_][A-Za-z0-9_]*$"},
		{name: "payload field starts with a digit", section: "  version: 1\n  summaryMaxBytes: 128\n  payloadFields: [9lives]\n",
			message: "must match ^[A-Za-z_][A-Za-z0-9_]*$"},
		{name: "duplicate payload field", section: "  version: 1\n  summaryMaxBytes: 128\n  payloadFields: [changedFiles, changedFiles]\n",
			message: "duplicates payloadFields[0]"},
		// Normalisation trims, so two keys that differ only by whitespace are the
		// same key and must be refused instead of silently collapsed.
		{name: "duplicate after trimming", section: "  version: 1\n  summaryMaxBytes: 128\n  payloadFields: [changedFiles, ' changedFiles ']\n",
			message: "duplicates payloadFields[0]"},
		{name: "blank payload field", section: "  version: 1\n  summaryMaxBytes: 128\n  payloadFields: [changedFiles, '  ']\n",
			message: "payloadFields[1] must not be empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// The read path must stay lenient: a document this validator refuses
			// still parses, so a revision a future Worker wrote remains loadable.
			_, err := heroesource.Parse([]byte(heroMarkdownWithReportContract(test.section)))
			require.NoError(t, err, "Parse must not reject %s", test.name)

			err = heroesource.ValidateForWrite(rawHeroDocument(t, test.section))
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

// A duplicate is harmless once normalised, but a client that sends one is
// asking for something the document cannot express, so the API write boundary
// refuses it. A hand-authored file keeps parsing: the collapse is
// order-independent, so it cannot change what the Hero requires.
func TestParseCollapsesDuplicatePayloadFieldsTheWritePathRefuses(t *testing.T) {
	const section = "  version: 1\n  summaryMaxBytes: 128\n  payloadFields: [changedFiles, changedFiles]\n"
	document, err := heroesource.Parse([]byte(heroMarkdownWithReportContract(section)))
	require.NoError(t, err)
	assert.Equal(t, []string{"changedFiles"}, document.ReportContract.PayloadFields)
	require.NoError(t, heroesource.ValidateForWrite(document),
		"the parsed document is already canonical, so publishing it stays possible")

	require.Error(t, heroesource.ValidateForWrite(rawHeroDocument(t, section)),
		"the un-normalised API body is refused")
}

// A published revision written before the field existed must remain loadable and
// writable: the read path never rejects the contract, and an absent contract is
// still a valid document.
func TestValidateForWriteAcceptsAnAbsentReportContract(t *testing.T) {
	document, err := heroesource.Parse([]byte(validHeroMarkdown))
	require.NoError(t, err)
	require.Nil(t, document.ReportContract)
	require.NoError(t, heroesource.ValidateForWrite(document))
}

// A normalized contract freezes into the domain definition the Worker enforces,
// so the payload-field order an author wrote is irrelevant at Run time.
func TestEncodePreservesReportContractWithoutAliasing(t *testing.T) {
	document, err := heroesource.Parse([]byte(heroMarkdownWithReportContract(
		"  version: 1\n  summaryMaxBytes: 128\n  payloadFields: [b, a]\n")))
	require.NoError(t, err)
	require.NotNil(t, document.ReportContract)
	original := append([]string(nil), document.ReportContract.PayloadFields...)

	encoded, err := heroesource.Encode(document)
	require.NoError(t, err)
	assert.Equal(t, original, document.ReportContract.PayloadFields,
		"encoding must not mutate the caller's contract")
	assert.Contains(t, string(encoded), "payloadFields:")

	digest, err := heroesource.SourceDigest(document)
	require.NoError(t, err)
	assert.Equal(t, original, document.ReportContract.PayloadFields,
		"digesting must not mutate the caller's contract")
	assert.NotEmpty(t, digest)
	assert.IsType(t, &domain.HeroReportContract{}, document.ReportContract)
}
