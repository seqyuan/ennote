package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
)

var mcpFilesMu sync.Mutex

type mcpFileDocument struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Profiles      map[string]mcpFileProfile `json:"profiles"`
}

type mcpFileProfile struct {
	Profile  domain.MCPServerProfile          `json:"profile"`
	Versions []domain.MCPServerProfileVersion `json:"versions"`
}

func loadMCPFile(path string) (mcpFileDocument, error) {
	document := mcpFileDocument{SchemaVersion: 1, Profiles: map[string]mcpFileProfile{}}
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return document, nil
	}
	if err != nil {
		return document, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return document, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return document, fmt.Errorf("mcp.json must contain one JSON value")
	}
	if document.SchemaVersion != 1 || document.Profiles == nil {
		return document, fmt.Errorf("unsupported mcp.json schema")
	}
	return document, nil
}

func writeMCPFile(path string, document mcpFileDocument) error {
	contents, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mcp.json-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
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
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (r *MCPProfileRepo) fileCreateProfile(input CreateMCPProfileInput) (*domain.MCPServerProfile, error) {
	mcpFilesMu.Lock()
	defer mcpFilesMu.Unlock()
	document, err := loadMCPFile(r.FilePath)
	if err != nil {
		return nil, err
	}
	for _, record := range document.Profiles {
		if record.Profile.Slug == input.Slug && record.Profile.Lifecycle == "active" {
			return nil, fmt.Errorf("MCP profile slug already exists")
		}
	}
	now := time.Now().UTC()
	profile := domain.MCPServerProfile{ID: uuid.NewString(), DisplayName: input.DisplayName, Slug: input.Slug,
		SourceKind: input.SourceKind, ProjectScope: input.ProjectScope, SourceLocator: input.SourceLocator,
		Lifecycle: "active", CreatedAt: now, UpdatedAt: now}
	document.Profiles[profile.ID] = mcpFileProfile{Profile: profile, Versions: []domain.MCPServerProfileVersion{}}
	if err := writeMCPFile(r.FilePath, document); err != nil {
		return nil, err
	}
	return &profile, nil
}

func (r *MCPProfileRepo) fileCreateVersion(profileID string, version *domain.MCPServerProfileVersion) error {
	mcpFilesMu.Lock()
	defer mcpFilesMu.Unlock()
	document, err := loadMCPFile(r.FilePath)
	if err != nil {
		return err
	}
	record, ok := document.Profiles[profileID]
	if !ok || record.Profile.Lifecycle != "active" {
		return sql.ErrNoRows
	}
	copy := *version
	copy.ProfileID, copy.Version = profileID, len(record.Versions)+1
	if copy.ID == "" {
		copy.ID = fmt.Sprintf("%s@v%06d", profileID, copy.Version)
	}
	if copy.CreatedAt.IsZero() {
		copy.CreatedAt = time.Now().UTC()
	}
	if copy.TimeoutMS <= 0 {
		copy.TimeoutMS = 15000
	}
	if copy.NetworkPolicy == "" {
		copy.NetworkPolicy = "default"
	}
	if copy.ConfigDigest == "" {
		copy.ConfigDigest = mcpConfigDigest(&copy)
	}
	record.Versions = append(record.Versions, copy)
	record.Profile.LatestVersion, record.Profile.UpdatedAt = copy.Version, time.Now().UTC()
	document.Profiles[profileID] = record
	if err := writeMCPFile(r.FilePath, document); err != nil {
		return err
	}
	*version = copy
	return nil
}

func (r *MCPProfileRepo) fileListProfiles() ([]*domain.MCPServerProfile, error) {
	mcpFilesMu.Lock()
	defer mcpFilesMu.Unlock()
	document, err := loadMCPFile(r.FilePath)
	if err != nil {
		return nil, err
	}
	profiles := make([]*domain.MCPServerProfile, 0, len(document.Profiles))
	for _, record := range document.Profiles {
		if record.Profile.Lifecycle == "active" {
			copy := record.Profile
			profiles = append(profiles, &copy)
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].DisplayName < profiles[j].DisplayName })
	return profiles, nil
}

func (r *MCPProfileRepo) fileGetProfile(id string) (*domain.MCPServerProfile, error) {
	document, err := loadMCPFile(r.FilePath)
	if err != nil {
		return nil, err
	}
	record, ok := document.Profiles[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	copy := record.Profile
	return &copy, nil
}

func (r *MCPProfileRepo) fileListVersions(profileID string) ([]*domain.MCPServerProfileVersion, error) {
	document, err := loadMCPFile(r.FilePath)
	if err != nil {
		return nil, err
	}
	record, ok := document.Profiles[profileID]
	if !ok {
		return []*domain.MCPServerProfileVersion{}, nil
	}
	versions := make([]*domain.MCPServerProfileVersion, len(record.Versions))
	for index := range record.Versions {
		copy := record.Versions[index]
		versions[index] = &copy
	}
	return versions, nil
}

func (r *MCPProfileRepo) fileGetVersion(id string) (*domain.MCPServerProfileVersion, error) {
	document, err := loadMCPFile(r.FilePath)
	if err != nil {
		return nil, err
	}
	for _, record := range document.Profiles {
		for index := range record.Versions {
			if record.Versions[index].ID == id {
				copy := record.Versions[index]
				return &copy, nil
			}
		}
	}
	return nil, sql.ErrNoRows
}

func (r *MCPProfileRepo) fileArchive(id string) error {
	mcpFilesMu.Lock()
	defer mcpFilesMu.Unlock()
	document, err := loadMCPFile(r.FilePath)
	if err != nil {
		return err
	}
	record, ok := document.Profiles[id]
	if !ok {
		return sql.ErrNoRows
	}
	record.Profile.Lifecycle, record.Profile.UpdatedAt = "archived", time.Now().UTC()
	document.Profiles[id] = record
	return writeMCPFile(r.FilePath, document)
}

func projectBinding(projectID string, value projectstore.MCPBinding) *domain.MCPProjectBinding {
	return &domain.MCPProjectBinding{ID: value.ID, ProjectID: projectID, ProfileVersionID: value.ProfileVersionID,
		DesiredEnabled: value.DesiredEnabled, Required: value.Required,
		SelectedRemoteToolNames: append([]string{}, value.SelectedRemoteToolNames...), CredentialRefs: value.CredentialRefs,
		Revision: value.Revision, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func (r *MCPBindingRepo) fileList(ctx context.Context, projectID string) ([]*domain.MCPProjectBinding, error) {
	manifest, err := r.Projects.ReadManifest(projectID)
	if err != nil {
		return nil, err
	}
	result := make([]*domain.MCPProjectBinding, len(manifest.MCPBindings))
	for index := range manifest.MCPBindings {
		result[index] = projectBinding(projectID, manifest.MCPBindings[index])
	}
	return result, nil
}

func (r *MCPBindingRepo) fileEnsure(ctx context.Context, projectID, versionID string) (*domain.MCPProjectBinding, error) {
	mcpFilesMu.Lock()
	defer mcpFilesMu.Unlock()
	var created *domain.MCPProjectBinding
	_, err := r.Projects.UpdateMCPBindings(ctx, projectID, func(bindings *[]projectstore.MCPBinding) error {
		for _, binding := range *bindings {
			if binding.ProfileVersionID == versionID {
				created = projectBinding(projectID, binding)
				return nil
			}
		}
		now := time.Now().UTC()
		value := projectstore.MCPBinding{ID: uuid.NewString(), ProfileVersionID: versionID, Required: true,
			SelectedRemoteToolNames: []string{}, CredentialRefs: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now}
		*bindings = append(*bindings, value)
		created = projectBinding(projectID, value)
		return nil
	})
	return created, err
}

func (r *MCPBindingRepo) fileGet(ctx context.Context, bindingID string) (*domain.MCPProjectBinding, error) {
	projects, err := r.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		bindings, err := r.fileList(ctx, project.ID)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			if binding.ID == bindingID {
				return binding, nil
			}
		}
	}
	return nil, sql.ErrNoRows
}

func (r *MCPBindingRepo) fileUpdate(ctx context.Context, bindingID string, update MCPBindingUpdate) (*domain.MCPProjectBinding, error) {
	current, err := r.fileGet(ctx, bindingID)
	if err != nil {
		return nil, err
	}
	if update.CredentialRefs != nil {
		if err := validateCredentialRefMap(update.CredentialRefs); err != nil {
			return nil, err
		}
	}
	mcpFilesMu.Lock()
	defer mcpFilesMu.Unlock()
	var updated *domain.MCPProjectBinding
	_, err = r.Projects.UpdateMCPBindings(ctx, current.ProjectID, func(bindings *[]projectstore.MCPBinding) error {
		for index := range *bindings {
			value := &(*bindings)[index]
			if value.ID != bindingID {
				continue
			}
			if update.DesiredEnabled != nil {
				value.DesiredEnabled = *update.DesiredEnabled
			}
			if update.Required != nil {
				value.Required = *update.Required
			}
			if update.SelectedRemoteToolNames != nil {
				value.SelectedRemoteToolNames = append([]string{}, update.SelectedRemoteToolNames...)
			}
			if update.CredentialRefs != nil {
				value.CredentialRefs = update.CredentialRefs
			}
			value.Revision++
			value.UpdatedAt = time.Now().UTC()
			updated = projectBinding(current.ProjectID, *value)
			return nil
		}
		return sql.ErrNoRows
	})
	return updated, err
}

func (r *MCPBindingRepo) fileDelete(ctx context.Context, bindingID string) error {
	current, err := r.fileGet(ctx, bindingID)
	if err != nil {
		return err
	}
	mcpFilesMu.Lock()
	defer mcpFilesMu.Unlock()
	_, err = r.Projects.UpdateMCPBindings(ctx, current.ProjectID, func(bindings *[]projectstore.MCPBinding) error {
		for index := range *bindings {
			if (*bindings)[index].ID == bindingID {
				*bindings = append((*bindings)[:index], (*bindings)[index+1:]...)
				return nil
			}
		}
		return sql.ErrNoRows
	})
	return err
}
