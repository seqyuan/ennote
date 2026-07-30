package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrPathEscape = errors.New("path escapes workspace")

type Jail struct {
	root string
}

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
	return &Jail{root: filepath.Clean(canonical)}, nil
}

func (j *Jail) Root() string { return j.root }

func (j *Jail) ResolveExisting(input string) (string, error) {
	candidate, err := j.lexicalCandidate(input)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !j.contains(canonical) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, input)
	}
	return canonical, nil
}

func (j *Jail) ResolveForWrite(input string) (string, error) {
	candidate, err := j.lexicalCandidate(input)
	if err != nil {
		return "", err
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
	if !j.contains(canonical) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, input)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		canonical = filepath.Join(canonical, missing[index])
	}
	if !j.contains(canonical) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, input)
	}
	return canonical, nil
}

func (j *Jail) DisplayPath(hostPath string) (string, error) {
	canonical := filepath.Clean(hostPath)
	if !j.contains(canonical) {
		return "", ErrPathEscape
	}
	relative, err := filepath.Rel(j.root, canonical)
	if err != nil {
		return "", err
	}
	if relative == "." {
		return "/workspace", nil
	}
	return "/workspace/" + filepath.ToSlash(relative), nil
}

func (j *Jail) lexicalCandidate(input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" || value == "." || value == "/workspace" {
		return j.root, nil
	}
	if filepath.IsAbs(value) {
		slash := filepath.ToSlash(filepath.Clean(value))
		if slash != "/workspace" && !strings.HasPrefix(slash, "/workspace/") {
			return "", fmt.Errorf("%w: absolute path must start with /workspace", ErrPathEscape)
		}
		value = strings.TrimPrefix(slash, "/workspace/")
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, input)
	}
	candidate := filepath.Join(j.root, clean)
	if !j.contains(candidate) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, input)
	}
	return candidate, nil
}

func (j *Jail) contains(path string) bool {
	relative, err := filepath.Rel(j.root, filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
