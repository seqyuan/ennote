package api

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/mcpclient"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupFileMCP wires a file-backed MCPServer over an isolated Home: profiles in
// config/mcp.json, bindings in project.json, catalog cache under cache/mcp, and
// Run snapshots in the owning Session database.
func setupFileMCP(t *testing.T) (*MCPServer, *store.MCPProfileRepo, *store.MCPBindingRepo,
	*sessionstore.Manager, *domain.Project, *domain.Session, *sql.DB) {
	t.Helper()
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(context.Background(),
		domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { _ = sessions.Close() })
	session, err := sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)
	db, err := sessions.OpenSession(context.Background(), session.ID)
	require.NoError(t, err)
	profileRepo := &store.MCPProfileRepo{FilePath: filepath.Join(home, "config", "mcp.json")}
	bindingRepo := &store.MCPBindingRepo{Projects: projects}
	catalogRepo := &store.MCPCatalogRepo{CacheDir: filepath.Join(home, "cache", "mcp")}
	mcp := &MCPServer{
		Profiles: profileRepo, Bindings: bindingRepo, Catalogs: catalogRepo,
		Runs:    &store.MCPRunRepo{DB: db},
		Bundled: mcpclient.NewBundledRegistry(),
	}
	return mcp, profileRepo, bindingRepo, sessions, project, session, db
}

func TestFileMCPProfileVersionBindingAPI(t *testing.T) {
	mcp, profileRepo, bindingRepo, _, project, _, _ := setupFileMCP(t)
	_ = mcp
	ctx := context.Background()

	profile, err := profileRepo.CreateProfile(ctx, store.CreateMCPProfileInput{
		DisplayName: "Local Tools", Slug: "local-tools", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{
		Transport: domain.MCPTransportStdio, Executable: "tool-server", Argv: []string{"serve"},
	}
	require.NoError(t, profileRepo.CreateVersion(ctx, profile.ID, version))
	assert.Equal(t, profile.ID+"@v000001", version.ID)

	enabled := true
	binding, err := bindingRepo.EnsureBindingExists(ctx, project.ID, version.ID)
	require.NoError(t, err)
	updated, err := bindingRepo.Update(ctx, binding.ID, store.MCPBindingUpdate{
		DesiredEnabled: &enabled, SelectedRemoteToolNames: []string{"search"},
	})
	require.NoError(t, err)
	assert.True(t, updated.DesiredEnabled)

	// File mode is 0600 and the profile file is the authority.
	info, err := os.Stat(profileRepo.FilePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Reopen from files: list is stable (no SQL authority).
	reopened := &store.MCPProfileRepo{FilePath: profileRepo.FilePath}
	profiles, err := reopened.ListProfiles(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.Equal(t, profile.ID, profiles[0].ID)
	assert.Equal(t, 1, profiles[0].LatestVersion)
	versions, err := reopened.ListVersions(ctx, profile.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Equal(t, version.ID, versions[0].ID)

	reopenedBindings := &store.MCPBindingRepo{Projects: bindingRepo.Projects}
	reloaded, err := reopenedBindings.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, reloaded, 1)
	assert.Equal(t, []string{"search"}, reloaded[0].SelectedRemoteToolNames)
}

func TestFileMCPFreezeRunUsesProjectBinding(t *testing.T) {
	mcp, profileRepo, bindingRepo, _, project, session, _ := setupFileMCP(t)
	ctx := context.Background()
	profile, err := profileRepo.CreateProfile(ctx, store.CreateMCPProfileInput{
		DisplayName: "Bio", Slug: "bio", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{
		Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp",
	}
	require.NoError(t, profileRepo.CreateVersion(ctx, profile.ID, version))
	enabled := true
	binding, err := bindingRepo.EnsureBindingExists(ctx, project.ID, version.ID)
	require.NoError(t, err)
	_, err = bindingRepo.Update(ctx, binding.ID, store.MCPBindingUpdate{
		DesiredEnabled: &enabled, SelectedRemoteToolNames: []string{"search"},
		CredentialRefs: map[string]string{"TOKEN": "env:MCP_TOKEN"},
	})
	require.NoError(t, err)

	mcp.DiscoverFn = func(_ context.Context, b *domain.MCPProjectBinding,
		v *domain.MCPServerProfileVersion) ([]domain.MCPCatalogEntry, error) {
		return []domain.MCPCatalogEntry{{RemoteName: "search", ExposedName: "bio__search",
			InputSchema: []byte(`{"type":"object"}`), Digest: "d1"}}, nil
	}
	servers, err := mcp.FreezeRun(ctx, "run-file-mcp", project.ID)
	require.NoError(t, err)
	require.Len(t, servers, 1)
	assert.True(t, servers[0].Snapshot.Required)
	var toolNames []string
	for _, tool := range servers[0].Tools {
		toolNames = append(toolNames, tool.ExposedName)
	}
	assert.Equal(t, []string{"bio__search"}, toolNames)

	// Freeze again for the same Session run: idempotent, no duplicate rows.
	servers2, err := mcp.FreezeRun(ctx, "run-file-mcp", project.ID)
	require.NoError(t, err)
	require.Len(t, servers2, 1)
	_ = session
}

// TestFileMCPHTTPRoutes exercises the HTTP surface with file-backed repos so
// the API contract (create profile/version/binding) is validated without the
// legacy global SQL authority.
func TestFileMCPHTTPRoutes(t *testing.T) {
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(context.Background(),
		domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	server := &Server{
		Token: "test-token",
		MCP: &MCPServer{
			Profiles: &store.MCPProfileRepo{FilePath: filepath.Join(home, "config", "mcp.json")},
			Bindings: &store.MCPBindingRepo{Projects: projects},
			Catalogs: &store.MCPCatalogRepo{CacheDir: filepath.Join(home, "cache", "mcp")},
			Runs:     &store.MCPRunRepo{},
			Bundled:  mcpclient.NewBundledRegistry(),
		},
	}
	handler := server.Handler()

	rec := request(t, handler, http.MethodPost, "/v1/mcp/server-profiles", map[string]any{
		"displayName": "Pubmed", "slug": "pubmed", "sourceKind": "managed",
	}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var profile domain.MCPServerProfile
	decodeData(t, rec, &profile)

	rec = request(t, handler, http.MethodPost, "/v1/mcp/server-profiles/"+profile.ID+"/versions", map[string]any{
		"transport": "stdio", "executable": "/bin/echo", "argv": []string{"-n", "hi"},
		"timeoutMs": 5000, "networkPolicy": "default",
	}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version domain.MCPServerProfileVersion
	decodeData(t, rec, &version)
	assert.Equal(t, 1, version.Version)

	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/mcp/bindings", map[string]any{
		"profileVersionId": version.ID, "desiredEnabled": true, "required": true,
		"selectedRemoteToolNames": []string{}, "credentialRefs": map[string]string{},
	}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var binding domain.MCPProjectBinding
	decodeData(t, rec, &binding)
	assert.Equal(t, project.ID, binding.ProjectID)
	assert.Equal(t, version.ID, binding.ProfileVersionID)
}
