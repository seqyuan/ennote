package skillsmgmt

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/skills"
)

// Sentinel errors surfaced by Service.Create so the API can map them to
// stable HTTP status codes instead of matching message text.
var (
	ErrSkillIDInvalid      = errors.New("invalid skill id")
	ErrSkillPromptRequired = errors.New("skill prompt is required")
	ErrSkillExists         = errors.New("skill already exists")
)

// skillIDPattern constrains a Skill ID to a filesystem-safe slug: it must start
// with a lowercase letter, may continue with lowercase letters, digits, hyphen
// or underscore, and is at most 64 characters. Path separators, "..", the
// empty string, uppercase letters and spaces are all rejected by construction.
var skillIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// CreateInput describes a Skill to author under the user skills root.
type CreateInput struct {
	ID           string
	Name         string
	Description  string
	Prompt       string
	AllowedTools []string
}

// CreateResult summarizes a freshly created Skill. Path is the absolute
// directory on the worker host (the parent of skill.json and SKILL.md).
type CreateResult struct {
	ID           string   `json:"id"`
	Path         string   `json:"path"`
	Description  string   `json:"description"`
	AllowedTools []string `json:"allowedTools"`
}

// Create authors a new Skill directory (skill.json + SKILL.md) under
// Service.UserRoot. The directory is staged in a hidden temp directory inside
// the same root, validated with the real loader, then published with a single
// rename, so a concurrent catalog read never sees a partially written Skill.
// Existing Skill directories are never overwritten.
func (s *Service) Create(input CreateInput) (*CreateResult, error) {
	id := input.ID
	if !skillIDPattern.MatchString(id) {
		return nil, fmt.Errorf("%w: %q must match %s", ErrSkillIDInvalid, id, skillIDPattern.String())
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return nil, fmt.Errorf("%w: prompt must not be empty", ErrSkillPromptRequired)
	}
	root := strings.TrimSpace(s.UserRoot)
	if root == "" {
		return nil, fmt.Errorf("skills user root is not configured")
	}
	root = filepath.Clean(root)

	name := singleLine(input.Name)
	if name == "" {
		name = id
	}
	description := singleLine(input.Description)
	allowedTools := normalizeAllowedTools(input.AllowedTools)

	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create skills root: %w", err)
	}
	target := filepath.Join(root, id)
	// Lstat (not Stat) so an existing symlink or directory is a conflict
	// rather than something we follow.
	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrSkillExists, id)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect skill directory: %w", err)
	}

	// The temp name is dot-prefixed, so catalog discovery skips it while the
	// Skill is being written.
	tmp, err := os.MkdirTemp(root, ".create-"+id+"-")
	if err != nil {
		return nil, fmt.Errorf("create staging directory: %w", err)
	}
	defer func() {
		if tmp != "" {
			_ = os.RemoveAll(tmp)
		}
	}()

	manifest := skills.Manifest{
		ID:           id,
		Version:      "1",
		Prompt:       "SKILL.md",
		Description:  description,
		AllowedTools: allowedTools,
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode skill manifest: %w", err)
	}
	encoded = append(encoded, '\n')

	if err := writeNewFile(filepath.Join(tmp, "skill.json"), encoded); err != nil {
		return nil, fmt.Errorf("write skill.json: %w", err)
	}
	if err := writeNewFile(filepath.Join(tmp, "SKILL.md"), renderSkillMD(name, description, input.Prompt)); err != nil {
		return nil, fmt.Errorf("write SKILL.md: %w", err)
	}

	// Validate the staged Skill with the same loader the live catalog uses, so
	// a Skill that cannot be loaded is never published.
	if _, err := skills.Load(tmp); err != nil {
		return nil, fmt.Errorf("validate staged skill: %w", err)
	}

	// Single rename publishes both files atomically under the final ID.
	if err := os.Rename(tmp, target); err != nil {
		return nil, fmt.Errorf("publish skill directory: %w", err)
	}
	tmp = "" // published; nothing left to clean up

	return &CreateResult{
		ID:           id,
		Path:         target,
		Description:  description,
		AllowedTools: allowedTools,
	}, nil
}

// renderSkillMD builds a SKILL.md document with YAML frontmatter followed by
// the prompt body. Frontmatter values are folded to a single line because the
// loader parses the block line by line.
func renderSkillMD(name, description, prompt string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + name + "\n")
	if description != "" {
		b.WriteString("description: " + description + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(prompt, "\r\n"))
	b.WriteString("\n")
	return []byte(b.String())
}

// singleLine trims a frontmatter-bound value and folds embedded newlines into
// spaces so the frontmatter block always stays line-oriented.
func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// normalizeAllowedTools trims tool names, drops empties, and de-duplicates
// while preserving caller order. The result is never nil so JSON encodes [].
func normalizeAllowedTools(tools []string) []string {
	out := make([]string, 0, len(tools))
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// writeNewFile creates a new 0600 file and refuses to follow an existing entry.
func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
