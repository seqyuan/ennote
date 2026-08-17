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
	_, handler := setupServer(t, nil)
	root := t.TempDir()
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
