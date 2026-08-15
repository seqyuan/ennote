package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const globalRoleFixture = `---
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

func TestGlobalRoleCatalogCreateUpdateAndConflict(t *testing.T) {
	home := t.TempDir()
	server := &Server{Token: "test-token", GlobalSources: &globalsource.Store{HomeDir: home}}
	handler := server.Handler()
	document, err := rolesource.Parse([]byte(globalRoleFixture))
	require.NoError(t, err)

	response := request(t, handler, http.MethodPost, "/v1/global-roles", map[string]any{"document": document}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalRoleDetail
	decodeData(t, response, &created)
	assert.Equal(t, "researcher", created.ID)
	assert.NotEmpty(t, created.Digest)

	response = request(t, handler, http.MethodGet, "/v1/global-roles", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var summaries []globalRoleSummary
	decodeData(t, response, &summaries)
	require.Len(t, summaries, 1)
	assert.Equal(t, "Researcher", summaries[0].Name)

	updatedDocument := *document
	updatedDocument.Description = "Updated description"
	response = request(t, handler, http.MethodPatch, "/v1/global-roles/researcher", map[string]any{"expectedDigest": created.Digest, "document": &updatedDocument}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updated globalRoleDetail
	decodeData(t, response, &updated)
	assert.NotEqual(t, created.Digest, updated.Digest)
	assert.Equal(t, "Updated description", updated.Document.Description)

	response = request(t, handler, http.MethodPatch, "/v1/global-roles/researcher", map[string]any{"expectedDigest": created.Digest, "document": &updatedDocument}, true)
	assert.Equal(t, http.StatusConflict, response.Code)
	assert.True(t, strings.Contains(response.Body.String(), "source_digest_conflict"))
}
