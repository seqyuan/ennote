package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const globalHeroFixture = `---
schemaVersion: 1
handle: researcher
name: Researcher
description: Researches evidence
positioning: Use for evidence gathering.
icon: bot
color: neutral
model:
  ref: anthropic/claude-sonnet-4
  thinkingEffort: medium
  fallbacks: []
skills: []
authority: read_only
permissionCeiling: discuss
allowedTools: [read, ls, grep, find]
context:
  defaultMode: room
  allowedModes: [room, fresh]
  ownExecutionContinuity: none
delegation:
  admission: auto_within_budget
  allowedCallerKinds: [host]
  allowedStrategies: [single]
  maxInvocationsPerParentRun: 4
  maxConcurrentInstances: 2
  budgetCeiling:
    maxModelCalls: 4
    maxToolCalls: 8
    maxTotalTokens: 20000
    maxOutputTokens: 4000
    maxCostUsdMicros: 100000
    maxWallTimeMs: 120000
outputContract: text-v1
maxLoopIterations: 8
---
Gather evidence and distinguish it from assumptions.
`

func TestGlobalHeroCatalogCreateUpdateAndConflict(t *testing.T) {
	home := t.TempDir()
	server := &Server{Token: "test-token", GlobalSources: &globalsource.Store{HomeDir: home}}
	handler := server.Handler()
	document, err := heroesource.Parse([]byte(globalHeroFixture))
	require.NoError(t, err)

	response := request(t, handler, http.MethodPost, "/v1/global-heroes", map[string]any{"document": document}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalHeroDetail
	decodeData(t, response, &created)
	assert.Equal(t, "researcher", created.ID)
	assert.NotEmpty(t, created.Digest)

	response = request(t, handler, http.MethodGet, "/v1/global-heroes", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var summaries []globalHeroSummary
	decodeData(t, response, &summaries)
	require.Len(t, summaries, 1)
	assert.Equal(t, "Researcher", summaries[0].Name)

	updatedDocument := *document
	updatedDocument.Description = "Updated description"
	response = request(t, handler, http.MethodPatch, "/v1/global-heroes/researcher", map[string]any{"expectedDigest": created.Digest, "document": &updatedDocument}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updated globalHeroDetail
	decodeData(t, response, &updated)
	assert.NotEqual(t, created.Digest, updated.Digest)
	assert.Equal(t, "Updated description", updated.Document.Description)

	response = request(t, handler, http.MethodPatch, "/v1/global-heroes/researcher", map[string]any{"expectedDigest": created.Digest, "document": &updatedDocument}, true)
	assert.Equal(t, http.StatusConflict, response.Code)
	assert.True(t, strings.Contains(response.Body.String(), "source_digest_conflict"))
}
