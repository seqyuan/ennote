package skills

import (
	"fmt"
	"strings"
)

// BuildCatalogPrompt generates the <available_skills> block for the system prompt.
// It only includes root-level nodes. The prompt is bounded by maxBytes (16 KiB).
func BuildCatalogPrompt(catalog *Catalog, maxBytes int) string {
	if catalog == nil || len(catalog.Roots) == 0 {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = 16 * 1024
	}

	var sb strings.Builder
	sb.WriteString("<available_skills>\n")
	sb.WriteString("Skills are grouped hierarchically. For a matching category, read its category.md,\n")
	sb.WriteString("then continue until a leaf skill is found. Read that SKILL.md before using it.\n")
	sb.WriteString("Relative references in a skill resolve against the directory containing SKILL.md.\n\n")

	for _, root := range catalog.Roots {
		var line string
		if root.Kind == NodeSkill {
			line = fmt.Sprintf("- skill `%s`: %s. Location: `/skills/%s/SKILL.md`\n",
				sanitizePromptName(root.Name), sanitizePromptDesc(root.Description), root.RelPath)
		} else {
			line = fmt.Sprintf("- category `%s`: %s. Location: `/skills/%s/category.md`\n",
				sanitizePromptName(root.Name), sanitizePromptDesc(root.Description), root.RelPath)
		}

		sb.WriteString(line)
		if sb.Len() > maxBytes {
			return "" // caller should generate an error
		}
	}

	sb.WriteString("</available_skills>\n")
	if sb.Len() > maxBytes {
		return ""
	}
	return sb.String()
}

func sanitizePromptName(name string) string {
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\r", " ")
	// Truncate long names
	if len(name) > 120 {
		name = name[:117] + "..."
	}
	return name
}

func sanitizePromptDesc(desc string) string {
	desc = strings.ReplaceAll(desc, "\n", " ")
	desc = strings.ReplaceAll(desc, "\r", " ")
	if len(desc) > 240 {
		desc = desc[:237] + "..."
	}
	return desc
}
