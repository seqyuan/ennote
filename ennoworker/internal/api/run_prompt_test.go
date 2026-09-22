package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/systemprompt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runPromptResponse struct {
	RunID           string                 `json:"runId"`
	Version         int                    `json:"version"`
	PlatformVersion string                 `json:"platformVersion"`
	Digest          string                 `json:"digest"`
	Sections        []domain.PromptSection `json:"sections"`
	SectionsDigest  string                 `json:"sectionsDigest"`
	ComposedDigest  string                 `json:"composedDigest"`
	Prompt          string                 `json:"prompt"`
	// The catalog state is part of the frozen record: it explains why a catalog
	// section is present or absent.
	SkillCatalogState  string `json:"skillCatalogState"`
	SkillCatalogDigest string `json:"skillCatalogDigest"`
	Recorded           bool   `json:"recorded"`
}

// insertRunRow seeds the minimum rows the router and the handler need: the
// resource lookup finds the Run in its Session database, and the projection
// reads the frozen snapshot plus the content-addressed prompt blob.
func insertRunRow(t *testing.T, ctx context.Context, db *sql.DB, sessionID, snapshotJSON, snapshotDigest string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	messageID, turnID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := db.ExecContext(ctx,
		`INSERT INTO messages (id,session_id,role,status,created_at) VALUES (?,?,'user','complete',?)`,
		messageID, sessionID, now)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO turns (id,session_id,client_request_id,user_message_id,status,created_at,updated_at)
		 VALUES (?,?,?,?,'running',?,?)`, turnID, sessionID, runID, messageID, now, now)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO agent_runs (id,turn_id,session_id,status,system_prompt_digest,system_prompt_snapshot_json,created_at)
		 VALUES (?,?,?,'running',?,?,?)`, runID, turnID, sessionID, snapshotDigest, snapshotJSON, now)
	require.NoError(t, err)
	return runID
}

func insertRunWithRecordedComposition(t *testing.T, ctx context.Context, db *sql.DB, sessionID string) (string, string, []domain.PromptSection) {
	t.Helper()
	composed := "base\n\n## Project Instructions - AGENTS.md\n\nrules"
	sections := []domain.PromptSection{
		{ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile",
			Bytes: 5, Digest: systemprompt.TextDigest("base\n")},
		{ID: "context.agents.project", Kind: domain.PromptSectionContext, Source: "project:AGENTS.md",
			Bytes: 40, Digest: systemprompt.TextDigest("project rules")},
	}
	sectionsDigest, err := systemprompt.SectionsDigest(sections)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(composed))
	composedDigest := hex.EncodeToString(sum[:])
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `INSERT INTO prompt_blobs (digest,bytes,text,created_at) VALUES (?,?,?,?)`,
		composedDigest, len(composed), composed, now)
	require.NoError(t, err)

	snapshot, err := json.Marshal(domain.SystemPromptSnapshot{
		Version: 1, PlatformVersion: "hosted-v1", AgentPrompt: composed, Digest: "base-digest",
		Sections: sections, SectionsDigest: sectionsDigest, ComposedDigest: composedDigest,
		SkillCatalogState: domain.SkillCatalogMaterialized, SkillCatalogDigest: "catalog-digest",
	})
	require.NoError(t, err)
	return insertRunRow(t, ctx, db, sessionID, string(snapshot), "base-digest"), composed, sections
}

func TestGetRunPromptReturnsTheFrozenComposition(t *testing.T) {
	_, handler, _, session, db := setupGraphRunsServer(t)
	ctx := context.Background()
	runID, composed, sections := insertRunWithRecordedComposition(t, ctx, db, session.ID)

	rec := request(t, handler, http.MethodGet, "/v1/runs/"+runID+"/prompt", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got runPromptResponse
	decodeData(t, rec, &got)
	require.True(t, got.Recorded)
	assert.Equal(t, runID, got.RunID)
	assert.Equal(t, 1, got.Version)
	assert.Equal(t, "hosted-v1", got.PlatformVersion)
	assert.Equal(t, "base-digest", got.Digest)
	assert.Equal(t, composed, got.Prompt)
	assert.Equal(t, sections, got.Sections)
	assert.NotEmpty(t, got.SectionsDigest)
	assert.NotEmpty(t, got.ComposedDigest)
	assert.Equal(t, "materialized", got.SkillCatalogState,
		"the recorded catalog state travels with the composition so absence is explainable")
	assert.Equal(t, "catalog-digest", got.SkillCatalogDigest)
}

// A Run frozen before composition freezing existed must read as not recorded,
// with an empty array rather than a null the client has to special-case.
func TestGetRunPromptReportsAnUnrecordedComposition(t *testing.T) {
	_, handler, _, session, db := setupGraphRunsServer(t)
	ctx := context.Background()
	snapshot, err := json.Marshal(domain.SystemPromptSnapshot{
		Version: 1, PlatformVersion: "hosted-v1", AgentPrompt: "base", Digest: "base-digest",
	})
	require.NoError(t, err)
	runID := insertRunRow(t, ctx, db, session.ID, string(snapshot), "base-digest")

	rec := request(t, handler, http.MethodGet, "/v1/runs/"+runID+"/prompt", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got runPromptResponse
	decodeData(t, rec, &got)
	assert.False(t, got.Recorded)
	assert.Empty(t, got.Sections)
	assert.Empty(t, got.Prompt)
	assert.Empty(t, got.ComposedDigest)
	assert.Empty(t, got.SkillCatalogState)
	assert.Equal(t, "base-digest", got.Digest, "the frozen base-prompt digest is still reported")
	assert.Contains(t, rec.Body.String(), `"sections":[]`, "sections must serialize as an array")
}

func TestGetRunPromptRejectsAnUnknownRun(t *testing.T) {
	_, handler, _, _, _ := setupGraphRunsServer(t)

	rec := request(t, handler, http.MethodGet, "/v1/runs/"+uuid.NewString()+"/prompt", nil, true)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// The Run index must let a carrier decide whether the detail endpoint has
// anything to show without calling it.
func TestGetRunExposesTheCompositionIndex(t *testing.T) {
	server, handler, _, session, db := setupGraphRunsServer(t)
	// GET /v1/runs/{id} hydrates through the Run repo; the session-scoped clone
	// supplies the database, so a zero-value repo is enough to route it.
	server.Runs = &store.RunRepo{}
	handler = server.Handler()
	ctx := context.Background()
	runID, _, _ := insertRunWithRecordedComposition(t, ctx, db, session.ID)

	rec := request(t, handler, http.MethodGet, "/v1/runs/"+runID, nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var run domain.AgentRun
	decodeData(t, rec, &run)
	require.NotNil(t, run.SystemPrompt)
	assert.NotEmpty(t, run.SystemPrompt.SectionsDigest)
	assert.NotEmpty(t, run.SystemPrompt.ComposedDigest)
	assert.NotContains(t, rec.Body.String(), "Project Instructions",
		"the Run index must never carry the prompt text")
}
