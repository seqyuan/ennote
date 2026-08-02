package skills

import (
	"fmt"
	"strings"
)

// RenderTrustedTemplate processes a Skill prompt template and substitutes
// explicitly trusted variables supplied by the worker. Unknown placeholders,
// shell-style defaults, and characters not in the trusted set are left as-is.
//
// Supported variables:
//
//	${workspace}  - the visible workspace path for tool execution
//	${skill_dir}  - the per-run snapshot path of the current Skill
//
// Escaping: $$ produces a literal $ so $${workspace} renders as ${workspace}.
func RenderTrustedTemplate(raw string, vars map[string]string) (string, error) {
	if len(vars) == 0 {
		return raw, nil
	}

	// Validation: all required keys must have non-empty values.
	for _, name := range []string{"workspace", "skill_dir"} {
		if _, ok := vars[name]; ok {
			if vars[name] == "" {
				return "", fmt.Errorf("trusted variable %q must not be empty", name)
			}
		}
	}

	supported := map[string]bool{"workspace": true, "skill_dir": true}

	var result strings.Builder
	result.Grow(len(raw))
	i := 0
	for i < len(raw) {
		if raw[i] != '$' || i+1 >= len(raw) {
			result.WriteByte(raw[i])
			i++
			continue
		}
		// Handle $$ escaping.
		if raw[i+1] == '$' {
			result.WriteByte('$')
			i += 2
			continue
		}
		// Only process ${name} syntax.
		if raw[i+1] != '{' {
			result.WriteByte('$')
			i++
			continue
		}
		// Find the closing brace.
		close := strings.IndexByte(raw[i+2:], '}')
		if close < 0 {
			// Malformed: no closing brace. Output as-is.
			result.WriteByte('$')
			i++
			continue
		}
		close += i + 2 // absolute position of '}'

		name := raw[i+2 : close]
		if supported[name] {
			// Supported: check if value was provided.
			if val, ok := vars[name]; ok {
				result.WriteString(val)
			} else {
				// Missing but required: leave untransformed.
				result.WriteString(raw[i : close+1])
			}
		} else {
			// Unknown placeholder: leave as-is.
			result.WriteString(raw[i : close+1])
		}
		i = close + 1
	}

	return result.String(), nil
}
