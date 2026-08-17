package projectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const SchemaVersion = 1

// ErrNotFound reports that a project manifest does not exist (unknown or
// already-deleted project id).
var ErrNotFound = errors.New("project not found")

type GraphBinding struct {
	GraphID  string `json:"graphId"`
	Revision string `json:"revision"`
	Enabled  bool   `json:"enabled"`
}

type MCPBinding struct {
	ID                      string            `json:"id"`
	ProfileVersionID        string            `json:"profileVersionId"`
	DesiredEnabled          bool              `json:"desiredEnabled"`
	Required                bool              `json:"required"`
	SelectedRemoteToolNames []string          `json:"selectedRemoteToolNames"`
	CredentialRefs          map[string]string `json:"credentialRefs,omitempty"`
	Revision                int               `json:"revision"`
	CreatedAt               time.Time         `json:"createdAt"`
	UpdatedAt               time.Time         `json:"updatedAt"`
}

type Manifest struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Project       domain.Project          `json:"project"`
	Workspace     domain.ProjectWorkspace `json:"workspace"`
	GraphBindings []GraphBinding          `json:"graphBindings"`
	MCPBindings   []MCPBinding            `json:"mcpBindings"`
}

type Store struct {
	Root string
	Now  func() time.Time
}

func (s *Store) CreateWithWorkspace(_ context.Context, input domain.CreateProjectInput) (*domain.Project, *domain.ProjectWorkspace, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return nil, nil, fmt.Errorf("project name is required")
	}
	hostPath, err := canonicalHostPath(input.HostPath)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	projectID := uuid.NewString()
	workspaceID := uuid.NewString()
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(hostPath)))[:16]
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		Project: domain.Project{
			ID: projectID, Name: input.Name, Description: input.Description,
			Status: "active", CreatedAt: now, UpdatedAt: now,
		},
		Workspace: domain.ProjectWorkspace{
			ID: workspaceID, ProjectID: projectID, Kind: "local", HostPath: hostPath,
			VirtualPath: "/workspace", Status: "active", PathFingerprint: fingerprint,
			CreatedAt: now,
		},
		GraphBindings: []GraphBinding{}, MCPBindings: []MCPBinding{},
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create projects root: %w", err)
	}
	temporary, err := os.MkdirTemp(s.Root, ".project-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create project temp directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return nil, nil, err
	}
	for _, directory := range []string{filepath.Join(temporary, "artifacts"), filepath.Join(temporary, "sessions")} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return nil, nil, err
		}
	}
	if err := writeManifest(filepath.Join(temporary, "project.json"), manifest); err != nil {
		return nil, nil, err
	}
	if err := syncDirectory(temporary); err != nil {
		return nil, nil, err
	}
	final := filepath.Join(s.Root, projectID)
	if err := os.Rename(temporary, final); err != nil {
		return nil, nil, fmt.Errorf("publish project directory: %w", err)
	}
	if err := syncDirectory(s.Root); err != nil {
		return nil, nil, err
	}
	project, workspace := manifest.Project, manifest.Workspace
	return &project, &workspace, nil
}

func (s *Store) List(_ context.Context) ([]domain.Project, error) {
	manifests, err := s.listManifests()
	if err != nil {
		return nil, err
	}
	projects := make([]domain.Project, 0, len(manifests))
	for _, manifest := range manifests {
		if manifest.Project.Status != "archived" && manifest.Project.Status != "deleted" {
			projects = append(projects, manifest.Project)
		}
	}
	sort.Slice(projects, func(i, j int) bool {
		if !projects[i].UpdatedAt.Equal(projects[j].UpdatedAt) {
			return projects[i].UpdatedAt.After(projects[j].UpdatedAt)
		}
		return projects[i].ID < projects[j].ID
	})
	return projects, nil
}

func (s *Store) FindByID(_ context.Context, id string) (*domain.Project, error) {
	manifest, err := s.read(id)
	if errors.Is(err, os.ErrNotExist) || isInvalidProjectID(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	project := manifest.Project
	return &project, nil
}

func (s *Store) FindWorkspaceByProjectID(_ context.Context, id string) (*domain.ProjectWorkspace, error) {
	manifest, err := s.read(id)
	if errors.Is(err, os.ErrNotExist) || isInvalidProjectID(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	workspace := manifest.Workspace
	return &workspace, nil
}

func (s *Store) ReadManifest(id string) (*Manifest, error) {
	manifest, err := s.read(id)
	if err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (s *Store) listManifests() ([]Manifest, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return []Manifest{}, nil
	}
	if err != nil {
		return nil, err
	}
	manifests := make([]Manifest, 0, len(entries))
	for _, entry := range entries {
		if _, err := uuid.Parse(entry.Name()); err != nil {
			continue
		}
		manifest, err := s.read(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read project %s: %w", entry.Name(), err)
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

func (s *Store) read(id string) (Manifest, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Manifest{}, fmt.Errorf("invalid project id %q", id)
	}
	directory := filepath.Join(s.Root, id)
	info, err := os.Lstat(directory)
	if err != nil {
		return Manifest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return Manifest{}, fmt.Errorf("project must be a regular directory")
	}
	path := filepath.Join(directory, "project.json")
	info, err = os.Lstat(path)
	if err != nil {
		return Manifest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Manifest{}, fmt.Errorf("project manifest must be a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode project manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fmt.Errorf("project manifest must contain one JSON value")
	}
	if err := validate(manifest, id); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validate(manifest Manifest, directoryID string) error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported project schemaVersion %d", manifest.SchemaVersion)
	}
	if manifest.Project.ID != directoryID || manifest.Workspace.ProjectID != directoryID {
		return fmt.Errorf("project manifest identity does not match directory")
	}
	if manifest.Project.Name == "" || manifest.Project.CreatedAt.IsZero() || manifest.Project.UpdatedAt.IsZero() {
		return fmt.Errorf("project manifest is incomplete")
	}
	if manifest.Workspace.ID == "" || manifest.Workspace.HostPath == "" || manifest.Workspace.CreatedAt.IsZero() {
		return fmt.Errorf("project workspace is incomplete")
	}
	if !filepath.IsAbs(manifest.Workspace.HostPath) {
		return fmt.Errorf("project workspace hostPath must be absolute")
	}
	return nil
}

func (s *Store) UpdateMCPBindings(_ context.Context, projectID string, mutate func(*[]MCPBinding) error) (*Manifest, error) {
	manifest, err := s.ReadManifest(projectID)
	if err != nil {
		return nil, err
	}
	if err := mutate(&manifest.MCPBindings); err != nil {
		return nil, err
	}
	manifest.Project.UpdatedAt = s.now()
	if err := s.persistManifest(projectID, *manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// Rename updates a project's display name in place. The workspace host path
// is untouched: renaming never moves the user's directory.
func (s *Store) Rename(_ context.Context, projectID, name string) (*domain.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("project name is required")
	}
	manifest, err := s.read(projectID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isInvalidProjectID(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	manifest.Project.Name = name
	manifest.Project.UpdatedAt = s.now()
	if err := s.persistManifest(projectID, manifest); err != nil {
		return nil, err
	}
	project := manifest.Project
	return &project, nil
}

// Delete soft-deletes a project: it marks the manifest status "deleted" so
// List() stops returning it, without touching the workspace host directory or
// the session data already stored under the project. The manifest stays on
// disk, so the deletion is recoverable.
func (s *Store) Delete(_ context.Context, projectID string) (*domain.Project, error) {
	manifest, err := s.read(projectID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isInvalidProjectID(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	manifest.Project.Status = "deleted"
	manifest.Project.UpdatedAt = s.now()
	if err := s.persistManifest(projectID, manifest); err != nil {
		return nil, err
	}
	project := manifest.Project
	return &project, nil
}

// persistManifest atomically rewrites a project's project.json manifest.
func (s *Store) persistManifest(projectID string, manifest Manifest) error {
	path := filepath.Join(s.Root, projectID, "project.json")
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".project.json-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func canonicalHostPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve host path: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("host path does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("host path is not a directory: %s", absolute)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve host path symlinks: %w", err)
	}
	return filepath.Clean(canonical), nil
}

func writeManifest(path string, manifest Manifest) error {
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// isInvalidProjectID reports whether err is the invalid-project-id guard from
// read(); callers treat invalid ids as unknown projects (not found).
func isInvalidProjectID(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "invalid project id ")
}
