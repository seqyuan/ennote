package api

import (
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localHeroPayload is the document the editor sends when it creates a Task with a
// new Graph-private Hero. It mirrors what the Hero form writes.
func localHeroPayload(handle string) map[string]any {
	return map[string]any{
		"schemaVersion": 1, "handle": handle, "name": handle,
		"description": "", "positioning": "", "icon": "user", "color": "neutral",
		"model":             map[string]any{"ref": "provider/model", "thinkingEffort": "default", "fallbacks": []string{}},
		"skills":            []any{},
		"authority":         "read_only",
		"permissionCeiling": "discuss",
		"allowedTools":      []string{"read"},
		"context": map[string]any{
			"defaultMode": "task_only", "allowedModes": []string{"task_only"}, "ownExecutionContinuity": "none",
		},
		"delegation": map[string]any{
			"admission": "auto_within_budget", "allowedCallerKinds": []string{"host"},
			"allowedStrategies": []string{"single"}, "maxInvocationsPerParentRun": 1,
			"maxConcurrentInstances": 1,
			"budgetCeiling": map[string]any{
				"maxModelCalls": 1, "maxToolCalls": 2, "maxTotalTokens": 1000,
				"maxOutputTokens": 500, "maxCostUsdMicros": 0, "maxWallTimeMs": 60000,
			},
		},
		"outputContract": "text-v1", "maxLoopIterations": 4,
		"prompt": "Do the thing.",
	}
}

func createGraphForHeroTest(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "qc", "name": "QC"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalGraphDetail
	response = request(t, handler, http.MethodGet, "/v1/graphs/qc", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	decodeData(t, response, &created)
	return created.Digest
}

// The Add Task path: one mutation creates the Task and the Graph-private Hero it
// references, so the reference can never point at a Hero nobody wrote.
func TestUpdateGraphCreatesTheLocalHeroItReferences(t *testing.T) {
	_, handler, _ := setupGlobalGraphServer(t)
	digest := createGraphForHeroTest(t, handler)

	response := request(t, handler, http.MethodPatch, "/v1/graphs/qc", map[string]any{
		"expectedDigest": digest,
		"task": map[string]any{"id": "review", "value": map[string]any{
			"hero": "local/review", "goal": "Review the report",
		}},
		"localHero": localHeroPayload("review"),
	}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	var detail GraphLocalHeroDetailJSON
	response = request(t, handler, http.MethodGet, "/v1/graphs/qc/heroes/review", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	decodeData(t, response, &detail)
	assert.Equal(t, "review", detail.ID)
	assert.Equal(t, "Do the thing.", detail.Document.Prompt)
	assert.NotEmpty(t, detail.Digest)

	// The edited draft is what the next publish freezes; the handle is identity.
	edited := localHeroPayload("review")
	edited["prompt"] = "Do the other thing."
	response = request(t, handler, http.MethodPatch, "/v1/graphs/qc/heroes/review", map[string]any{
		"expectedDigest": detail.Digest, "document": edited,
	}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updated GraphLocalHeroDetailJSON
	decodeData(t, response, &updated)
	assert.Equal(t, "Do the other thing.", updated.Document.Prompt)
	assert.NotEqual(t, detail.Digest, updated.Digest)

	// Reusing the digest from before that edit is a conflict, not a silent overwrite.
	response = request(t, handler, http.MethodPatch, "/v1/graphs/qc/heroes/review", map[string]any{
		"expectedDigest": detail.Digest, "document": edited,
	}, true)
	assert.Equal(t, http.StatusConflict, response.Code, response.Body.String())
}

// Every relation between the payload and the mutation is enforced server-side;
// the editor is not the only caller.
func TestUpdateGraphLocalHeroValidation(t *testing.T) {
	_, handler, _ := setupGlobalGraphServer(t)
	digest := createGraphForHeroTest(t, handler)

	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "a Hero with no Task to reference it",
			body: map[string]any{"expectedDigest": digest, "localHero": localHeroPayload("review")},
		},
		{
			name: "a Hero alongside a task deletion",
			body: map[string]any{
				"expectedDigest": digest,
				"task":           map[string]any{"id": "review", "value": nil},
				"localHero":      localHeroPayload("review"),
			},
		},
		{
			name: "a Task that references a different Hero",
			body: map[string]any{
				"expectedDigest": digest,
				"task": map[string]any{"id": "review", "value": map[string]any{
					"hero": "local/something-else", "goal": "Review",
				}},
				"localHero": localHeroPayload("review"),
			},
		},
		{
			name: "an invalid Hero document",
			body: map[string]any{
				"expectedDigest": digest,
				"task": map[string]any{"id": "review", "value": map[string]any{
					"hero": "local/review", "goal": "Review",
				}},
				"localHero": map[string]any{"schemaVersion": 1, "handle": "review"},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := request(t, handler, http.MethodPatch, "/v1/graphs/qc", testCase.body, true)
			assert.GreaterOrEqual(t, response.Code, 400, response.Body.String())
			// Nothing was written: no Task, no Hero.
			graph := request(t, handler, http.MethodGet, "/v1/graphs/qc", nil, true)
			require.Equal(t, http.StatusOK, graph.Code)
			var detail globalGraphDetail
			decodeData(t, graph, &detail)
			assert.Empty(t, detail.Definition.Tasks)
			hero := request(t, handler, http.MethodGet, "/v1/graphs/qc/heroes/review", nil, true)
			assert.Equal(t, http.StatusNotFound, hero.Code)
		})
	}
}

func TestGraphLocalHeroIsNotFoundForAnotherGraph(t *testing.T) {
	_, handler, _ := setupGlobalGraphServer(t)
	digest := createGraphForHeroTest(t, handler)
	require.Equal(t, http.StatusOK, request(t, handler, http.MethodPatch, "/v1/graphs/qc", map[string]any{
		"expectedDigest": digest,
		"task": map[string]any{"id": "review", "value": map[string]any{
			"hero": "local/review", "goal": "Review",
		}},
		"localHero": localHeroPayload("review"),
	}, true).Code)

	require.Equal(t, http.StatusCreated, request(t, handler, http.MethodPost, "/v1/graphs",
		map[string]any{"id": "other", "name": "Other"}, true).Code)
	response := request(t, handler, http.MethodGet, "/v1/graphs/other/heroes/review", nil, true)
	assert.Equal(t, http.StatusNotFound, response.Code, "a Graph-local Hero is not visible from another Graph")
}

// GraphLocalHeroDetailJSON mirrors the response shape the handlers write; the
// contract's generated type is checked by the sample test.
type GraphLocalHeroDetailJSON struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Path     string                `json:"path"`
	Digest   string                `json:"digest"`
	Document *heroesource.Document `json:"document"`
}
