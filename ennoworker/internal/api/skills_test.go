package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupSkillsServer(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))

	home := t.TempDir()
	userRoot := filepath.Join(home, ".pi", "agent", "skills")
	require.NoError(t, os.MkdirAll(filepath.Join(userRoot, "web-search"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userRoot, "web-search", "SKILL.md"),
		[]byte("---\nname: web-search\ndescription: Search the web.\n---\n\nbody\n"), 0o644))

	trustStore, err := workspace.NewTrustStore(filepath.Join(home, "ennote"))
	require.NoError(t, err)

	server := &Server{
		DB: db, Token: "test-token", Sandbox: "none",
		Projects: &store.ProjectRepo{DB: db},
		Skills:   &skillsmgmt.Service{UserRoot: userRoot, BuiltinRoot: "", HomeDir: home},
		Trust:    trustStore,
	}
	return server, server.Handler(), home
}

func TestSkillsListRequiresAuthentication(t *testing.T) {
	_, handler, _ := setupSkillsServer(t)
	response := request(t, handler, http.MethodGet, "/v1/skills", nil, false)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestSkillsListReturnsAnnotatedSkills(t *testing.T) {
	_, handler, home := setupSkillsServer(t)
	// Global lock marks web-search as installed.
	lockDir := filepath.Join(home, ".agents")
	require.NoError(t, os.MkdirAll(lockDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lockDir, ".skill-lock.json"), []byte(`{
	  "skills": {
	    "web-search": {
	      "source": "acme/skills", "sourceType": "github",
	      "skillPath": "web-search/SKILL.md", "skillFolderHash": "abc123"
	    }
	  }
	}`), 0o644))

	response := request(t, handler, http.MethodGet, "/v1/skills", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result struct {
		Skills []struct {
			SkillID string                   `json:"skillId"`
			Name    string                   `json:"name"`
			Install *skillsmgmt.InstallInfo  `json:"install"`
		} `json:"skills"`
	}
	decodeData(t, response, &result)
	require.Len(t, result.Skills, 1)
	assert.Equal(t, "web-search", result.Skills[0].SkillID)
	require.NotNil(t, result.Skills[0].Install)
	assert.Equal(t, "global", result.Skills[0].Install.Scope)
}

func TestSkillsListProjectTrustFlag(t *testing.T) {
	server, handler, _ := setupSkillsServer(t)
	hostPath := t.TempDir()
	project, workspace, err := server.Projects.CreateWithWorkspace(t.Context(),
		domain.CreateProjectInput{Name: "Proj", HostPath: hostPath})
	require.NoError(t, err)

	response := request(t, handler, http.MethodGet, "/v1/skills?projectID="+project.ID, nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result struct {
		ProjectResourcesLoaded bool `json:"projectResourcesLoaded"`
	}
	decodeData(t, response, &result)
	assert.False(t, result.ProjectResourcesLoaded)

	require.NoError(t, server.Trust.Trust(workspace.ID, hostPath))
	response = request(t, handler, http.MethodGet, "/v1/skills?projectID="+project.ID, nil, true)
	decodeData(t, response, &result)
	assert.True(t, result.ProjectResourcesLoaded)
}

func TestToggleSkillDisabled(t *testing.T) {
	_, handler, home := setupSkillsServer(t)
	userRoot := filepath.Join(home, ".pi", "agent", "skills")
	response := request(t, handler, http.MethodPatch, "/v1/skills/disabled/web-search",
		map[string]any{"disabled": true}, true)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())

	data, err := os.ReadFile(filepath.Join(userRoot, "web-search", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "disable-model-invocation: true")

	response = request(t, handler, http.MethodPatch, "/v1/skills/disabled/web-search",
		map[string]any{"disabled": false}, true)
	require.Equal(t, http.StatusNoContent, response.Code)
	data, err = os.ReadFile(filepath.Join(userRoot, "web-search", "SKILL.md"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "disable-model-invocation")
}

func TestSkillPathTraversalRejected(t *testing.T) {
	_, handler, _ := setupSkillsServer(t)
	for _, path := range []string{
		"/v1/skills/disabled/%2E%2E%2F%2E%2E%2Fetc",
		"/v1/skills/disabled/%2Fetc%2Fpasswd",
		"/v1/skills/disabled/..%2Foutside",
	} {
		response := request(t, handler, http.MethodPatch, path, map[string]any{"disabled": true}, true)
		assert.Equal(t, http.StatusBadRequest, response.Code, path)
	}
	// Literal ".." is canonicalized by ServeMux into a redirect before the
	// handler runs; that must never reach a 2xx write.
	response := request(t, handler, http.MethodPatch, "/v1/skills/disabled/../outside",
		map[string]any{"disabled": true}, true)
	assert.NotEqual(t, http.StatusNoContent, response.Code)
}

func TestInstallSkillValidatesBodyAndScope(t *testing.T) {
	_, handler, _ := setupSkillsServer(t)

	// Missing package.
	response := request(t, handler, http.MethodPost, "/v1/skills/install",
		map[string]any{"scope": "global"}, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)

	// Invalid scope.
	response = request(t, handler, http.MethodPost, "/v1/skills/install",
		map[string]any{"package": "acme/skills@web", "scope": "system"}, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)

	// Project scope without projectId.
	response = request(t, handler, http.MethodPost, "/v1/skills/install",
		map[string]any{"package": "acme/skills@web", "scope": "project"}, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)
}

func TestInstallProjectScopeRequiresTrust(t *testing.T) {
	server, handler, _ := setupSkillsServer(t)
	project, _, err := server.Projects.CreateWithWorkspace(t.Context(),
		domain.CreateProjectInput{Name: "Proj", HostPath: t.TempDir()})
	require.NoError(t, err)

	response := request(t, handler, http.MethodPost, "/v1/skills/install",
		map[string]any{"package": "acme/skills@web", "scope": "project", "projectId": project.ID}, true)
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
}

func TestRemoveSkillPathTraversalRejected(t *testing.T) {
	_, handler, _ := setupSkillsServer(t)
	response := request(t, handler, http.MethodPost, "/v1/skills/remove/%2E%2E%2Fescape", nil, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)
}

func TestSearchSkillRequiresAuth(t *testing.T) {
	_, handler, _ := setupSkillsServer(t)
	response := request(t, handler, http.MethodGet, "/v1/skills/search?q=web", nil, false)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestSkillJSONEnvelopeShape(t *testing.T) {
	_, handler, _ := setupSkillsServer(t)
	response := request(t, handler, http.MethodGet, "/v1/skills", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &raw))
	_, hasData := raw["data"]
	assert.True(t, hasData, "response must use the data envelope")
}
