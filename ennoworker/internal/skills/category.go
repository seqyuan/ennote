package skills

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type Category struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ParseCategory parses a category.md file. It expects YAML frontmatter
// delimited by --- on the first line and a closing ---, followed by
// Markdown body. The description must not exceed 240 Unicode code points
// and must not contain newlines or control characters.
func ParseCategory(data []byte, dirName string) (*Category, string, error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return nil, "", fmt.Errorf("category.md must start with YAML frontmatter (---)")
	}

	rest := text[4:]
	closeIdx := strings.Index(rest, "\n---")
	if closeIdx < 0 {
		return nil, "", fmt.Errorf("category.md YAML frontmatter is not closed (---)")
	}
	frontmatter := rest[:closeIdx]
	body := rest[closeIdx+4:]
	// Strip leading newline after ---
	body = strings.TrimPrefix(body, "\n")

	var cat Category
	if err := yaml.Unmarshal([]byte(frontmatter), &cat); err != nil {
		return nil, "", fmt.Errorf("parse category YAML: %w", err)
	}

	// Validate description
	if cat.Description == "" {
		return nil, "", fmt.Errorf("category description is required")
	}
	if strings.ContainsRune(cat.Description, '\n') || strings.ContainsRune(cat.Description, '\r') {
		return nil, "", fmt.Errorf("category description must not contain newlines")
	}
	if containsControl(cat.Description) {
		return nil, "", fmt.Errorf("category description must not contain control characters")
	}
	if utf8.RuneCountInString(cat.Description) > 240 {
		return nil, "", fmt.Errorf("category description exceeds 240 Unicode code points")
	}

	// Validate name
	if strings.ContainsRune(cat.Name, '\n') || strings.ContainsRune(cat.Name, '\r') {
		return nil, "", fmt.Errorf("category name must not contain newlines")
	}
	if containsControl(cat.Name) {
		return nil, "", fmt.Errorf("category name must not contain control characters")
	}

	if cat.Name == "" {
		cat.Name = dirName
	}

	return &cat, body, nil
}

func containsControl(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return true
		}
		if r == 0x7f {
			return true
		}
	}
	return false
}
