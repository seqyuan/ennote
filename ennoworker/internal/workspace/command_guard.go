package workspace

import (
	"fmt"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func (m *Manager) protectedPathError() error {
	path := "~/.ennote"
	if len(m.ProtectedPaths) > 0 {
		path = m.ProtectedPaths[0]
	}
	return fmt.Errorf("sandbox mode %q cannot access the Ennote home (%s); enable ENNOTE_SANDBOX=bwrap for host credential isolation", m.Mode, path)
}

// shellParseError reports a command the guard could not even parse. It is a
// distinct failure from a protected-path hit: nothing is known about the
// command yet, so the message must not claim a home-path violation.
func (m *Manager) shellParseError(err error) error {
	return fmt.Errorf("sandbox mode %q cannot run this command: shell syntax error: %v; fix the quoting or enable ENNOTE_SANDBOX=bwrap for host credential isolation", m.Mode, err)
}

// shellExpansionError reports a construct that defeats static path analysis:
// the shell, not the guard, decides the final text a word denotes. Kept
// separate from protectedPathError so operators can tell a literal hit from a
// fail-closed rejection.
func (m *Manager) shellExpansionError(mechanism string) error {
	return fmt.Errorf("sandbox mode %q cannot access the Ennote home when a command uses %s: the expansion is rejected because it can assemble that path after validation; pass a literal path or enable ENNOTE_SANDBOX=bwrap for host credential isolation", m.Mode, mechanism)
}

// rejectShellCommand fails closed for degraded (SandboxNone) shell commands.
// The command is parsed and every word is reconstructed from its literal parts
// so that shell-level assembly is visible to the guard: unquoted globs, brace
// expansion, ANSI-C quoting ($'\x2f...'), backslash escapes and empty quote
// pairs (a pair of empty single quotes inside a name) all still name the same
// path once the shell runs.
// Residual limitation: this stays a static analysis of one command string. It
// rejects every construct it cannot resolve (expansions, substitutions, globs,
// braces), but a script reached indirectly (sh -c "$(cat x)", a shell function,
// a file on disk) is only caught by bwrap isolation, never by this guard.
func (m *Manager) rejectShellCommand(command string) error {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return m.shellParseError(err)
	}
	if violation := m.scanShellWords(file); violation != nil {
		return violation
	}
	// Backstop for parser quirks: re-check the raw command with the shell-level
	// assembly syntax stripped.
	if m.literalHitsHome(normalizeShellCommand(command)) {
		return m.protectedPathError()
	}
	return nil
}

func (m *Manager) scanShellWords(file *syntax.File) error {
	var violation error
	syntax.Walk(file, func(node syntax.Node) bool {
		if violation != nil {
			return false
		}
		word, ok := node.(*syntax.Word)
		if !ok {
			return true
		}
		literal := literalWordValue(word)
		switch {
		case literal.mechanism != "":
			violation = m.shellExpansionError(literal.mechanism)
		case literal.brace:
			// {a,b} is expanded by the shell, and the parser does not model
			// it, so a literal brace expansion is rejected. A bare {} or {x}
			// (find -exec ... {} +) is literal text and stays allowed.
			violation = m.shellExpansionError("an unquoted brace expansion ({...})")
		case literal.glob:
			// Any unquoted glob may expand into the Ennote home even when its
			// literal text does not contain it, so every unquoted glob is
			// rejected rather than pattern-matched.
			violation = m.shellExpansionError("an unquoted glob pattern")
		case literal.tilde:
			// The shell expands a leading ~ only when the word starts with an
			// unquoted tilde; '~notes' and "~backup" stay literal text.
			violation = m.protectedPathError()
		case m.literalHitsHome(literal.value):
			violation = m.protectedPathError()
		}
		return violation == nil
	})
	return violation
}

// literalWord is the reconstruction of one shell word.
type literalWord struct {
	value string
	// mechanism names the first construct that defeats static analysis; empty
	// when the word is fully literal.
	mechanism string
	// glob reports an unquoted glob metacharacter, brace an unquoted brace
	// expansion, tilde a leading unquoted ~ (home expansion).
	glob  bool
	brace bool
	tilde bool
}

// unquotedBraceExpansion reports a real brace expansion inside one unquoted
// literal: a '{' whose body holds a ',' or '..' before the closing '}'. A
// standalone '{}' (find -exec ... {} +) or '{name}' is literal text, and the
// {a,b} form the parser understands is already a *syntax.BraceExp.
func unquotedBraceExpansion(value string) bool {
	for offset := 0; ; {
		open := strings.IndexByte(value[offset:], '{')
		if open < 0 {
			return false
		}
		open += offset
		shut := strings.IndexByte(value[open:], '}')
		if shut < 0 {
			return false
		}
		body := value[open+1 : open+shut]
		if strings.Contains(body, ",") || strings.Contains(body, "..") {
			return true
		}
		offset = open + 1
	}
}

// literalWordValue reconstructs the text a word denotes when it contains no
// expansion. Quoted metacharacters stay literal and are not reported.
func literalWordValue(word *syntax.Word) literalWord {
	var text strings.Builder
	result := literalWord{}
	var walk func(parts []syntax.WordPart, quoted bool)
	walk = func(parts []syntax.WordPart, quoted bool) {
		for index, part := range parts {
			if result.mechanism != "" {
				return
			}
			switch part := part.(type) {
			case *syntax.Lit:
				text.WriteString(part.Value)
				if !quoted {
					result.glob = result.glob || strings.ContainsAny(part.Value, "*?[")
					result.brace = result.brace || unquotedBraceExpansion(part.Value)
					// Tilde expansion only fires for a word that starts with an
					// unquoted literal ~; a quoted or later ~ is literal text.
					result.tilde = result.tilde || (index == 0 && strings.HasPrefix(part.Value, "~"))
				}
			case *syntax.SglQuoted:
				if part.Dollar {
					// $'...' decodes escapes (\x2f is '/'), so its value is
					// whatever the shell decodes, not what the guard sees.
					result.mechanism = "ANSI-C quoting ($'...')"
					return
				}
				text.WriteString(part.Value)
			case *syntax.DblQuoted:
				walk(part.Parts, true)
			case *syntax.ParamExp:
				result.mechanism = "parameter expansion ($VAR or ${...})"
			case *syntax.CmdSubst:
				result.mechanism = "command substitution ($(...) or `...`)"
			case *syntax.ProcSubst:
				result.mechanism = "process substitution (<(...) or >(...))"
			case *syntax.ArithmExp:
				result.mechanism = "arithmetic expansion ($((...)))"
			case *syntax.BraceExp:
				result.mechanism = "brace expansion ({...})"
			case *syntax.ExtGlob:
				result.mechanism = "an extended glob pattern"
			default:
				result.mechanism = "an unsupported shell construct"
			}
		}
	}
	walk(word.Parts, false)
	if result.mechanism != "" {
		return result
	}
	// The parser keeps backslash escapes in Lit values; the shell removes them.
	result.value = strings.ReplaceAll(text.String(), `\`, "")
	return result
}

// normalizeShellCommand strips syntax the shell deletes before the command
// runs: empty quote pairs and backslash escapes.
func normalizeShellCommand(command string) string {
	replacer := strings.NewReplacer("''", "", `""`, "", `\`, "")
	return replacer.Replace(command)
}

func (m *Manager) literalHitsHome(value string) bool {
	if value == "" {
		return false
	}
	if strings.Contains(value, "~/.ennote") || strings.Contains(value, "$HOME/.ennote") || strings.Contains(value, "$ENNOTE_HOME") {
		return true
	}
	cleaned := filepath.Clean(value)
	for _, path := range m.ProtectedPaths {
		path = filepath.Clean(path)
		if path == "" || path == "." || path == string(filepath.Separator) {
			continue
		}
		if cleaned == path || strings.HasPrefix(cleaned, path+string(filepath.Separator)) || strings.Contains(value, path) {
			return true
		}
		// The home directory keeps its own name under a different root (another
		// user's home, a symlinked or relocated home). Hidden directories are
		// matched by name; a custom-named home is only matched by full path.
		base := filepath.Base(path)
		if base == "." || base == string(filepath.Separator) || !strings.HasPrefix(base, ".") {
			continue
		}
		for _, component := range strings.Split(value, "/") {
			if component == base {
				return true
			}
		}
	}
	return false
}
