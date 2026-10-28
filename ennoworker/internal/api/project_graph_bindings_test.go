package api

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectGraphBindingEnablesFlowRevision(t *testing.T) {
	server, handler, sources := flowAPIFixture(t)
	projects := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects")}
	server.Projects = &store.ProjectRepo{Files: projects}
	project, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "Project", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	_, err = sources.CreateFlowFromDefinition(flowWithRole("review-flow"))
	require.NoError(t, err)
	revision, err := sources.PublishFlowRevision("review-flow")
	require.NoError(t, err)

	missing := request(t, handler, http.MethodGet,
		"/v1/projects/"+project.ID+"/graph-bindings", nil, true)
	require.Equal(t, http.StatusOK, missing.Code, missing.Body.String())
	var empty []projectstore.GraphBinding
	decodeData(t, missing, &empty)
	assert.Empty(t, empty)

	invalid := request(t, handler, http.MethodPut,
		"/v1/projects/"+project.ID+"/graph-bindings/review-flow",
		map[string]any{"sourceKind": "flow", "revision": "v999999", "enabled": true}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, invalid.Code)
	assert.Contains(t, invalid.Body.String(), "source_revision_not_found")

	enabled := request(t, handler, http.MethodPut,
		"/v1/projects/"+project.ID+"/graph-bindings/review-flow",
		map[string]any{"sourceKind": "flow", "revision": revision.ID(), "enabled": true}, true)
	require.Equal(t, http.StatusOK, enabled.Code, enabled.Body.String())
	var binding projectstore.GraphBinding
	decodeData(t, enabled, &binding)
	assert.True(t, binding.Enabled)
	assert.Equal(t, revision.ID(), binding.Revision)

	// Enabling with no revision pins the latest published revision, never a
	// moving "latest" reference.
	pinned := request(t, handler, http.MethodPut,
		"/v1/projects/"+project.ID+"/graph-bindings/review-flow",
		map[string]any{"sourceKind": "flow", "enabled": true}, true)
	require.Equal(t, http.StatusOK, pinned.Code, pinned.Body.String())
	var pinnedBinding projectstore.GraphBinding
	decodeData(t, pinned, &pinnedBinding)
	assert.Equal(t, revision.ID(), pinnedBinding.Revision)

	mismatch := request(t, handler, http.MethodPut,
		"/v1/projects/"+project.ID+"/graph-bindings/review-flow",
		map[string]any{"sourceKind": "graph", "enabled": true}, true)
	assert.Equal(t, http.StatusConflict, mismatch.Code)

	removed := request(t, handler, http.MethodDelete,
		"/v1/projects/"+project.ID+"/graph-bindings/review-flow?sourceKind=flow", nil, true)
	assert.Equal(t, http.StatusNoContent, removed.Code)

	_, err = projects.FindGraphBinding(context.Background(), project.ID, globalsource.SourceKindFlow, "review-flow")
	require.Error(t, err)
}

func TestProjectGraphBindingRejectsUnknownSource(t *testing.T) {
	server, handler, _ := flowAPIFixture(t)
	projects := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects")}
	server.Projects = &store.ProjectRepo{Files: projects}
	project, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "Project", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	response := request(t, handler, http.MethodPut,
		"/v1/projects/"+project.ID+"/graph-bindings/missing",
		map[string]any{"sourceKind": "flow", "enabled": true}, true)
	assert.Equal(t, http.StatusNotFound, response.Code)
}
