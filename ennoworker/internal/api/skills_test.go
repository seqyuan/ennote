package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
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
	require.NoError(t, store.MigrateFixtureSchema(db))

	home := t.TempDir()
	userRoot := filepath.Join(home, ".pi", "agent", "skills")
	require.NoError(t, os.MkdirAll(filepath.Join(userRoot, "web-search"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userRoot, "web-search", "SKILL.md"),
		[]byte("---\nname: web-search\ndescription: Search the web.\n---\n\nbody\n"), 0o644))

	trustStore, err := workspace.NewTrustStore(filepath.Join(home, "ennote"))
	require.NoError(t, err)

	settings := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	).Settings

	server := &Server{
		DB: db, Token: "test-token", Sandbox: "none",
		Projects:   newFileProjects(t),
		Skills:     &skillsmgmt.Service{UserRoot: userRoot, BuiltinRoot: "", HomeDir: home},
		SkillRoots: &store.SkillRootRepo{Settings: settings},
		Trust:      trustStore,
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
			SkillID string                  `json:"skillId"`
			Name    string                  `json:"name"`
			Install *skillsmgmt.InstallInfo `json:"install"`
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

func TestSkillRootsRequireAuth(t *testing.T) {
	_, handler, _ := setupSkillsServer(t)
	response := request(t, handler, http.MethodGet, "/v1/skills/roots", nil, false)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

// Regression: adding a root must make its skills visible without restart.
// (The in-memory AdditionalRoots snapshot is refreshed after roots CRUD.)
func TestSkillRootCreateRefreshesCatalog(t *testing.T) {
	_, handler, home := setupSkillsServer(t)
	rootDir := filepath.Join(home, "extra")
	writeSkillAt := func(dir, name string) string {
		skillDir := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
			[]byte("---\nname: "+name+"\n---\n\nbody\n"), 0o644))
		return skillDir
	}
	skillDir := writeSkillAt(rootDir, "from-extra-root")

	// Not visible before adding the root.
	response := request(t, handler, http.MethodGet, "/v1/skills", nil, true)
	var before struct {
		Skills []struct {
			SkillID string `json:"skillId"`
		} `json:"skills"`
	}
	decodeData(t, response, &before)
	assert.False(t, containsSkill(before.Skills, "from-extra-root"))

	// Add root → skill appears immediately (no restart).
	response = request(t, handler, http.MethodPost, "/v1/skills/roots",
		map[string]any{"name": "extra", "path": rootDir, "agentKind": "generic"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	response = request(t, handler, http.MethodGet, "/v1/skills", nil, true)
	var after struct {
		Skills []struct {
			SkillID string `json:"skillId"`
			RelPath string `json:"relPath"`
			BaseDir string `json:"baseDir"`
		} `json:"skills"`
	}
	decodeData(t, response, &after)
	foundInAfter := false
	for _, skill := range after.Skills {
		if skill.SkillID == "from-extra-root" {
			foundInAfter = true
		}
	}
	require.True(t, foundInAfter)
	var found struct{ RelPath, BaseDir string }
	for _, skill := range after.Skills {
		if skill.SkillID == "from-extra-root" {
			found = struct{ RelPath, BaseDir string }{skill.RelPath, skill.BaseDir}
		}
	}
	// Toggle uses the directory relPath, not the manifest ID.
	response = request(t, handler, http.MethodPatch, "/v1/skills/disabled/"+found.RelPath,
		map[string]any{"disabled": true}, true)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "disable-model-invocation: true")
}

func TestCreateSkillEndpointFeedsLiveCatalog(t *testing.T) {
	server, handler, _ := setupSkillsServer(t)

	response := request(t, handler, http.MethodPost, "/v1/skills", map[string]any{
		"id":           "release-notes",
		"name":         "Release Notes",
		"description":  "Draft release notes from git history.",
		"prompt":       "Read the git log and write release notes.",
		"allowedTools": []string{"read", "bash"},
	}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())

	var created struct {
		ID           string   `json:"id"`
		Path         string   `json:"path"`
		Description  string   `json:"description"`
		AllowedTools []string `json:"allowedTools"`
	}
	decodeData(t, response, &created)
	assert.Equal(t, "release-notes", created.ID)
	assert.Equal(t, filepath.Join(server.Skills.UserRoot, "release-notes"), created.Path)
	assert.Equal(t, "Draft release notes from git history.", created.Description)
	assert.Equal(t, []string{"read", "bash"}, created.AllowedTools)

	// GET /v1/skills lists the new Skill without a Worker restart.
	response = request(t, handler, http.MethodGet, "/v1/skills", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var listed struct {
		Skills []struct {
			SkillID     string `json:"skillId"`
			Name        string `json:"name"`
			RelPath     string `json:"relPath"`
			FilePath    string `json:"filePath"`
			Description string `json:"description"`
		} `json:"skills"`
	}
	decodeData(t, response, &listed)
	found := false
	for _, skill := range listed.Skills {
		if skill.SkillID == "release-notes" {
			found = true
			assert.Equal(t, "release-notes", skill.RelPath)
			assert.True(t, strings.HasSuffix(skill.FilePath, filepath.Join("release-notes", "SKILL.md")))
			assert.Equal(t, "Draft release notes from git history.", skill.Description)
		}
	}
	assert.True(t, found, "created skill must appear in the live catalog")

	// Flow/Role freeze reads the catalog from disk at freeze time; the new ID
	// must carry a non-empty digest there too (no restart, no cached snapshot).
	frozen := skills.DigestCatalog(server.Skills.UserRoot)
	assert.NotEmpty(t, frozen["release-notes"])

	loaded, err := skills.Load(created.Path)
	require.NoError(t, err)
	assert.Contains(t, loaded.PromptText, "Read the git log and write release notes.")
}

func TestCreateSkillEndpointValidation(t *testing.T) {
	server, handler, _ := setupSkillsServer(t)

	invalidIDs := []string{"", "../escape", "a/b", "Upper", "with space", ".."}
	for _, id := range invalidIDs {
		response := request(t, handler, http.MethodPost, "/v1/skills",
			map[string]any{"id": id, "prompt": "body"}, true)
		assert.Equal(t, http.StatusBadRequest, response.Code, id)
	}

	// Empty prompt.
	response := request(t, handler, http.MethodPost, "/v1/skills",
		map[string]any{"id": "no-prompt", "prompt": "  "}, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)

	// Unknown field rejected by the strict decoder.
	response = request(t, handler, http.MethodPost, "/v1/skills",
		map[string]any{"id": "strict-body", "prompt": "body", "extra": true}, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)

	// Nothing escaped the user skills root.
	_, err := os.Stat(filepath.Join(server.Skills.UserRoot, "..", "escape"))
	assert.True(t, os.IsNotExist(err))

	// Duplicate ID conflicts instead of overwriting.
	response = request(t, handler, http.MethodPost, "/v1/skills",
		map[string]any{"id": "duplicate-skill", "prompt": "first"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	response = request(t, handler, http.MethodPost, "/v1/skills",
		map[string]any{"id": "duplicate-skill", "prompt": "second"}, true)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	encoded, err := os.ReadFile(filepath.Join(server.Skills.UserRoot, "duplicate-skill", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "first")
}

func TestCreateSkillRequiresAuth(t *testing.T) {
	_, handler, _ := setupSkillsServer(t)
	response := request(t, handler, http.MethodPost, "/v1/skills",
		map[string]any{"id": "unauth-skill", "prompt": "body"}, false)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func containsSkill(skills []struct {
	SkillID string `json:"skillId"`
}, id string) bool {
	for _, skill := range skills {
		if skill.SkillID == id {
			return true
		}
	}
	return false
}
