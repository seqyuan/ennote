package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	server.SkillCatalog = func() map[string]string { return map[string]string{"cell-qc": "digest-cell-qc"} }
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
	assert.JSONEq(t, `[{"id":"cell-qc","digest":"digest-cell-qc"}]`, skillsJSON,
		"the turn carries the de-duplicated selection with the digest it was resolved against")
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

// A retry with the same idempotency key answers with the turn that was already
// accepted: the decision is made, the Run may be executing, and a Skill deleted
// from disk in the meantime must not turn the retry into a 400.
func TestSubmitInvocationReplaysAnAcceptedTurnAfterTheSkillDisappears(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	server.Skills = skillServiceFixture(t, "cell-qc")
	server.SkillCatalog = func() map[string]string { return map[string]string{"cell-qc": "digest-cell-qc"} }
	workspace := hostWorkspace(t, server, "skills-replay")
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
		"skills": []map[string]any{{"id": "cell-qc"}},
	}
	first := requestWithIdempotencyKey(t, handler, "/v1/sessions/"+createdSession.ID+"/invocations", body, "replay-key")
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())

	// The catalog loses the Skill, and the client retries the same request.
	server.SkillCatalog = func() map[string]string { return map[string]string{} }
	replay := requestWithIdempotencyKey(t, handler, "/v1/sessions/"+createdSession.ID+"/invocations", body, "replay-key")
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	var submission domain.TurnSubmission
	decodeData(t, replay, &submission)
	assert.True(t, submission.Existing)
	assert.Equal(t, []string{"cell-qc"}, submission.Skills,
		"a replay echoes what the turn carries, so a caller can notice a disagreement")
}

// requestWithIdempotencyKey posts an authenticated JSON body with an explicit
// Idempotency-Key, which is what a retrying client sends.
func requestWithIdempotencyKey(t *testing.T, handler http.Handler, path string, body any, key string) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

// Each selected Skill injects a whole body into the prompt, so the cap is a real
// limit rather than a suggestion.
func TestSubmitInvocationRefusesMoreSkillsThanTheCap(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	ids := make([]string, 0, maxSelectedSkills+1)
	catalog := map[string]string{}
	for index := 0; index <= maxSelectedSkills; index++ {
		id := fmt.Sprintf("skill-%02d", index)
		ids = append(ids, id)
		catalog[id] = "digest-" + id
	}
	server.SkillCatalog = func() map[string]string { return catalog }
	workspace := hostWorkspace(t, server, "skills-cap")
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

	selection := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		selection = append(selection, map[string]any{"id": id})
	}
	body := map[string]any{"text": "too many", "target": map[string]any{"kind": "host"}, "skills": selection}
	response := request(t, handler, http.MethodPost, "/v1/sessions/"+createdSession.ID+"/invocations", body, true)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), string(domain.ErrorTooManySkills))
}

// A Skill the model may not invoke on its own is exactly what a person picking it
// in the composer is for, so the validator must accept it.
func TestSubmitInvocationAcceptsAModelHiddenSkill(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	root := t.TempDir()
	dir := filepath.Join(root, "manual-only")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: manual-only\ndescription: fixture.\ndisable-model-invocation: true\n---\n\nbody\n"), 0o644))
	server.Skills = &skillsmgmt.Service{UserRoot: root, HomeDir: t.TempDir()}
	server.SkillCatalog = func() map[string]string { return map[string]string{"manual-only": "digest-manual"} }
	workspace := hostWorkspace(t, server, "manual-skill")
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

	body := map[string]any{"text": "use it", "target": map[string]any{"kind": "host"},
		"skills": []map[string]any{{"id": "manual-only"}}}
	response := request(t, handler, http.MethodPost, "/v1/sessions/"+createdSession.ID+"/invocations", body, true)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
}
