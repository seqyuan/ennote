package prompts

import (
	"errors"
	"strconv"
	"strings"
)

// ParsedInvocation holds the result of parsing a slash invocation.
type ParsedInvocation struct {
	Name         string
	RawArguments string
}

// Expand limits.
const (
	expandOutputLimit = 256 * 1024 // 256 KiB
)

// Expand errors.
var (
	ErrExpandedPromptTooLarge = errors.New("expanded prompt exceeds 256 KiB")
	ErrExpandedPromptEmpty    = errors.New("expanded prompt is empty or whitespace-only")
)

// ParseInvocation parses a slash-command invocation string.
func ParseInvocation(s string) (ParsedInvocation, bool) {
	if len(s) == 0 || s[0] != '/' {
		return ParsedInvocation{}, false
	}
	pos := 1
	if pos >= len(s) {
		return ParsedInvocation{}, false
	}
	c := s[pos]
	if !isNameChar(c) || !isNameFirstChar(c) {
		return ParsedInvocation{}, false
	}
	nameStart := pos
	pos++
	for pos < len(s) && isNameChar(s[pos]) && pos-nameStart < 32 {
		pos++
	}
	if pos-nameStart == 32 && pos < len(s) && isNameChar(s[pos]) {
		return ParsedInvocation{}, false
	}
	name := s[nameStart:pos]
	if pos < len(s) {
		c = s[pos]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return ParsedInvocation{}, false
		}
	}
	for pos < len(s) && isASCIIWhitespace(s[pos]) {
		pos++
	}
	rawArgs := ""
	if pos < len(s) {
		rawArgs = s[pos:]
	}
	return ParsedInvocation{Name: name, RawArguments: rawArgs}, true
}

func isNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}
func isNameFirstChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}
func isASCIIWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}
func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isIdentifierCont(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// expandBuilder tracks bytes written and enforces the output limit.
type expandBuilder struct {
	builder strings.Builder
	written int
}

func (b *expandBuilder) writeString(s string) error {
	b.written += len(s)
	if b.written > expandOutputLimit {
		return ErrExpandedPromptTooLarge
	}
	b.builder.WriteString(s)
	return nil
}

func (b *expandBuilder) writeByte(c byte) error {
	b.written++
	if b.written > expandOutputLimit {
		return ErrExpandedPromptTooLarge
	}
	b.builder.WriteByte(c)
	return nil
}

func (b *expandBuilder) string() string { return b.builder.String() }

// ExpandTemplate expands a template body against the given args using a single
// left-to-right scan. Placeholder priority: $$ → ${...} → $ARGUMENTS → $@ → $N.
//
// A template with no non-$$ placeholders appends args after "\n\n" when args
// are present (design §5.3 rule 7-8).
func ExpandTemplate(body string, args []string) (string, error) {
	joinedAll := strings.Join(args, " ")
	var b expandBuilder
	var hadPlaceholder bool
	i, n := 0, len(body)

	for i < n {
		c := body[i]
		if c != '$' || i+1 >= n {
			if err := b.writeByte(c); err != nil {
				return "", err
			}
			i++
			continue
		}
		next := body[i+1]

		// $$ → literal $.
		if next == '$' {
			if err := b.writeByte('$'); err != nil {
				return "", err
			}
			i += 2
			continue
		}

		// ${...} → braced form.
		if next == '{' {
			end := strings.IndexByte(body[i+2:], '}')
			if end < 0 {
				if err := b.writeByte('$'); err != nil {
					return "", err
				}
				i++
				continue
			}
			inner := body[i+2 : i+2+end]
			if err := b.writeString(expandBraced(inner, args)); err != nil {
				return "", err
			}
			i += 2 + end + 1
			hadPlaceholder = true
			continue
		}

		// $ARGUMENTS (only when NOT followed by [a-zA-Z0-9_]).
		argTag := "ARGUMENTS"
		if i+1+len(argTag) <= n && body[i+1:i+1+len(argTag)] == argTag {
			afterPos := i + 1 + len(argTag)
			if afterPos >= n || !isIdentifierCont(body[afterPos]) {
				if err := b.writeString(joinedAll); err != nil {
					return "", err
				}
				i += 1 + len(argTag)
				hadPlaceholder = true
				continue
			}
			if err := b.writeByte('$'); err != nil {
				return "", err
			}
			i++
			continue
		}

		// $@ → all args (always 2 characters).
		if next == '@' {
			if err := b.writeString(joinedAll); err != nil {
				return "", err
			}
			i += 2
			hadPlaceholder = true
			continue
		}

		// $N → positional arg N.
		if isDigit(next) {
			j := i + 1
			for j < n && isDigit(body[j]) {
				j++
			}
			idx, _ := strconv.Atoi(body[i+1 : j])
			if idx >= 1 && idx <= len(args) {
				if err := b.writeString(args[idx-1]); err != nil {
					return "", err
				}
			}
			i = j
			hadPlaceholder = true
			continue
		}

		// Unrecognised: emit $ literally.
		if err := b.writeByte('$'); err != nil {
			return "", err
		}
		i++
	}

	// No-placeholder append: args present but no non-$$ placeholder was found.
	if !hadPlaceholder && len(args) > 0 {
		if err := b.writeString("\n\n" + joinedAll); err != nil {
			return "", err
		}
	}

	result := b.string()
	if strings.TrimSpace(result) == "" {
		return "", ErrExpandedPromptEmpty
	}
	return result, nil
}

// expandBraced handles ${...} content, dispatching on default, slice, or plain.
func expandBraced(inner string, args []string) string {
	if idx := strings.Index(inner, ":-"); idx >= 0 {
		return expandDefaulted(inner[:idx], inner[idx+2:], args)
	}
	if colonIdx := strings.IndexByte(inner, ':'); colonIdx >= 0 {
		if prefix := inner[:colonIdx]; prefix == "@" || prefix == "ARGUMENTS" {
			return expandSlice(inner[colonIdx+1:], args)
		}
		return ""
	}
	return expandPlain(inner, args)
}

func expandPlain(name string, args []string) string {
	if name == "@" || name == "ARGUMENTS" {
		return strings.Join(args, " ")
	}
	if idx, err := strconv.Atoi(name); err == nil {
		if idx >= 1 && idx <= len(args) {
			return args[idx-1]
		}
		return ""
	}
	return ""
}

func expandDefaulted(name, def string, args []string) string {
	if name == "@" || name == "ARGUMENTS" {
		if joined := strings.Join(args, " "); joined != "" {
			return joined
		}
		return def
	}
	if idx, err := strconv.Atoi(name); err == nil {
		if idx >= 1 && idx <= len(args) && args[idx-1] != "" {
			return args[idx-1]
		}
		return def
	}
	return def
}

func expandSlice(rest string, args []string) string {
	parts := strings.SplitN(rest, ":", 2)
	start, _ := strconv.Atoi(parts[0])
	if start < 1 {
		return ""
	}
	begin := start - 1
	if begin >= len(args) {
		return ""
	}
	end := len(args)
	if len(parts) == 2 {
		if l, err := strconv.Atoi(parts[1]); err == nil && l >= 0 {
			end = begin + l
			if end > len(args) {
				end = len(args)
			}
		}
	}
	return strings.Join(args[begin:end], " ")
}
