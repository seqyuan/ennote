package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The write endpoints are the only place the Worker changes user files outside a
// tool call, so what matters in these tests is what they refuse.

func writeFixture(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	server, handler := setupServer(t, &fakeController{})
	workspaceHost := hostWorkspace(t, server, "writes")
	created := request(t, handler, http.MethodPost, "/v1/projects",
		map[string]any{"name": "W", "hostPath": workspaceHost}, true)
	var project struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	decodeData(t, created, &project)
	return server, handler, project.Project.ID
}

func createEntry(t *testing.T, handler http.Handler, projectID, path, kind, content string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"path": path, "kind": kind}
	if content != "" {
		body["content"] = content
	}
	return request(t, handler, http.MethodPost,
		"/v1/projects/"+projectID+"/files/create", body, true)
}

func TestCreateProjectFileEntryRefusesToOverwrite(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	first := createEntry(t, handler, projectID, "/workspace/report.tsv", "file", "a\tb\n")
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	second := createEntry(t, handler, projectID, "/workspace/report.tsv", "file", "replaced\n")
	assert.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "workspace_path_exists")

	// The refusal is real: the file still holds what the first call wrote.
	content := request(t, handler, http.MethodGet,
		"/v1/projects/"+projectID+"/files/content?path=/workspace/report.tsv", nil, true)
	assert.Contains(t, content.Body.String(), "a\tb")
}

func TestCreateProjectFileEntryNeedsAnExistingParent(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	answer := createEntry(t, handler, projectID, "/workspace/nope/deep/file.txt", "file", "")
	assert.Equal(t, http.StatusNotFound, answer.Code)
	assert.Contains(t, answer.Body.String(), "parent_not_found")
}

func TestCreateProjectFileEntryWritesContentAndDirectories(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	created := createEntry(t, handler, projectID, "/workspace/analysis", "directory", "")
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	file := createEntry(t, handler, projectID, "/workspace/analysis/run.py", "file", "print(1)\n")
	require.Equal(t, http.StatusCreated, file.Code, file.Body.String())
	assert.Contains(t, file.Body.String(), `"name":"run.py"`)
	assert.Contains(t, file.Body.String(), `"isDir":false`)

	listed := request(t, handler, http.MethodGet,
		"/v1/projects/"+projectID+"/files?path=/workspace/analysis", nil, true)
	assert.Contains(t, listed.Body.String(), "run.py")
}

func TestCreateProjectFileEntryRejectsAnUnknownKind(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	answer := createEntry(t, handler, projectID, "/workspace/thing", "socket", "")
	assert.Equal(t, http.StatusBadRequest, answer.Code)
	assert.Contains(t, answer.Body.String(), "invalid_entry_kind")
}

func TestMoveProjectFileEntryRenamesWithoutReplacing(t *testing.T) {
	_, handler, projectID := writeFixture(t)
	require.Equal(t, http.StatusCreated, createEntry(t, handler, projectID, "/workspace/a.txt", "file", "one").Code)
	require.Equal(t, http.StatusCreated, createEntry(t, handler, projectID, "/workspace/b.txt", "file", "two").Code)

	clash := request(t, handler, http.MethodPost, "/v1/projects/"+projectID+"/files/move",
		map[string]any{"from": "/workspace/a.txt", "to": "/workspace/b.txt"}, true)
	assert.Equal(t, http.StatusConflict, clash.Code)

	moved := request(t, handler, http.MethodPost, "/v1/projects/"+projectID+"/files/move",
		map[string]any{"from": "/workspace/a.txt", "to": "/workspace/renamed.txt"}, true)
	require.Equal(t, http.StatusOK, moved.Code, moved.Body.String())
	assert.Contains(t, moved.Body.String(), "renamed.txt")

	gone := request(t, handler, http.MethodGet,
		"/v1/projects/"+projectID+"/files/content?path=/workspace/a.txt", nil, true)
	assert.Equal(t, http.StatusNotFound, gone.Code)
}

func TestMoveProjectFileEntryRefusesToNestADirectoryInsideItself(t *testing.T) {
	_, handler, projectID := writeFixture(t)
	require.Equal(t, http.StatusCreated, createEntry(t, handler, projectID, "/workspace/inner", "directory", "").Code)

	answer := request(t, handler, http.MethodPost, "/v1/projects/"+projectID+"/files/move",
		map[string]any{"from": "/workspace/inner", "to": "/workspace/inner/inner"}, true)
	assert.Equal(t, http.StatusConflict, answer.Code)
	assert.Contains(t, answer.Body.String(), "invalid_move_target")
}

func TestDeleteProjectFileEntryNeedsRecursionForANonEmptyDirectory(t *testing.T) {
	_, handler, projectID := writeFixture(t)
	require.Equal(t, http.StatusCreated, createEntry(t, handler, projectID, "/workspace/dir", "directory", "").Code)
	require.Equal(t, http.StatusCreated, createEntry(t, handler, projectID, "/workspace/dir/file.txt", "file", "x").Code)

	refused := request(t, handler, http.MethodDelete,
		"/v1/projects/"+projectID+"/files?path=/workspace/dir", nil, true)
	assert.Equal(t, http.StatusConflict, refused.Code)
	assert.Contains(t, refused.Body.String(), "directory_not_empty")

	empty := request(t, handler, http.MethodDelete,
		"/v1/projects/"+projectID+"/files?path=/workspace/dir/file.txt", nil, true)
	require.Equal(t, http.StatusNoContent, empty.Code)

	emptied := request(t, handler, http.MethodDelete,
		"/v1/projects/"+projectID+"/files?path=/workspace/dir", nil, true)
	require.Equal(t, http.StatusNoContent, emptied.Code)

	recursive := request(t, handler, http.MethodDelete,
		"/v1/projects/"+projectID+"/files?path=/workspace&recursive=true", nil, true)
	assert.Equal(t, http.StatusBadRequest, recursive.Code, "the workspace root is never a delete target")
	assert.Contains(t, recursive.Body.String(), "workspace_root_immutable")
}

func TestWorkspaceRootIsProtectedHoweverItIsSpelled(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	// Every spelling of the root is refused. Two of them (`//workspace`, empty) are
	// refused earlier as "not a /workspace path" — a different message for the same
	// outcome, which is what matters here.
	for _, spelling := range []string{"/workspace", "/workspace/", "/workspace/.", "//workspace", "/workspace/..//workspace", ""} {
		deleted := request(t, handler, http.MethodDelete,
			"/v1/projects/"+projectID+"/files?path="+url.QueryEscape(spelling)+"&recursive=true", nil, true)
		require.Equal(t, http.StatusBadRequest, deleted.Code, "delete %q: %s", spelling, deleted.Body.String())
		body := deleted.Body.String()
		assert.True(t,
			strings.Contains(body, "workspace_root_immutable") || strings.Contains(body, "invalid_workspace_path"),
			"delete %q: %s", spelling, body)
	}
	// The root is still there: nothing above may have deleted it.
	root := request(t, handler, http.MethodGet, "/v1/projects/"+projectID+"/workspace", nil, true)
	require.Equal(t, http.StatusOK, root.Code)
}

func TestWorkspaceWritesRefuseAPathOutsideTheMount(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	for _, spelling := range []string{"relative.txt", "../outside.txt", "/etc/passwd", "/workspace/../outside.txt"} {
		answer := createEntry(t, handler, projectID, spelling, "file", "")
		assert.Equal(t, http.StatusBadRequest, answer.Code, "create %q: %s", spelling, answer.Body.String())
	}
}

func TestMoveAndDeleteActOnASymlinkItself(t *testing.T) {
	_, handler, projectID := writeFixture(t)
	require.Equal(t, http.StatusCreated, createEntry(t, handler, projectID, "/workspace/real.txt", "file", "keep").Code)

	record := request(t, handler, http.MethodGet, "/v1/projects/"+projectID+"/workspace", nil, true)
	var workspaceRecord struct {
		HostPath string `json:"hostPath"`
	}
	decodeData(t, record, &workspaceRecord)
	linkPath := filepath.Join(workspaceRecord.HostPath, "link.txt")
	require.NoError(t, os.Symlink(filepath.Join(workspaceRecord.HostPath, "real.txt"), linkPath))

	// Deleting the link deletes the link, not the file it names.
	removed := request(t, handler, http.MethodDelete,
		"/v1/projects/"+projectID+"/files?path=/workspace/link.txt", nil, true)
	require.Equal(t, http.StatusNoContent, removed.Code, removed.Body.String())
	_, err := os.Lstat(linkPath)
	assert.True(t, os.IsNotExist(err), "the link is gone")
	kept := request(t, handler, http.MethodGet,
		"/v1/projects/"+projectID+"/files/content?path=/workspace/real.txt", nil, true)
	assert.Contains(t, kept.Body.String(), "keep", "the file the link pointed at is untouched")
}

func TestWorkspaceWritesStayInsideTheWorkspaceMount(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	escape := createEntry(t, handler, projectID, "/workspace/../outside.txt", "file", "")
	assert.Equal(t, http.StatusBadRequest, escape.Code)

	// A symlink pointing out of the jail is the interesting case: the path looks
	// like a workspace path and resolves outside it.
	record := request(t, handler, http.MethodGet, "/v1/projects/"+projectID+"/workspace", nil, true)
	var workspaceRecord struct {
		HostPath string `json:"hostPath"`
	}
	decodeData(t, record, &workspaceRecord)
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(workspaceRecord.HostPath, "link")))

	viaLink := createEntry(t, handler, projectID, "/workspace/link/escaped.txt", "file", "")
	assert.Equal(t, http.StatusBadRequest, viaLink.Code, viaLink.Body.String())
	_, statErr := os.Stat(filepath.Join(outside, "escaped.txt"))
	assert.True(t, os.IsNotExist(statErr), "nothing may be written outside the jail")
}

func TestWorkspaceWritesRefuseAReadOnlyMount(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	// /skills is a read-only mount on every workspace.
	answer := createEntry(t, handler, projectID, "/skills/anything.md", "file", "")
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusForbidden}, answer.Code, answer.Body.String())
	// Either refusal is correct: a path outside /workspace and a read-only mount
	// are both "you may not write this".
	body := answer.Body.String()
	assert.True(t,
		strings.Contains(body, "workspace_read_only") || strings.Contains(body, "workspace_path_escape") || strings.Contains(body, "invalid_workspace_path"),
		body)
}

func TestCreateProjectFileEntryRejectsAnOversizedBody(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	huge := strings.Repeat("x", int(maxWorkspacePreviewBytes)+1)
	answer := request(t, handler, http.MethodPost, "/v1/projects/"+projectID+"/files/create",
		map[string]any{"path": "/workspace/big.txt", "kind": "file", "content": huge}, true)
	assert.Equal(t, http.StatusRequestEntityTooLarge, answer.Code, answer.Body.String())
}

func TestCreateProjectFileEntryRejectsAMalformedBody(t *testing.T) {
	_, handler, projectID := writeFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/projects/"+projectID+"/files/create", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "invalid_request_body")
}
