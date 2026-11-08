package api

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostDirectoryBrowserListsAndCreatesDirectories(t *testing.T) {
	server, handler := setupServer(t, nil)
	root := server.HostHome
	require.NoError(t, os.Mkdir(filepath.Join(root, "beta"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "Alpha"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "not-a-directory.txt"), []byte("x"), 0o644))

	listed := request(t, handler, http.MethodGet, "/v1/host/directories?path="+url.QueryEscape(root), nil, true)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var listing hostDirectoryListing
	decodeData(t, listed, &listing)
	assert.Equal(t, filepath.Clean(root), listing.Path)
	require.Len(t, listing.Entries, 3)
	assert.Equal(t, []string{".hidden", "Alpha", "beta"}, []string{listing.Entries[0].Name, listing.Entries[1].Name, listing.Entries[2].Name})
	assert.True(t, listing.Entries[0].Hidden)
	assert.False(t, listing.Truncated)

	created := request(t, handler, http.MethodPost, "/v1/host/directories", map[string]any{"path": root, "name": "fresh"}, true)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	info, err := os.Stat(filepath.Join(root, "fresh"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	duplicate := request(t, handler, http.MethodPost, "/v1/host/directories", map[string]any{"path": root, "name": "fresh"}, true)
	assert.Equal(t, http.StatusConflict, duplicate.Code)
	invalid := request(t, handler, http.MethodPost, "/v1/host/directories", map[string]any{"path": root, "name": "../escape"}, true)
	assert.Equal(t, http.StatusBadRequest, invalid.Code)
}

func TestHostDirectoryBrowserRejectsRelativePathsAndRequiresAuth(t *testing.T) {
	_, handler := setupServer(t, nil)
	relative := request(t, handler, http.MethodGet, "/v1/host/directories?path=relative", nil, true)
	assert.Equal(t, http.StatusBadRequest, relative.Code)
	unauthorized := request(t, handler, http.MethodGet, "/v1/host/directories", nil, false)
	assert.Equal(t, http.StatusUnauthorized, unauthorized.Code)
}

func TestHostDirectoryBrowserRejectsPathsOutsideHome(t *testing.T) {
	_, handler := setupServer(t, nil)
	outside := request(t, handler, http.MethodGet, "/v1/host/directories?path="+url.QueryEscape(t.TempDir()), nil, true)
	assert.Equal(t, http.StatusForbidden, outside.Code)
	createOutside := request(t, handler, http.MethodPost, "/v1/host/directories", map[string]any{"path": t.TempDir(), "name": "x"}, true)
	assert.Equal(t, http.StatusForbidden, createOutside.Code)
}

func TestHostDirectoryBrowserIgnoresSymlinksThatEscapeHome(t *testing.T) {
	server, handler := setupServer(t, nil)
	escape := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(escape, "secret"), 0o755))
	link := filepath.Join(server.HostHome, "escape")
	require.NoError(t, os.Symlink(escape, link))
	listed := request(t, handler, http.MethodGet, "/v1/host/directories?path="+url.QueryEscape(server.HostHome), nil, true)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var listing hostDirectoryListing
	decodeData(t, listed, &listing)
	for _, entry := range listing.Entries {
		assert.NotEqual(t, "escape", entry.Name)
	}
	direct := request(t, handler, http.MethodGet, "/v1/host/directories?path="+url.QueryEscape(link), nil, true)
	assert.Equal(t, http.StatusForbidden, direct.Code)
}

func TestHostDirectoryBrowserAllowsConfiguredExtraRoots(t *testing.T) {
	server, handler := setupServer(t, nil)
	// The fixture's home root is a temp directory; a second, unrelated root stands
	// in for something like /Volumes/data, which is what ENNOTE_HOST_ROOTS is for.
	extra := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(extra, "shared"), 0o755))
	server.HostRoots = []string{extra}

	listed := request(t, handler, http.MethodGet, "/v1/host/directories?path="+url.QueryEscape(extra), nil, true)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var listing hostDirectoryListing
	decodeData(t, listed, &listing)
	assert.Equal(t, filepath.Clean(extra), listing.Path)
	assert.Equal(t, server.HostHome, listing.Home)
	assert.Equal(t, []string{server.HostHome, filepath.Clean(extra)}, listing.Roots)
	require.Len(t, listing.Entries, 1)
	assert.Equal(t, "shared", listing.Entries[0].Name)
	// Crumbs stop at the extra root: nothing above it is browsable, so nothing
	// above it is offered.
	assert.Equal(t, []string{filepath.Clean(extra)}, crumbPaths(listing))

	created := request(t, handler, http.MethodPost, "/v1/host/directories",
		map[string]any{"path": extra, "name": "fresh"}, true)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	info, err := os.Stat(filepath.Join(extra, "fresh"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// The primary root keeps working and now reports both roots, so a client can
	// offer the configured root as a jump instead of demanding a typed path.
	home := request(t, handler, http.MethodGet, "/v1/host/directories", nil, true)
	require.Equal(t, http.StatusOK, home.Code, home.Body.String())
	decodeData(t, home, &listing)
	assert.Equal(t, server.HostHome, listing.Path)
	assert.Len(t, listing.Roots, 2)
}

func TestHostDirectoryBrowserStillRejectsPathsOutsideEveryRoot(t *testing.T) {
	server, handler := setupServer(t, nil)
	server.HostRoots = []string{t.TempDir()}
	outside := request(t, handler, http.MethodGet, "/v1/host/directories?path="+url.QueryEscape(t.TempDir()), nil, true)
	assert.Equal(t, http.StatusForbidden, outside.Code)
	assert.Contains(t, outside.Body.String(), "host_directory_forbidden")
}

func TestHostDirectoryRootsDropNestedEntries(t *testing.T) {
	server, handler := setupServer(t, nil)
	nested := hostWorkspace(t, server, "nested")
	// A root inside Home adds nothing: Home already covers it, and keeping it
	// would give a path two possible crumb stops.
	server.HostRoots = []string{nested, "", "  "}

	listed := request(t, handler, http.MethodGet, "/v1/host/directories", nil, true)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var listing hostDirectoryListing
	decodeData(t, listed, &listing)
	assert.Equal(t, []string{server.HostHome}, listing.Roots)
}

func TestCreateProjectRefusesHostPathsOutsideEveryRoot(t *testing.T) {
	server, handler := setupServer(t, nil)
	outside := t.TempDir()
	refused := request(t, handler, http.MethodPost, "/v1/projects",
		map[string]any{"name": "outside", "hostPath": outside}, true)
	require.Equal(t, http.StatusForbidden, refused.Code, refused.Body.String())
	assert.Contains(t, refused.Body.String(), "host_directory_forbidden")

	// Configuring that directory as a root is the supported way to allow it.
	server.HostRoots = []string{outside}
	accepted := request(t, handler, http.MethodPost, "/v1/projects",
		map[string]any{"name": "outside", "hostPath": outside}, true)
	require.Equal(t, http.StatusCreated, accepted.Code, accepted.Body.String())

	relative := request(t, handler, http.MethodPost, "/v1/projects",
		map[string]any{"name": "relative", "hostPath": "some/dir"}, true)
	assert.Equal(t, http.StatusBadRequest, relative.Code)
	assert.Contains(t, relative.Body.String(), "invalid_host_directory")
}

func crumbPaths(listing hostDirectoryListing) []string {
	paths := make([]string, 0, len(listing.Crumbs))
	for _, crumb := range listing.Crumbs {
		paths = append(paths, crumb.Path)
	}
	return paths
}
