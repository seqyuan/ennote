package prompts

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// ParseTemplate — happy paths
// ============================================================================

func TestParseTemplate_NoFrontmatter(t *testing.T) {
	data := []byte("This is a simple prompt template.")
	tmpl, err := ParseTemplate(data, "simple.md")
	require.NoError(t, err)
	assert.Equal(t, "simple", tmpl.Name)
	assert.Equal(t, "This is a simple prompt template.", tmpl.Description)
	assert.Equal(t, "", tmpl.ArgumentHint)
	assert.Equal(t, "This is a simple prompt template.", tmpl.Body)
}

func TestParseTemplate_MinimalFrontmatter(t *testing.T) {
	data := []byte("---\nname: review\ndescription: Review a file\n---\nPlease review: $1")
	tmpl, err := ParseTemplate(data, "any.md")
	require.NoError(t, err)
	assert.Equal(t, "review", tmpl.Name)
	assert.Equal(t, "Review a file", tmpl.Description)
	assert.Equal(t, "Please review: $1", tmpl.Body)
}

func TestParseTemplate_WithArgumentHint(t *testing.T) {
	data := []byte("---\nname: review\ndescription: Review files\nargument-hint: <path> [focus]\n---\nReview: $1")
	tmpl, err := ParseTemplate(data, "rev.md")
	require.NoError(t, err)
	assert.Equal(t, "review", tmpl.Name)
	assert.Equal(t, "Review files", tmpl.Description)
	assert.Equal(t, "<path> [focus]", tmpl.ArgumentHint)
}

func TestParseTemplate_NameFromFile(t *testing.T) {
	// No frontmatter at all → name from file name.
	data := []byte("Just body")
	tmpl, err := ParseTemplate(data, "my-command.md")
	require.NoError(t, err)
	assert.Equal(t, "my-command", tmpl.Name)

	// Frontmatter without name key → name from file name.
	data = []byte("---\ndescription: Some cmd\n---\nBody")
	tmpl, err = ParseTemplate(data, "override.md")
	require.NoError(t, err)
	assert.Equal(t, "override", tmpl.Name)
}

func TestParseTemplate_DescriptionFallback(t *testing.T) {
	// No frontmatter: description = first non-empty body line.
	data := []byte("First line\nSecond line")
	tmpl, err := ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "First line", tmpl.Description)

	// Frontmatter with absent description → body first line.
	data = []byte("---\nname: cmd\n---\nBody first line\nSecond line")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "Body first line", tmpl.Description)

	// Description: null → fallback.
	data = []byte("---\nname: cmd\ndescription: null\n---\nFallback line\nMore")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "Fallback line", tmpl.Description)

	// Description: ~ → fallback (nil).
	data = []byte("---\nname: cmd\ndescription: ~\n---\nFrom body\n")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "From body", tmpl.Description)

	// Description: "" → fallback.
	data = []byte("---\nname: cmd\ndescription: \"\"\n---\nFrom empty desc\n")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "From empty desc", tmpl.Description)
}

func TestParseTemplate_ArgumentHintEmpty(t *testing.T) {
	// Absent → "".
	data := []byte("---\nname: cmd\n---\nBody")
	tmpl, err := ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "", tmpl.ArgumentHint)

	// Null → "".
	data = []byte("---\nname: cmd\nargument-hint: null\n---\nBody")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "", tmpl.ArgumentHint)

	// ~ → "".
	data = []byte("---\nname: cmd\nargument-hint: ~\n---\nBody")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "", tmpl.ArgumentHint)

	// "" → "".
	data = []byte("---\nname: cmd\nargument-hint: \"\"\n---\nBody")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "", tmpl.ArgumentHint)
}

// ============================================================================
// ParseTemplate — name rejection cases
// ============================================================================

func TestParseTemplate_NameInvalid(t *testing.T) {
	// name: null → invalid.
	_, err := ParseTemplate([]byte("---\nname: null\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)

	// name: ~ → invalid.
	_, err = ParseTemplate([]byte("---\nname: ~\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)

	// name: "" → invalid (empty string).
	_, err = ParseTemplate([]byte("---\nname: \"\"\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)

	// name: (bare, no value) → null → invalid.
	_, err = ParseTemplate([]byte("---\nname:\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)

	// name with uppercase → invalid.
	_, err = ParseTemplate([]byte("---\nname: Foo\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)

	// name with special char → invalid.
	_, err = ParseTemplate([]byte("---\nname: foo@bar\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)

	// name > 32 chars → invalid.
	_, err = ParseTemplate([]byte("---\nname: "+strings.Repeat("a", 33)+"\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)
}

func TestParseTemplate_NameNonString(t *testing.T) {
	// name as integer → invalid (non-!!str, non-!!null).
	_, err := ParseTemplate([]byte("---\nname: 123\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)

	// name as sequence → invalid.
	_, err = ParseTemplate([]byte("---\nname:\n  - item\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)

	// name as mapping → invalid.
	_, err = ParseTemplate([]byte("---\nname:\n  key: val\n---\nBody"), "file.md")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)
}

// ============================================================================
// ParseTemplate — YAML structure rejection
// ============================================================================

func TestParseTemplate_DuplicateKey(t *testing.T) {
	data := []byte("---\nname: cmd\ndescription: d1\ndescription: d2\n---\nBody")
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateDuplicateKey)
}

func TestParseTemplate_UnknownField(t *testing.T) {
	data := []byte("---\nname: cmd\nfoo: bar\n---\nBody")
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateUnknownField)
}

func TestParseTemplate_AliasNotAllowed(t *testing.T) {
	// YAML anchor + alias → rejected.
	data := []byte("---\nname: cmd\ndescription: &anchor value\ndescription: *anchor\n---\nBody")
	_, err := ParseTemplate(data, "t.md")
	assert.True(t, errors.Is(err, ErrTemplateAliasAnchor) || errors.Is(err, ErrTemplateDuplicateKey),
		"expected ErrTemplateAliasAnchor or duplicate key, got %v", err)
}

// ============================================================================
// ParseTemplate — size / encoding / NUL / BOM
// ============================================================================

func TestParseTemplate_TooLarge(t *testing.T) {
	bigBody := strings.Repeat("x", maxTemplateBytes+1)
	_, err := ParseTemplate([]byte(bigBody), "t.md")
	assert.ErrorIs(t, err, ErrTemplateTooLarge)
}

func TestParseTemplate_Exactly64KiB(t *testing.T) {
	// 64 KiB exactly should succeed.
	body := strings.Repeat("x", maxTemplateBytes)
	tmpl, err := ParseTemplate([]byte(body), "ok.md")
	require.NoError(t, err)
	assert.Equal(t, "ok", tmpl.Name)
}

func TestParseTemplate_UTF8BOMRejected(t *testing.T) {
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("---\nname: cmd\n---\nBody")...)
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateBOM)
}

func TestParseTemplate_InvalidUTF8(t *testing.T) {
	// 0xFF is never valid in UTF-8.
	data := []byte{0xFF, 0xFE}
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateInvalidUTF8)
}

func TestParseTemplate_NULRejected(t *testing.T) {
	data := []byte("hello\x00world")
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateContainsNUL)
}

// ============================================================================
// ParseTemplate — body validation
// ============================================================================

func TestParseTemplate_BodyEmpty(t *testing.T) {
	_, err := ParseTemplate([]byte("---\nname: cmd\n---\n"), "t.md")
	assert.ErrorIs(t, err, ErrTemplateBodyEmpty)

	_, err = ParseTemplate([]byte("---\nname: cmd\n---\n   \n\t\n"), "t.md")
	assert.ErrorIs(t, err, ErrTemplateBodyEmpty)
}

// ============================================================================
// ParseTemplate — description / argument-hint length & content
// ============================================================================

func TestParseTemplate_DescriptionTooLong(t *testing.T) {
	long := strings.Repeat("x", maxDescriptionLen+1)
	data := []byte("---\nname: cmd\ndescription: " + long + "\n---\nBody")
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateDescTooLong)
}

func TestParseTemplate_DescriptionWithNewline(t *testing.T) {
	data := []byte("---\nname: cmd\ndescription: \"line1\\nline2\"\n---\nBody")
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateDescInvalid)
}

func TestParseTemplate_ArgumentHintTooLong(t *testing.T) {
	long := strings.Repeat("x", maxArgumentHintLen+1)
	data := []byte("---\nname: cmd\nargument-hint: " + long + "\n---\nBody")
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateHintTooLong)
}

func TestParseTemplate_ArgumentHintWithControlChar(t *testing.T) {
	// NUL byte in a YAML string.
	data := []byte("---\nname: cmd\nargument-hint: \"x\\x00y\"\n---\nBody")
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateHintInvalid)
}

// ============================================================================
// ParseTemplate — delimiter handling
// ============================================================================

func TestParseTemplate_NoClosingDelimiter(t *testing.T) {
	data := []byte("---\nname: cmd\nBody without closing fence")
	_, err := ParseTemplate(data, "t.md")
	assert.ErrorIs(t, err, ErrTemplateBadDelimiter)
}

func TestParseTemplate_OpeningDelimiterMustBeFirstLine(t *testing.T) {
	// Opening --- is not on the first line → no frontmatter; whole text is body.
	data := []byte("\n---\nname: cmd\n---\nBody")
	tmpl, err := ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "t", tmpl.Name) // file name fallback
	// The entire content is the body (starting with \n---\n...).
	assert.True(t, strings.Contains(tmpl.Body, "---"))
}

func TestParseTemplate_OpeningFenceNotStandalone(t *testing.T) {
	// "---abc" is not a delimiter → no frontmatter.
	data := []byte("---abc\nname: cmd\n---\nBody")
	tmpl, err := ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "t", tmpl.Name)
	assert.True(t, strings.HasPrefix(tmpl.Body, "---abc"))
}

func TestParseTemplate_OnlyOpeningFence(t *testing.T) {
	// Just "---" as the whole file → no frontmatter (no newline after opening).
	data := []byte("---")
	tmpl, err := ParseTemplate(data, "single.md")
	require.NoError(t, err)
	assert.Equal(t, "single", tmpl.Name)
	assert.Equal(t, "---", tmpl.Body)
}

// ============================================================================
// ParseTemplate — CRLF / CR normalisation
// ============================================================================

func TestParseTemplate_CRLFNormalization(t *testing.T) {
	data := []byte("---\r\nname: cmd\r\n---\r\nLine 1\r\nLine 2")
	tmpl, err := ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "cmd", tmpl.Name)
	assert.Equal(t, "Line 1\nLine 2", tmpl.Body)
}

func TestParseTemplate_CRNormalization(t *testing.T) {
	data := []byte("---\rname: cmd\r---\rLine 1\rLine 2")
	tmpl, err := ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "cmd", tmpl.Name)
	assert.Equal(t, "Line 1\nLine 2", tmpl.Body)
}

// ============================================================================
// ParseTemplate — edge cases
// ============================================================================

func TestParseTemplate_ValidNameBoundaries(t *testing.T) {
	// 1 char name.
	data := []byte("---\nname: a\n---\nBody")
	tmpl, err := ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "a", tmpl.Name)

	// 32 char name.
	data = []byte("---\nname: " + strings.Repeat("a", 32) + "\n---\nBody")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 32), tmpl.Name)

	// Name starting with digit.
	data = []byte("---\nname: 123abc\n---\nBody")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "123abc", tmpl.Name)

	// Name with hyphens and underscores.
	data = []byte("---\nname: my-command_v2\n---\nBody")
	tmpl, err = ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, "my-command_v2", tmpl.Name)
}

func TestParseTemplate_DescriptionExactlyMaxLength(t *testing.T) {
	desc := strings.Repeat("x", maxDescriptionLen)
	data := []byte("---\nname: cmd\ndescription: " + desc + "\n---\nBody")
	tmpl, err := ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, desc, tmpl.Description)
}

func TestParseTemplate_ArgumentHintExactlyMaxLength(t *testing.T) {
	hint := strings.Repeat("<", maxArgumentHintLen)
	data := []byte("---\nname: cmd\nargument-hint: " + hint + "\n---\nBody")
	tmpl, err := ParseTemplate(data, "t.md")
	require.NoError(t, err)
	assert.Equal(t, hint, tmpl.ArgumentHint)
}
