package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A turn may name global Skills it wants loaded. The ids travel with the request
// and are frozen into the Run, so the prompt composition can show exactly which
// Skill bodies the model was given and by whom they were chosen.
func TestSubmitInvocationAcceptsSessionSelectedSkills(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	server.Skills = skillServiceFixture(t, "cell-qc")
	workspace := hostWorkspace(t, server, "skills-turn")
	created := request(t, handler, http.MethodPost, "/v1/projects",
		map[string]any{"name": "P", "hostPath": workspace}, true)
	var project struct {
		Project domain.Project `json:"project"`
	}
	decodeData(t, created, &project)
	session := request(t, handler, http.MethodPost, "/v1/projects/"+project.Project.ID+"/sessions",
		map[string]any{"title": "S"}, true)
	var createdSession struct {
		ID string `json:"id"`
	}
	decodeData(t, session, &createdSession)

	body := map[string]any{
		"text": "run this turn", "target": map[string]any{"kind": "host"},
		"skills": []map[string]any{{"id": "cell-qc"}, {"id": "cell-qc"}},
	}
	response := request(t, handler, http.MethodPost, "/v1/sessions/"+createdSession.ID+"/invocations", body, true)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	// The selection is durable on the turn, which is what the config freeze reads.
	db, err := server.SessionStores.OpenSession(context.Background(), createdSession.ID)
	require.NoError(t, err)
	var skillsJSON string
	require.NoError(t, db.QueryRow(`SELECT skills_json FROM turns WHERE session_id=?`, createdSession.ID).Scan(&skillsJSON))
	assert.JSONEq(t, `["cell-qc"]`, skillsJSON, "the turn carries the de-duplicated selection")
}

func TestSubmitInvocationRejectsAnUnknownSkill(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	server.Skills = skillServiceFixture(t, "cell-qc")
	workspace := hostWorkspace(t, server, "unknown-skill")
	created := request(t, handler, http.MethodPost, "/v1/projects",
		map[string]any{"name": "P", "hostPath": workspace}, true)
	var project struct {
		Project domain.Project `json:"project"`
	}
	decodeData(t, created, &project)
	session := request(t, handler, http.MethodPost, "/v1/projects/"+project.Project.ID+"/sessions",
		map[string]any{"title": "S"}, true)
	var createdSession struct {
		ID string `json:"id"`
	}
	decodeData(t, session, &createdSession)

	body := map[string]any{
		"text": "run this turn", "target": map[string]any{"kind": "host"},
		"skills": []map[string]any{{"id": "does-not-exist"}},
	}
	response := request(t, handler, http.MethodPost, "/v1/sessions/"+createdSession.ID+"/invocations", body, true)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "skill_not_found")
}

// skillServiceFixture builds a Skills service whose catalog holds one Skill per
// id, so a turn can name them.
func skillServiceFixture(t *testing.T, ids ...string) *skillsmgmt.Service {
	t.Helper()
	root := t.TempDir()
	for _, id := range ids {
		dir := filepath.Join(root, id)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte("---\nname: "+id+"\ndescription: fixture.\n---\n\nbody of "+id+"\n"), 0o644))
	}
	return &skillsmgmt.Service{UserRoot: root, HomeDir: t.TempDir()}
}
