package skillsmgmt

import (
	"crypto/sha256"
	"encoding/hex"
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
	// ErrSkillExistsIdentical reports that the target directory already holds
	// exactly the bytes Write would publish, so nothing was written. It is a
	// distinct sentinel (not a wrap of ErrSkillExists) because an installer
	// treats it as a no-op reinstall while any other existing target is a
	// conflict.
	ErrSkillExistsIdentical = errors.New("skill already exists with identical content")
)

// Write outcomes reported by Write and PlanWrite. They describe what happened
// (or what would happen) to the target directory, which is what the Agent Pack
// installer needs to decide between create and a no-op reinstall.
const (
	WriteStateCreated         = "created"
	WriteStateExistsIdentical = "exists_identical"
)

// skillIDPattern constrains a Skill ID to a filesystem-safe slug: it must start
// with a lowercase letter, may continue with lowercase letters, digits, hyphen
// or underscore, and is at most 64 characters. Path separators, "..", the
// empty string, uppercase letters and spaces are all rejected by construction.
var skillIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// publishedSkillFiles are the files Write publishes, in digest order.
var publishedSkillFiles = []string{"skill.json", "SKILL.md"}

// skillDigestTag domain-separates the skill content digest from every other
// sha256 use in the Worker.
const skillDigestTag = "skillsmgmt-skill-v1"

// CreateInput describes a Skill to author under a skills root.
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

// ConflictError reports an existing target directory whose bytes differ from
// the requested Skill. It wraps ErrSkillExists so existing callers that match
// the sentinel keep working.
type ConflictError struct {
	ID   string
	Path string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: %s already exists at %s with different content", ErrSkillExists, e.ID, e.Path)
}

func (e *ConflictError) Unwrap() error { return ErrSkillExists }

// Rendered is the canonical file set Write publishes for one CreateInput.
type Rendered struct {
	ID           string
	Description  string
	AllowedTools []string
	Files        map[string][]byte
}

// Digest is the root-independent content digest of the rendered file set, the
// same value Write reports and ContentDigest recomputes from an installed
// directory.
func (r *Rendered) Digest() string {
	if r == nil {
		return ""
	}
	return digestRendered(r.Files)
}

// Create authors a new Skill directory (skill.json + SKILL.md) under
// Service.UserRoot. It is a thin wrapper over Write so the Agent Pack installer
// publishes Skills through exactly the same writer. An existing target is always
// a conflict here: the create-skill API never silently succeeds on a no-op.
func (s *Service) Create(input CreateInput) (*CreateResult, error) {
	root := strings.TrimSpace(s.UserRoot)
	if root == "" {
		return nil, fmt.Errorf("skills user root is not configured")
	}
	result, err := Write(root, input)
	if errors.Is(err, ErrSkillExistsIdentical) {
		return nil, fmt.Errorf("%w: %s", ErrSkillExists, input.ID)
	}
	return result, err
}

// Render normalizes input and returns the exact bytes Write publishes. It never
// touches the filesystem.
func Render(input CreateInput) (*Rendered, error) {
	id := input.ID
	if !skillIDPattern.MatchString(id) {
		return nil, fmt.Errorf("%w: %q must match %s", ErrSkillIDInvalid, id, skillIDPattern.String())
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return nil, fmt.Errorf("%w: prompt must not be empty", ErrSkillPromptRequired)
	}
	name := singleLine(input.Name)
	if name == "" {
		name = id
	}
	description := singleLine(input.Description)
	allowedTools := normalizeAllowedTools(input.AllowedTools)

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

	return &Rendered{
		ID:           id,
		Description:  description,
		AllowedTools: allowedTools,
		Files: map[string][]byte{
			"skill.json": encoded,
			"SKILL.md":   renderSkillMD(name, description, input.Prompt),
		},
	}, nil
}

// InspectWrite reports what Write(root, input) would do without writing
// anything: state is WriteStateCreated or WriteStateExistsIdentical and digest is
// the root-independent content digest of the published files. A target that
// exists with different content returns a *ConflictError.
func InspectWrite(root string, input CreateInput) (state string, digest string, err error) {
	rendered, err := Render(input)
	if err != nil {
		return "", "", err
	}
	base, err := cleanRoot(root)
	if err != nil {
		return "", "", err
	}
	state, err = classifyTarget(filepath.Join(base, rendered.ID), rendered)
	if err != nil {
		return "", "", err
	}
	return state, rendered.Digest(), nil
}

// Write authors a Skill directory (skill.json + SKILL.md) under root. The
// directory is staged in a hidden temp directory inside the same root,
// validated with the real loader, then published with a single rename, so a
// concurrent catalog read never sees a partially written Skill. Existing Skill
// directories are never overwritten: identical content reports
// WriteStateExistsIdentical, anything else returns a *ConflictError.
//
// root is the parent skills directory (the user skills root for global Skills,
// agents/graphs/<flow>/skills for a flow-scoped Skill).
func Write(root string, input CreateInput) (*CreateResult, error) {
	rendered, err := Render(input)
	if err != nil {
		return nil, err
	}
	base, err := cleanRoot(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, fmt.Errorf("create skills root: %w", err)
	}
	target := filepath.Join(base, rendered.ID)
	state, err := classifyTarget(target, rendered)
	if err != nil {
		return nil, err
	}
	if state == WriteStateExistsIdentical {
		return nil, fmt.Errorf("%w: %s", ErrSkillExistsIdentical, rendered.ID)
	}

	// The temp name is dot-prefixed, so catalog discovery skips it while the
	// Skill is being written.
	tmp, err := os.MkdirTemp(base, ".create-"+rendered.ID+"-")
	if err != nil {
		return nil, fmt.Errorf("create staging directory: %w", err)
	}
	defer func() {
		if tmp != "" {
			_ = os.RemoveAll(tmp)
		}
	}()

	if err := writeNewFile(filepath.Join(tmp, "skill.json"), rendered.Files["skill.json"]); err != nil {
		return nil, fmt.Errorf("write skill.json: %w", err)
	}
	if err := writeNewFile(filepath.Join(tmp, "SKILL.md"), rendered.Files["SKILL.md"]); err != nil {
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
		ID:           rendered.ID,
		Path:         target,
		Description:  rendered.Description,
		AllowedTools: rendered.AllowedTools,
	}, nil
}

// ContentDigest hashes the published files of an installed Skill at
// root/<id>. It reads the same two files Write publishes, so an installer can
// compare it against the digest it recorded to detect local edits.
func ContentDigest(root, id string) (string, error) {
	if !skillIDPattern.MatchString(id) {
		return "", fmt.Errorf("%w: %q must match %s", ErrSkillIDInvalid, id, skillIDPattern.String())
	}
	base, err := cleanRoot(root)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(base, id)
	files := make(map[string][]byte, len(publishedSkillFiles))
	for _, name := range publishedSkillFiles {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("skill file %s must be a regular file", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		files[name] = data
	}
	return digestRendered(files), nil
}

// classifyTarget reports whether target is absent (created) or present with
// exactly the rendered bytes (exists_identical). Every other shape — a symlink,
// a file, a directory with missing or different files — is a conflict.
func classifyTarget(target string, rendered *Rendered) (string, error) {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return WriteStateCreated, nil
		}
		return "", fmt.Errorf("inspect skill directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", &ConflictError{ID: rendered.ID, Path: target}
	}
	for _, name := range publishedSkillFiles {
		path := filepath.Join(target, name)
		fileInfo, err := os.Lstat(path)
		if err != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
			return "", &ConflictError{ID: rendered.ID, Path: target}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read existing skill file: %w", err)
		}
		if string(data) != string(rendered.Files[name]) {
			return "", &ConflictError{ID: rendered.ID, Path: target}
		}
	}
	return WriteStateExistsIdentical, nil
}

func cleanRoot(root string) (string, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return "", fmt.Errorf("skills root is not configured")
	}
	return filepath.Clean(trimmed), nil
}

// digestRendered is the canonical content digest of the published file set:
// sha256 over tag \0 name \0 sha256(bytes) \n for each published file.
func digestRendered(files map[string][]byte) string {
	hash := sha256.New()
	hash.Write([]byte(skillDigestTag))
	hash.Write([]byte{0})
	for _, name := range publishedSkillFiles {
		sum := sha256.Sum256(files[name])
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		hash.Write([]byte(hex.EncodeToString(sum[:])))
		hash.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
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
