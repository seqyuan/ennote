package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/prompts"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupPromptServer builds a server with a real prompts.Service wired in and
// a real (temp) global store.
func setupPromptServer(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	home := t.TempDir()

	globalStore, err := prompts.OpenGlobalStore(home)
	require.NoError(t, err)
	t.Cleanup(func() { globalStore.Close() })

	builtins, err := prompts.LoadBuiltins()
	require.NoError(t, err)

	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))

	trustStore, err := workspace.NewTrustStore(home)
	require.NoError(t, err)

	projectRepo := &store.ProjectRepo{DB: db}

	promptsSvc := &prompts.Service{
		HomeDir:     home,
		Projects:    projectRepo,
		TrustStore:  trustStore,
		Builtins:    builtins,
		GlobalStore: globalStore,
	}

	server := &Server{
		DB: db, Token: "test-token", Sandbox: "none",
		Projects: projectRepo,
		Prompts:  promptsSvc,
	}
	return server, server.Handler(), home
}

func TestPromptTemplates_GlobalCRUD(t *testing.T) {
	_, handler, home := setupPromptServer(t)

	// Create.
	rec := request(t, handler, "POST", "/v1/prompt-templates",
		map[string]string{"name": "hello", "body": "say $1"}, true)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// Create duplicate → 409.
	rec = request(t, handler, "POST", "/v1/prompt-templates",
		map[string]string{"name": "hello", "body": "again"}, true)
	assert.Equal(t, http.StatusConflict, rec.Code)

	// Get.
	rec = request(t, handler, "GET", "/v1/prompt-templates/hello", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var got struct {
		Data struct {
			Name string `json:"name"`
			Body string `json:"body"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "hello", got.Data.Name)

	// Update.
	rec = request(t, handler, "PUT", "/v1/prompt-templates/hello",
		map[string]string{"description": "d", "body": "new body"}, true)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Get shows updated body.
	rec = request(t, handler, "GET", "/v1/prompt-templates/hello", nil, true)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "new body\n", got.Data.Body)

	// Delete → 204.
	rec = request(t, handler, "DELETE", "/v1/prompt-templates/hello", nil, true)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Get after delete → 404.
	rec = request(t, handler, "GET", "/v1/prompt-templates/hello", nil, true)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Management list still works and shows builtin templates.
	rec = request(t, handler, "GET", "/v1/prompt-templates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Data struct {
			Templates       []json.RawMessage `json:"templates"`
			GlobalTemplates []json.RawMessage `json:"globalTemplates"`
			RecoveryMode    bool              `json:"globalRecoveryMode"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.False(t, list.Data.RecoveryMode)
	assert.True(t, len(list.Data.Templates) >= 4, "builtin templates should be listed")
	assert.Len(t, list.Data.GlobalTemplates, 0)

	// The prompts dir is real on disk.
	_, err := os.Stat(filepath.Join(home, "prompts"))
	require.NoError(t, err)
}

func TestPromptTemplates_InvalidName400(t *testing.T) {
	_, handler, _ := setupPromptServer(t)
	rec := request(t, handler, "POST", "/v1/prompt-templates",
		map[string]string{"name": "Bad Name", "body": "x"}, true)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPromptTemplates_ExpandInvalidInvocationDoesNotNeedInfra(t *testing.T) {
	_, handler, _ := setupPromptServer(t)

	// /foo! is a malformed invocation → invalid_invocation (200), even though
	// no project exists (a project-scoped expand would 404 on lookup).
	rec := request(t, handler, "POST", "/v1/projects/nonexistent/prompt-templates/expand",
		map[string]string{"invocation": "/foo!"}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var got struct {
		Data struct {
			Case string `json:"case"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "invalid_invocation", got.Data.Case)
}

func TestPromptTemplates_ExpandUnknownTemplateWithNoProject(t *testing.T) {
	_, handler, _ := setupPromptServer(t)
	// Valid invocation but no project → 404 from project lookup (not a
	// not_found case: invalid fast path only covers malformed syntax).
	rec := request(t, handler, "POST", "/v1/projects/nope/prompt-templates/expand",
		map[string]string{"invocation": "/unknown"}, true)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPromptTemplates_ExpandInvocationTooLarge413(t *testing.T) {
	_, handler, _ := setupPromptServer(t)
	inv := "/" + string(make([]byte, 17*1024))
	rec := request(t, handler, "POST", "/v1/projects/nonexistent/prompt-templates/expand",
		map[string]string{"invocation": inv}, true)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestPromptTemplates_ExpandBodyTooLarge413(t *testing.T) {
	_, handler, _ := setupPromptServer(t)
	// A 40 KiB JSON body exceeds the 20 KiB expand body limit.
	rec := request(t, handler, "POST", "/v1/projects/nonexistent/prompt-templates/expand",
		map[string]string{"invocation": "/foo", "pad": string(make([]byte, 40*1024))}, true)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestPromptTemplates_CreateTooLarge413(t *testing.T) {
	_, handler, _ := setupPromptServer(t)
	rec := request(t, handler, "POST", "/v1/prompt-templates",
		map[string]string{"name": "big", "body": string(make([]byte, 70*1024))}, true)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestPromptTemplates_ListProjectEmpty404(t *testing.T) {
	_, handler, _ := setupPromptServer(t)
	// No such project → 404 for the project-scoped list.
	rec := request(t, handler, "GET", "/v1/projects/nope/prompt-templates", nil, true)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPromptTemplates_ContextCancellation(t *testing.T) {
	_, handler, _ := setupPromptServer(t)
	req := request(t, handler, "GET", "/v1/prompt-templates", nil, true)
	assert.Equal(t, http.StatusOK, req.Code)
}

func TestPromptTemplates_ManagementDiagnosticsPathFree(t *testing.T) {
	_, handler, home := setupPromptServer(t)

	// A broken symlink in the global dir must not crash the management list.
	dir := filepath.Join(home, "prompts")
	require.NoError(t, os.Symlink("/nonexistent/target", filepath.Join(dir, "link.md")))

	rec := request(t, handler, "GET", "/v1/prompt-templates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)

	// No diagnostic message should contain an absolute host path.
	var list struct {
		Data struct {
			Diagnostics []struct {
				Message string `json:"message"`
			} `json:"diagnostics"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	for _, d := range list.Data.Diagnostics {
		assert.NotContains(t, d.Message, "/nonexistent", "diagnostic message leaks host path")
	}
}

func TestPromptTemplates_ExpandBuiltinMatched(t *testing.T) {
	server, handler, _ := setupPromptServer(t)
	ctx := context.Background()

	// Create a real project + workspace (path must exist for canonicalisation).
	wsPath := t.TempDir()
	proj, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "proj", HostPath: wsPath,
	})
	require.NoError(t, err)

	// Untrusted → project list succeeds (200), builtin templates present,
	// and a project_templates_untrusted diagnostic is returned.
	rec := request(t, handler, "GET", "/v1/projects/"+proj.ID+"/prompt-templates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Data struct {
			Templates   []json.RawMessage `json:"templates"`
			Diagnostics []struct {
				Code string `json:"code"`
			} `json:"diagnostics"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.True(t, len(list.Data.Templates) >= 4, "builtins listed even when untrusted")
	assert.True(t, hasDiagCode(list.Data.Diagnostics, "project_templates_untrusted"))
}

func hasDiagCode(diags []struct {
	Code string `json:"code"`
}, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}
