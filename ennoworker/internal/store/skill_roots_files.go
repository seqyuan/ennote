package store

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func (r *SkillRootRepo) listFileRoots() ([]SkillRoot, error) {
	settings, err := r.Settings.Read()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	roots := make([]SkillRoot, 0, len(settings.SkillRoots))
	for index, path := range settings.SkillRoots {
		roots = append(roots, SkillRoot{
			ID: fileSkillRootID(path), Name: filepath.Base(path), Path: path,
			AgentKind: "generic", Priority: (index + 1) * 10, Enabled: true,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	return roots, nil
}

func (r *SkillRootRepo) createFileRoot(input CreateSkillRootInput) (*SkillRoot, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Path = strings.TrimSpace(input.Path)
	if input.Name == "" || input.Path == "" {
		return nil, fmt.Errorf("name and path are required")
	}
	absolute, err := filepath.Abs(input.Path)
	if err != nil {
		return nil, err
	}
	absolute = filepath.Clean(absolute)
	roots, err := r.listFileRoots()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(roots)+1)
	for _, root := range roots {
		if root.Path == absolute {
			return nil, fmt.Errorf("a skill root for %q already exists", absolute)
		}
		paths = append(paths, root.Path)
	}
	if input.Enabled {
		paths = append(paths, absolute)
		if err := r.Settings.SetSkillRoots(paths); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	priority := input.Priority
	if priority <= 0 {
		priority = (len(paths)) * 10
	}
	kind := strings.TrimSpace(input.AgentKind)
	if kind == "" {
		kind = "generic"
	}
	return &SkillRoot{ID: fileSkillRootID(absolute), Name: input.Name, Path: absolute,
		AgentKind: kind, Priority: priority, Enabled: input.Enabled, CreatedAt: now, UpdatedAt: now}, nil
}

func (r *SkillRootRepo) getFileRoot(id string) (*SkillRoot, error) {
	roots, err := r.listFileRoots()
	if err != nil {
		return nil, err
	}
	for index := range roots {
		if roots[index].ID == id {
			return &roots[index], nil
		}
	}
	return nil, ErrSkillRootNotFound
}

func (r *SkillRootRepo) updateFileRoot(id string, patch struct {
	Name      *string
	Path      *string
	AgentKind *string
	Priority  *int
	Enabled   *bool
}) (*SkillRoot, error) {
	current, err := r.getFileRoot(id)
	if err != nil {
		return nil, err
	}
	roots, err := r.listFileRoots()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		if root.ID != id {
			paths = append(paths, root.Path)
		}
	}
	if patch.Path != nil {
		absolute, err := filepath.Abs(strings.TrimSpace(*patch.Path))
		if err != nil {
			return nil, err
		}
		current.Path = filepath.Clean(absolute)
		current.ID = fileSkillRootID(current.Path)
	}
	if patch.Name != nil {
		current.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.AgentKind != nil {
		current.AgentKind = strings.TrimSpace(*patch.AgentKind)
	}
	if patch.Priority != nil {
		current.Priority = *patch.Priority
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	if current.Enabled {
		paths = append(paths, current.Path)
	}
	if err := r.Settings.SetSkillRoots(paths); err != nil {
		return nil, err
	}
	current.UpdatedAt = time.Now().UTC()
	return current, nil
}

func (r *SkillRootRepo) deleteFileRoot(id string) error {
	roots, err := r.listFileRoots()
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(roots))
	found := false
	for _, root := range roots {
		if root.ID == id {
			found = true
			continue
		}
		paths = append(paths, root.Path)
	}
	if !found {
		return ErrSkillRootNotFound
	}
	return r.Settings.SetSkillRoots(paths)
}

func fileSkillRootID(path string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(path)))
	return fmt.Sprintf("root-%x", digest[:8])
}
