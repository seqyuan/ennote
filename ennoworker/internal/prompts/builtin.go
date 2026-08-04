package prompts

import (
	"embed"
	"path/filepath"
	"strings"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// LoadBuiltins parses all embedded builtin templates. It fails on any parse
// error (a builtin that fails to parse is a compile-time programming error).
func LoadBuiltins() ([]Template, error) {
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil, err
	}
	var templates []Template
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := builtinFS.ReadFile(filepath.Join("builtin", e.Name()))
		if err != nil {
			return nil, err
		}
		tmpl, err := ParseTemplate(data, e.Name())
		if err != nil {
			return nil, err
		}
		// Set tier after parse — the parse doesn't know about tiers.
		tmpl.Tier = TierBuiltin
		tmpl.Source = "builtin"
		tmpl.Path = filepath.Join("builtin", e.Name())
		templates = append(templates, tmpl)
	}
	return templates, nil
}
