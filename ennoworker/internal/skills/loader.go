package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Manifest struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Prompt       string   `json:"prompt"`
	AllowedTools []string `json:"allowedTools,omitempty"`
	Description  string   `json:"description,omitempty"`
}

type LoadedSkill struct {
	Manifest     Manifest
	BaseDir      string
	PromptText   string
	ManifestHash string
	ContentHash  string
	RelPath      string // directory locator, e.g. scRNA/pseudotime
	SourceRoot   string // "user" | "builtin"
}

// IsSkillLeaf returns true if the directory contains a regular-file skill.json
// or SKILL.md (pi-ecosystem skills are SKILL.md-only; ennote synthesizes a
// manifest for them). It uses Lstat so symlinks are not followed and can be
// diagnosed upstream.
func IsSkillLeaf(dir string) (bool, error) {
	hasManifest := false
	for _, name := range []string{"skill.json", "SKILL.md", "skill.md"} {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if fi.Mode().IsRegular() {
			hasManifest = true
			break
		}
	}
	if !hasManifest {
		return false, nil
	}
	// Also check there's no category.md (mutual exclusion)
	catPath := filepath.Join(dir, "category.md")
	catFi, catErr := os.Lstat(catPath)
	if catErr == nil && catFi.Mode().IsRegular() {
		return false, fmt.Errorf("directory contains both a skill manifest and category.md")
	}
	return true, nil
}

// IsCategoryDir returns true if the directory contains a regular-file category.md
// and does NOT contain skill.json or SKILL.md.
func IsCategoryDir(dir string) (bool, error) {
	catPath := filepath.Join(dir, "category.md")
	catFi, err := os.Lstat(catPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !catFi.Mode().IsRegular() {
		return false, nil
	}
	// Must not contain skill.json or SKILL.md
	for _, name := range []string{"skill.json", "SKILL.md"} {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err == nil && fi.Mode().IsRegular() {
			return false, fmt.Errorf("category directory %q must not contain %s", dir, name)
		}
	}
	return true, nil
}

func Load(dir string) (*LoadedSkill, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("skill directory not found: %w", err)
	}
	manifestPath := filepath.Join(dir, "skill.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read skill.json: %w", err)
		}
		return loadSkillMDFallback(dir)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse skill.json: %w", err)
	}
	if m.ID == "" {
		return nil, fmt.Errorf("skill id is required")
	}
	if m.Version == "" {
		m.Version = "0.1.0"
	}

	// Validate prompt: must be empty or exactly "SKILL.md"
	if m.Prompt != "" && m.Prompt != "SKILL.md" {
		return nil, fmt.Errorf("skill.json prompt must be empty or \"SKILL.md\", got %q", m.Prompt)
	}

	promptFile := m.Prompt
	if promptFile == "" {
		promptFile = "SKILL.md"
	}
	promptData, err := os.ReadFile(filepath.Join(dir, promptFile))
	if err != nil {
		return nil, fmt.Errorf("read skill prompt %s: %w", promptFile, err)
	}
	promptText := string(promptData)

	manifestHash := sha256Hex(data)

	contentHash, err := dirHash(dir)
	if err != nil {
		return nil, fmt.Errorf("compute content hash: %w", err)
	}

	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err == nil {
			dir = abs
		}
	}

	return &LoadedSkill{
		Manifest:     m,
		BaseDir:      dir,
		PromptText:   promptText,
		ManifestHash: manifestHash,
		ContentHash:  contentHash,
	}, nil
}

// loadSkillMDFallback loads a pi-ecosystem skill directory that has SKILL.md
// but no skill.json. The manifest is synthesized: ID from the frontmatter
// "name" (falling back to a slug of the directory name), Version "1",
// Description from frontmatter when present, Prompt "SKILL.md". The prompt
// body has the frontmatter block stripped.
func loadSkillMDFallback(dir string) (*LoadedSkill, error) {
	skillPath := filepath.Join(dir, "SKILL.md")
	if _, statErr := os.Lstat(skillPath); statErr != nil {
		skillPath = filepath.Join(dir, "skill.md")
		if _, statErr = os.Lstat(skillPath); statErr != nil {
			return nil, fmt.Errorf("skill directory has neither skill.json nor SKILL.md")
		}
	}
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md: %w", err)
	}
	body, name, description := splitSkillFrontmatter(string(data))
	if name == "" {
		name = slugify(filepath.Base(dir))
	}
	if name == "" {
		return nil, fmt.Errorf("cannot derive skill id from directory %q", dir)
	}

	m := Manifest{
		ID:          name,
		Version:     "1",
		Prompt:      "SKILL.md",
		Description: description,
	}
	synth, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("synthesize manifest: %w", err)
	}

	contentHash, err := dirHash(dir)
	if err != nil {
		return nil, fmt.Errorf("compute content hash: %w", err)
	}
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err == nil {
			dir = abs
		}
	}

	return &LoadedSkill{
		Manifest:     m,
		BaseDir:      dir,
		PromptText:   body,
		ManifestHash: sha256Hex(synth),
		ContentHash:  contentHash,
	}, nil
}

// splitSkillFrontmatter extracts the YAML frontmatter block (--- ... ---) from
// a SKILL.md document and returns (body, name, description). Only the name and
// description keys are consumed; all other frontmatter is preserved for tools
// but not modeled by Manifest. Multi-line (| / >) description values take the
// first folded line for compactness.
func splitSkillFrontmatter(doc string) (body, name, description string) {
	const delim = "---"
	if !strings.HasPrefix(doc, delim) {
		return doc, "", ""
	}
	rest := doc[len(delim):]
	end := strings.Index(rest, "\n"+delim)
	if end < 0 {
		return doc, "", ""
	}
	front := rest[:end]
	body = rest[end+len("\n"+delim):]
	if strings.HasPrefix(body, "\n") {
		body = body[1:]
	}
	for i, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			if name == "" {
				name = strings.TrimSpace(value)
			}
		case "description":
			if description == "" {
				trimmed := strings.TrimSpace(value)
				if trimmed == "" {
					continue
				}
				if trimmed == "|" || trimmed == ">" || strings.HasPrefix(trimmed, "|-") || strings.HasPrefix(trimmed, ">-") {
					// Block scalar: consume the following indented lines; take the
					// first folded line for compactness.
					var folded []string
					for _, next := range strings.Split(front, "\n")[i+1:] {
						if strings.TrimSpace(next) == "" {
							continue
						}
						if next[0] != ' ' && next[0] != '\t' {
							break
						}
						folded = append(folded, strings.TrimSpace(next))
					}
					description = strings.Join(folded, " ")
				} else {
					description = trimmed
				}
			}
		}
	}
	return body, name, description
}

// slugify lowercases and converts any run of non [a-z0-9_-] characters into a
// single dash, trimming leading/trailing dashes, so directory names become
// stable skill IDs.
func slugify(s string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (s *LoadedSkill) SnapshotPath(runDir string) string {
	return filepath.Join(runDir, "skills", s.Manifest.ID)
}

func (s *LoadedSkill) CopyToSnapshot(runDir string) (string, error) {
	dest := s.SnapshotPath(runDir)
	if err := os.RemoveAll(dest); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("clean snapshot dir: %w", err)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return "", fmt.Errorf("create snapshot dir: %w", err)
	}
	if err := copyDir(s.BaseDir, dest); err != nil {
		return "", fmt.Errorf("copy skill to snapshot: %w", err)
	}
	verifyHash, err := dirHash(dest)
	if err != nil {
		return "", fmt.Errorf("verify snapshot hash: %w", err)
	}
	if verifyHash != s.ContentHash {
		return "", fmt.Errorf("snapshot hash mismatch: expected %s got %s", s.ContentHash, verifyHash)
	}
	return dest, nil
}

func Discover(baseDirs ...string) []*LoadedSkill {
	var skills []*LoadedSkill
	seen := map[string]bool{}
	for _, baseDir := range baseDirs {
		if baseDir == "" {
			continue
		}
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			dir := filepath.Join(baseDir, entry.Name())
			if seen[dir] {
				continue
			}
			seen[dir] = true
			skill, err := Load(dir)
			if err != nil {
				continue
			}
			skills = append(skills, skill)
		}
	}
	return skills
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func dirHash(dir string) (string, error) {
	h := sha256.New()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
