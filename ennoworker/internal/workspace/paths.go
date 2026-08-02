package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrPathEscape = errors.New("path escapes workspace")
var ErrReadOnlyMount = errors.New("mount is read-only")

type mountEntry struct {
	virtualPrefix string // "/workspace" | "/skills"
	hostRoot      string // canonical absolute path
	writable      bool
}

// Jail resolves virtual paths to host paths across multiple mount points.
type Jail struct {
	mounts []mountEntry // sorted by virtualPrefix length desc; matching requires path-segment boundary
}

// NewJail creates a workspace-only Jail (backward compatible).
func NewJail(root string) (*Jail, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("canonicalize workspace root: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}
	return &Jail{mounts: []mountEntry{{
		virtualPrefix: "/workspace",
		hostRoot:      filepath.Clean(canonical),
		writable:      true,
	}}}, nil
}

// NewJailWithSkills creates a dual-mount Jail with workspace and skills.
func NewJailWithSkills(workspaceRoot, skillsRoot string) (*Jail, error) {
	if workspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	wsAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	wsCanon, err := filepath.EvalSymlinks(wsAbs)
	if err != nil {
		return nil, fmt.Errorf("canonicalize workspace root: %w", err)
	}
	wsInfo, err := os.Stat(wsCanon)
	if err != nil {
		return nil, fmt.Errorf("stat workspace root: %w", err)
	}
	if !wsInfo.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}
	wsCanon = filepath.Clean(wsCanon)

	mounts := []mountEntry{{
		virtualPrefix: "/workspace",
		hostRoot:      wsCanon,
		writable:      true,
	}}

	if skillsRoot != "" {
		skAbs, err := filepath.Abs(skillsRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve skills root: %w", err)
		}
		skCanon, err := filepath.EvalSymlinks(skAbs)
		if err != nil {
			return nil, fmt.Errorf("canonicalize skills root: %w", err)
		}
		skInfo, err := os.Stat(skCanon)
		if err != nil {
			return nil, fmt.Errorf("stat skills root: %w", err)
		}
		if !skInfo.IsDir() {
			return nil, fmt.Errorf("skills root is not a directory")
		}
		skCanon = filepath.Clean(skCanon)

		// Verify no overlap
		if isWithinOrEqual(wsCanon, skCanon) || isWithinOrEqual(skCanon, wsCanon) {
			return nil, fmt.Errorf("workspace and skills roots must not overlap")
		}

		mounts = append(mounts, mountEntry{
			virtualPrefix: "/skills",
			hostRoot:      skCanon,
			writable:      false,
		})
	}

	// Sort by virtualPrefix length desc for longest-prefix match
	sort.Slice(mounts, func(i, j int) bool {
		return len(mounts[i].virtualPrefix) > len(mounts[j].virtualPrefix)
	})

	return &Jail{mounts: mounts}, nil
}

// VerifyNoOverlap checks that a runtime I/O root does not overlap with any mount.
func (j *Jail) VerifyNoOverlap(ioRoot string) error {
	abs, err := filepath.Abs(ioRoot)
	if err != nil {
		return err
	}
	canon, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	canon = filepath.Clean(canon)
	for _, m := range j.mounts {
		if isWithinOrEqual(canon, m.hostRoot) || isWithinOrEqual(m.hostRoot, canon) {
			return fmt.Errorf("runtime I/O root overlaps with mount %s", m.virtualPrefix)
		}
	}
	return nil
}

func isWithinOrEqual(a, b string) bool {
	if a == b {
		return true
	}
	rel, err := filepath.Rel(b, a)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (j *Jail) Root() string {
	for _, m := range j.mounts {
		if m.virtualPrefix == "/workspace" {
			return m.hostRoot
		}
	}
	return ""
}

func (j *Jail) ResolveExisting(input string) (string, error) {
	candidate, mount, err := j.resolveMount(input)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !j.containsMount(canonical, mount) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, input)
	}
	return canonical, nil
}

func (j *Jail) ResolveWorkspaceExisting(input string) (string, error) {
	candidate, mount, err := j.resolveMount(input)
	if err != nil {
		return "", err
	}
	if mount.virtualPrefix != "/workspace" {
		return "", fmt.Errorf("%w: %s is not in workspace", ErrPathEscape, input)
	}
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !j.containsMount(canonical, mount) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, input)
	}
	return canonical, nil
}

func (j *Jail) ResolveForWrite(input string) (string, error) {
	candidate, mount, err := j.resolveMount(input)
	if err != nil {
		return "", err
	}
	if !mount.writable {
		return "", fmt.Errorf("%w: %s", ErrReadOnlyMount, input)
	}

	existing := candidate
	var missing []string
	for {
		_, statErr := os.Lstat(existing)
		if statErr == nil {
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("%w: no existing workspace ancestor", ErrPathEscape)
		}
		missing = append(missing, filepath.Base(existing))
		existing = parent
	}
	canonical, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if !j.containsMount(canonical, mount) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, input)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		canonical = filepath.Join(canonical, missing[index])
	}
	if !j.containsMount(canonical, mount) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, input)
	}
	return canonical, nil
}

func (j *Jail) DisplayPath(hostPath string) (string, error) {
	canonical := filepath.Clean(hostPath)
	for i := range j.mounts {
		m := &j.mounts[i]
		if j.containsMount(canonical, m) {
			relative, err := filepath.Rel(m.hostRoot, canonical)
			if err != nil {
				return "", err
			}
			if relative == "." {
				return m.virtualPrefix, nil
			}
			return m.virtualPrefix + "/" + filepath.ToSlash(relative), nil
		}
	}
	return "", ErrPathEscape
}

func (j *Jail) DisplayWorkspacePath(hostPath string) (string, error) {
	canonical := filepath.Clean(hostPath)
	for i := range j.mounts {
		m := &j.mounts[i]
		if m.virtualPrefix == "/workspace" && j.containsMount(canonical, m) {
			relative, err := filepath.Rel(m.hostRoot, canonical)
			if err != nil {
				return "", err
			}
			if relative == "." {
				return "/workspace", nil
			}
			return "/workspace/" + filepath.ToSlash(relative), nil
		}
	}
	return "", ErrPathEscape
}

func (j *Jail) resolveMount(input string) (string, *mountEntry, error) {
	value := strings.TrimSpace(input)
	if value == "" || value == "." {
		// Default to workspace
		for i := range j.mounts {
			if j.mounts[i].virtualPrefix == "/workspace" {
				return j.mounts[i].hostRoot, &j.mounts[i], nil
			}
		}
		return "", nil, fmt.Errorf("%w: no workspace mount", ErrPathEscape)
	}

	// Try absolute virtual paths against mounts with segment-boundary matching
	if filepath.IsAbs(value) {
		slash := filepath.ToSlash(filepath.Clean(value))
		for i := range j.mounts {
			m := &j.mounts[i]
			if slash == m.virtualPrefix {
				return m.hostRoot, m, nil
			}
			if strings.HasPrefix(slash, m.virtualPrefix+"/") {
				rest := slash[len(m.virtualPrefix):]
				candidate := filepath.Join(m.hostRoot, filepath.FromSlash(rest))
				if j.containsMount(candidate, m) {
					return candidate, m, nil
				}
				return "", nil, fmt.Errorf("%w: %s", ErrPathEscape, input)
			}
		}
		return "", nil, fmt.Errorf("%w: absolute path must start with /workspace or /skills", ErrPathEscape)
	}

	// Relative path: default to workspace
	for i := range j.mounts {
		if j.mounts[i].virtualPrefix == "/workspace" {
			m := &j.mounts[i]
			clean := filepath.Clean(filepath.FromSlash(value))
			if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
				return "", nil, fmt.Errorf("%w: %s", ErrPathEscape, input)
			}
			candidate := filepath.Join(m.hostRoot, clean)
			if !j.containsMount(candidate, m) {
				return "", nil, fmt.Errorf("%w: %s", ErrPathEscape, input)
			}
			return candidate, m, nil
		}
	}
	return "", nil, fmt.Errorf("%w: no workspace mount", ErrPathEscape)
}

func (j *Jail) containsMount(path string, mount *mountEntry) bool {
	relative, err := filepath.Rel(mount.hostRoot, filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
