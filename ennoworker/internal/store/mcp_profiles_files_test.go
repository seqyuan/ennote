package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileMCPProfilesAndProjectBindings(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	profiles := &store.MCPProfileRepo{FilePath: filepath.Join(home, "config", "mcp.json")}
	profile, err := profiles.CreateProfile(ctx, store.CreateMCPProfileInput{
		DisplayName: "Local tools", Slug: "local-tools", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStdio, Executable: "tool-server", Argv: []string{"serve"}}
	require.NoError(t, profiles.CreateVersion(ctx, profile.ID, version))
	assert.Equal(t, 1, version.Version)
	assert.Contains(t, version.ID, "@v000001")
	info, err := os.Stat(filepath.Join(home, "config", "mcp.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	bindings := &store.MCPBindingRepo{Projects: projects}
	binding, err := bindings.EnsureBindingExists(ctx, project.ID, version.ID)
	require.NoError(t, err)
	enabled := true
	updated, err := bindings.Update(ctx, binding.ID, store.MCPBindingUpdate{
		DesiredEnabled: &enabled, SelectedRemoteToolNames: []string{"search"},
		CredentialRefs: map[string]string{"TOKEN": "env:MCP_TOKEN"},
	})
	require.NoError(t, err)
	assert.True(t, updated.DesiredEnabled)
	assert.Equal(t, 2, updated.Revision)

	reloaded, err := bindings.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, reloaded, 1)
	assert.Equal(t, []string{"search"}, reloaded[0].SelectedRemoteToolNames)
	manifest, err := projects.ReadManifest(project.ID)
	require.NoError(t, err)
	assert.Equal(t, version.ID, manifest.MCPBindings[0].ProfileVersionID)
}
