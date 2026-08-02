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

// IsSkillLeaf returns true if the directory contains a regular-file skill.json.
// It uses Lstat so symlinks are not followed and can be diagnosed upstream.
func IsSkillLeaf(dir string) (bool, error) {
	manifestPath := filepath.Join(dir, "skill.json")
	fi, err := os.Lstat(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, nil
	}
	// Also check there's no category.md (mutual exclusion)
	catPath := filepath.Join(dir, "category.md")
	catFi, catErr := os.Lstat(catPath)
	if catErr == nil && catFi.Mode().IsRegular() {
		return false, fmt.Errorf("directory contains both skill.json and category.md")
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
		return nil, fmt.Errorf("read skill.json: %w", err)
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
