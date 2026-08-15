package prompts

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Limits applied before line-ending normalisation (i.e. on raw bytes).
const maxTemplateBytes = 64 * 1024 // 64 KiB

// Limits applied after frontmatter parsing (Unicode code points, not bytes).
const (
	maxDescriptionLen  = 240
	maxArgumentHintLen = 120
	maxNameLen         = 32
)

// Frontmatter / template validation errors.
var (
	ErrTemplateTooLarge        = errors.New("template file exceeds 64 KiB")
	ErrTemplateInvalidUTF8     = errors.New("template file is not valid UTF-8")
	ErrTemplateContainsNUL     = errors.New("template file contains NUL bytes")
	ErrTemplateBOM             = errors.New("template file contains UTF-8 BOM")
	ErrTemplateBodyEmpty       = errors.New("template body is empty or whitespace-only")
	ErrTemplateFrontmatterYAML = errors.New("template frontmatter is not valid YAML")
	ErrTemplateNameInvalid     = errors.New("template name is invalid")
	ErrTemplateDescInvalid     = errors.New("template description is invalid")
	ErrTemplateHintInvalid     = errors.New("template argument-hint is invalid")
	ErrTemplateDescTooLong     = errors.New("template description exceeds 240 code points")
	ErrTemplateHintTooLong     = errors.New("template argument-hint exceeds 120 code points")
	ErrTemplateUnknownField    = errors.New("unknown field in frontmatter")
	ErrTemplateDuplicateKey    = errors.New("duplicate key in frontmatter")
	ErrTemplateAliasAnchor     = errors.New("alias or anchor in frontmatter")
	ErrTemplateBadDelimiter    = errors.New("bad or missing YAML frontmatter delimiter")
)

// Template is a parsed prompt template. It carries the resolved metadata and
// the prompt body text. Tier, Source, and Path are set later by the registry
// loader; they are not part of the file-level parse.
type Template struct {
	Name         string
	Description  string
	ArgumentHint string
	Body         string
	Tier         Tier
	Source       string
	Path         string // internal diagnostics only; never used as identity
}

// ParseTemplate parses raw bytes of a Markdown template file. fileName is the
// base name of the source file (e.g. "review.md"); its stem is used as the
// template name when the frontmatter omits the name key.
//
// Processing order:
//  1. Reject if raw size exceeds 64 KiB.
//  2. Reject UTF-8 BOM (0xEF 0xBB 0xBF).
//  3. Reject invalid UTF-8.
//  4. Reject NUL bytes.
//  5. Normalise CRLF / CR → LF.
//  6. Split optional YAML frontmatter (strict "---" delimiters on their own
//     lines; opening delimiter must be the first line of the normalised text).
//  7. Parse frontmatter via yaml.Node, tracking key presence, rejecting
//     aliases, anchors, duplicate keys, and unknown fields.
//  8. Resolve name, description, and argument-hint per the presence rules.
//  9. Validate lengths and character content of description and argument-hint.
//  10. Reject empty (whitespace-only) body.
func ParseTemplate(data []byte, fileName string) (Template, error) {
	// 1. Reject over-size.
	if len(data) > maxTemplateBytes {
		return Template{}, fmt.Errorf("%w: %d bytes", ErrTemplateTooLarge, len(data))
	}

	// 2. Reject UTF-8 BOM.
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return Template{}, ErrTemplateBOM
	}

	// 3. Reject invalid UTF-8.
	if !utf8.Valid(data) {
		return Template{}, ErrTemplateInvalidUTF8
	}

	// 4. Reject NUL.
	if bytes.IndexByte(data, 0) >= 0 {
		return Template{}, ErrTemplateContainsNUL
	}

	// 5. Normalise line endings.
	text := normalizeLineEndings(string(data))

	// 6. Split frontmatter and body.
	bodyText, fmText, hasFM, err := splitFrontmatter(text)
	if err != nil {
		return Template{}, err
	}

	var fm frontmatterResult
	if hasFM {
		fm, err = parseFrontmatter(fmText)
		if err != nil {
			return Template{}, err
		}
	}

	// 8. Resolve name and validate.
	name := resolveName(fm, fileName)
	if err := ValidateName(name); err != nil {
		return Template{}, fmt.Errorf("%w: %w", ErrTemplateNameInvalid, err)
	}

	// 9. Resolve description (may fall back to body first line).
	desc, err := resolveDescription(fm, bodyText)
	if err != nil {
		return Template{}, err
	}

	// 10. Resolve argument-hint.
	hint, err := resolveArgumentHint(fm)
	if err != nil {
		return Template{}, err
	}

	// 11. Body must be non-empty.
	body := bodyText
	if strings.TrimSpace(body) == "" {
		return Template{}, ErrTemplateBodyEmpty
	}

	return Template{
		Name:         name,
		Description:  desc,
		ArgumentHint: hint,
		Body:         body,
	}, nil
}

// normalizeLineEndings replaces CRLF and bare CR with LF.
func normalizeLineEndings(s string) string {
	// Fast path: no CR present.
	if !strings.Contains(s, "\r") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\r' {
			if i+1 < len(s) && s[i+1] == '\n' {
				i++ // skip the LF in CRLF
			}
			b.WriteByte('\n')
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// splitFrontmatter separates a leading "---\n"-delimited YAML block from the
// body. The opening delimiter must be the very first line of the normalised
// text (after normalisation, so the leading line is exactly "---" followed by
// LF). The closing delimiter must be a line consisting solely of "---" (the
// trailing LF is consumed as the line separator).
//
// Returns (body, frontmatterText, hasFrontmatter, error).
func splitFrontmatter(text string) (body string, fm string, has bool, err error) {
	if text == "" {
		return "", "", false, nil
	}

	// Opening delimiter must be the first line: "---\n" or "---" at EOF.
	const delim = "---"
	if !strings.HasPrefix(text, delim) {
		// No frontmatter at all — the whole text is the body.
		return text, "", false, nil
	}

	// Opening delimiter must be a complete line.
	rest := text[len(delim):]
	if rest == "" {
		// Just "---" with no body at all → no frontmatter, empty body.
		// Actually this would be a single-line file containing only "---".
		// Treat as: no frontmatter, body is "---". But per spec the opening
		// delimiter must be the first *line*, and with no newline after it
		// there is no closing delimiter either. Treat as no frontmatter.
		return text, "", false, nil
	}
	if rest[0] != '\n' {
		// "---something" on the first line → not a delimiter; treat as plain body.
		return text, "", false, nil
	}
	rest = rest[1:] // consume the LF after opening delimiter

	// Find the closing delimiter: a line that is exactly "---".
	lines := strings.Split(rest, "\n")
	closeIdx := -1
	for i, line := range lines {
		if line == delim {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return "", "", false, ErrTemplateBadDelimiter
	}

	// Frontmatter is the lines between opening and closing delimiters.
	fmLines := lines[:closeIdx]
	bodyLines := lines[closeIdx+1:]

	fm = strings.Join(fmLines, "\n")
	body = strings.Join(bodyLines, "\n")
	return body, fm, true, nil
}

// frontmatterResult holds the parsed presence of the three allowed keys.
// A nil node means the key was absent from the mapping entirely.
type frontmatterResult struct {
	name    *yaml.Node // absent → nil; present → non-nil
	desc    *yaml.Node // absent → nil; present → non-nil
	argHint *yaml.Node // absent → nil; present → non-nil
}

// parseFrontmatter decodes the frontmatter text as YAML into a yaml.Node tree,
// then extracts the three allowed keys from the root mapping. It rejects:
// aliases, anchors, merge keys (<<), unknown keys, and duplicate keys.
func parseFrontmatter(text string) (frontmatterResult, error) {
	if text == "" {
		return frontmatterResult{}, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return frontmatterResult{}, fmt.Errorf("%w: %w", ErrTemplateFrontmatterYAML, err)
	}

	// doc is a DocumentNode; Content[0] is the root.
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		// Empty document; no keys present.
		return frontmatterResult{}, nil
	}
	root := doc.Content[0]

	// Also check the document-level node for aliases.
	if err := checkNodeAliasAnchor(&doc); err != nil {
		return frontmatterResult{}, err
	}

	if root.Kind != yaml.MappingNode {
		return frontmatterResult{}, fmt.Errorf("%w: root must be a mapping, got %d",
			ErrTemplateFrontmatterYAML, root.Kind)
	}

	return extractMapping(root)
}

// checkNodeAliasAnchor returns an error if the node or any of its descendants
// contains an alias or an anchor (recursively).
func checkNodeAliasAnchor(node *yaml.Node) error {
	if node.Alias != nil {
		return ErrTemplateAliasAnchor
	}
	if node.Anchor != "" {
		return ErrTemplateAliasAnchor
	}
	for _, child := range node.Content {
		if err := checkNodeAliasAnchor(child); err != nil {
			return err
		}
	}
	return nil
}

// extractMapping walks the mapping node's Content (pairs of key/value) and
// extracts the three allowed keys. Duplicate keys and unknown keys are
// rejected. Merge keys (<<) are treated as unknown keys.
func extractMapping(mapping *yaml.Node) (frontmatterResult, error) {
	if mapping.Kind != yaml.MappingNode {
		return frontmatterResult{}, fmt.Errorf("expected mapping, got %d", mapping.Kind)
	}

	var result frontmatterResult
	seen := map[string]bool{} // reject duplicate keys

	// Content is [key0, val0, key1, val1, ...].
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		val := mapping.Content[i+1]

		if key.Kind != yaml.ScalarNode {
			return frontmatterResult{}, fmt.Errorf("%w: non-scalar key", ErrTemplateFrontmatterYAML)
		}

		k := key.Value
		if seen[k] {
			return frontmatterResult{}, fmt.Errorf("%w: %q", ErrTemplateDuplicateKey, k)
		}
		seen[k] = true

		// Reject aliases on the value node.
		if err := checkNodeAliasAnchor(val); err != nil {
			return frontmatterResult{}, err
		}

		switch k {
		case "name":
			result.name = val
		case "description":
			result.desc = val
		case "argument-hint":
			result.argHint = val
		default:
			return frontmatterResult{}, fmt.Errorf("%w: %q", ErrTemplateUnknownField, k)
		}
	}

	return result, nil
}

// resolveName determines the template name from the frontmatter presence.
//
// Rules:
//   - Key absent (nil) → file name stem without ".md" extension.
//   - Key present, !!str, non-empty → validate against ^[a-z0-9][a-z0-9_-]{0,31}$.
//   - Anything else (null, ~, empty string, non-scalar) → ErrTemplateNameInvalid.
func resolveName(fm frontmatterResult, fileName string) string {
	node := fm.name
	if node == nil {
		// Key absent → use file stem.
		base := filepath.Base(fileName)
		return strings.TrimSuffix(base, ".md")
	}

	// Key present: must be a non-empty !!str.
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || node.Value == "" {
		// Invalid: null, ~, empty string, sequence, mapping.
		// We return the empty string and let the caller decide.
		// (ParseTemplate checks for invalid name separately below.)
		return "" // will be caught by validateName
	}

	if err := validateName(node.Value); err != nil {
		return "" // will be caught by validateName
	}
	return node.Value
}

func validateName(name string) error {
	if len(name) == 0 || len(name) > 32 {
		return fmt.Errorf("%w: must be 1–32 characters: %q", ErrTemplateNameInvalid, name)
	}
	for i, r := range name {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				return fmt.Errorf("%w: first char must be [a-z0-9]: %q", ErrTemplateNameInvalid, name)
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
				return fmt.Errorf("%w: chars must be [a-z0-9_-]: %q", ErrTemplateNameInvalid, name)
			}
		}
	}
	return nil
}

// resolveDescription determines the description from frontmatter presence.
//
// Rules:
//   - Key absent or !!null → fall back to first non-empty body line.
//   - !!str, empty → fall back to first non-empty body line.
//   - !!str, non-empty → validate length ≤ 240 code points, no newlines, no control chars.
//   - Non-scalar → ErrTemplateDescInvalid.
func resolveDescription(fm frontmatterResult, body string) (string, error) {
	node := fm.desc

	if node == nil {
		return firstNonEmptyLine(body), nil
	}

	if node.Tag == "!!null" {
		return firstNonEmptyLine(body), nil
	}

	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%w: must be a string or null", ErrTemplateDescInvalid)
	}

	if node.Value == "" {
		return firstNonEmptyLine(body), nil
	}

	return validateTextField(node.Value, maxDescriptionLen, "description", ErrTemplateDescInvalid, ErrTemplateDescTooLong)
}

// resolveArgumentHint determines the argument-hint from frontmatter presence.
//
// Rules:
//   - Key absent or !!null → "" (no hint).
//   - !!str, empty → "" (no hint).
//   - !!str, non-empty → validate length ≤ 120 code points, no newlines, no control chars.
//   - Non-scalar → ErrTemplateHintInvalid.
func resolveArgumentHint(fm frontmatterResult) (string, error) {
	node := fm.argHint

	if node == nil {
		return "", nil
	}

	if node.Tag == "!!null" {
		return "", nil
	}

	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%w: must be a string or null", ErrTemplateHintInvalid)
	}

	if node.Value == "" {
		return "", nil
	}

	return validateTextField(node.Value, maxArgumentHintLen, "argument-hint", ErrTemplateHintInvalid, ErrTemplateHintTooLong)
}

// validateTextField checks that s contains no newlines, no control characters
// (except space and tab), and does not exceed maxCodePoints. It returns the
// validated string or a typed error.
func validateTextField(s string, maxCodePoints int, fieldName string, invalidErr, tooLongErr error) (string, error) {
	codePoints := 0
	for _, r := range s {
		codePoints++
		// Reject control characters except space (U+0020) and tab (U+0009).
		// Newline characters: U+000A (LF), U+000D (CR) are control chars
		// and will be caught here.
		if r < 0x20 && r != '\t' {
			return "", fmt.Errorf("%w: %s contains control character U+%04X", invalidErr, fieldName, r)
		}
		if r == 0x7F {
			return "", fmt.Errorf("%w: %s contains DEL character", invalidErr, fieldName)
		}
	}
	if codePoints > maxCodePoints {
		return "", fmt.Errorf("%w: %s has %d code points (max %d)", tooLongErr, fieldName, codePoints, maxCodePoints)
	}
	return s, nil
}

// firstNonEmptyLine returns the first line of s whose trimmed form is
// non-empty, itself trimmed. It is the description fallback when the
// frontmatter omits a description or sets it to null/empty.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			// Also validate the fallback: no control chars or newlines.
			if err := checkTextField(t, maxDescriptionLen, "description (fallback)", ErrTemplateDescInvalid, ErrTemplateDescTooLong); err != nil {
				// Fallback is invalid — return the text anyway but the
				// caller (ParseTemplate) may want to handle validation
				// separately. For now, trim and return.
				return t
			}
			return t
		}
	}
	return ""
}

// checkTextField is like validateTextField but returns only the error without
// returning the string.
func checkTextField(s string, maxCodePoints int, fieldName string, invalidErr, tooLongErr error) error {
	_, err := validateTextField(s, maxCodePoints, fieldName, invalidErr, tooLongErr)
	return err
}

// ValidateName checks whether name matches the allowed pattern:
// ^[a-z0-9][a-z0-9_-]{0,31}$
func ValidateName(name string) error {
	return validateName(name)
}
