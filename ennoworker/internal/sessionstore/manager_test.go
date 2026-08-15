package sessionstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateSessionUsesIndependentProjectSessionDirectory(t *testing.T) {
	manager, projects, projectID := setupManager(t)
	ctx := context.Background()
	session, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "Analysis"})
	require.NoError(t, err)
	assert.Equal(t, "Analysis", session.Title)

	directory := filepath.Join(projects.Root, projectID, "sessions", session.ID)
	require.FileExists(t, filepath.Join(directory, "session.json"))
	require.FileExists(t, filepath.Join(directory, "session.db"))
	require.DirExists(t, filepath.Join(directory, "artifacts"))
	require.DirExists(t, filepath.Join(directory, "snapshots", "skills"))

	db, err := manager.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	var storedSession, storedProject string
	require.NoError(t, db.QueryRow(`SELECT session_id,project_id FROM session_store_metadata WHERE singleton=1`).Scan(&storedSession, &storedProject))
	assert.Equal(t, session.ID, storedSession)
	assert.Equal(t, projectID, storedProject)
	found, err := manager.FindByID(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, session, found)
}

func TestSessionSchemaContainsOnlySessionAuthorityTables(t *testing.T) {
	manager, _, projectID := setupManager(t)
	session, err := manager.Create(context.Background(), domain.CreateSessionInput{ProjectID: projectID, Title: "Schema"})
	require.NoError(t, err)
	db, err := manager.OpenSession(context.Background(), session.ID)
	require.NoError(t, err)

	for _, table := range []string{
		"sessions", "messages", "turns", "agent_runs", "run_events", "tool_calls",
		"delegation_groups", "run_agent_flow", "run_mcp_servers", "skill_snapshots",
		"projection_outbox", "session_store_metadata", "schema_migrations",
	} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count))
		assert.Equal(t, 1, count, table)
	}
	for _, forbidden := range []string{
		"projects", "project_workspaces", "provider_profiles", "model_profiles", "agent_profiles",
		"agent_profile_versions", "agent_flow_profiles", "agent_flow_versions", "policy_profiles",
		"mcp_server_profiles", "mcp_server_profile_versions", "settings", "skill_roots",
	} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, forbidden).Scan(&count))
		assert.Zero(t, count, forbidden)
	}
	var foreignKeys int
	require.NoError(t, db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
	assert.Equal(t, 1, foreignKeys)
	var journalMode string
	require.NoError(t, db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode))
	assert.Equal(t, "wal", journalMode)
}

func TestTwoSessionsNeverShareRows(t *testing.T) {
	manager, _, projectID := setupManager(t)
	ctx := context.Background()
	first, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "First"})
	require.NoError(t, err)
	second, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "Second"})
	require.NoError(t, err)
	firstDB, err := manager.OpenSession(ctx, first.ID)
	require.NoError(t, err)
	secondDB, err := manager.OpenSession(ctx, second.ID)
	require.NoError(t, err)
	messageID := uuid.NewString()
	_, err = firstDB.Exec(`INSERT INTO messages(id,session_id,role,status,created_at) VALUES(?,?,'user','complete',?)`,
		messageID, first.ID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	var firstCount, secondCount int
	require.NoError(t, firstDB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&firstCount))
	require.NoError(t, secondDB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&secondCount))
	assert.Equal(t, 1, firstCount)
	assert.Zero(t, secondCount)
}

func TestOpenByResourceFindsOwningSession(t *testing.T) {
	manager, _, projectID := setupManager(t)
	ctx := context.Background()
	first, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "First"})
	require.NoError(t, err)
	_, err = manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "Second"})
	require.NoError(t, err)
	firstDB, err := manager.OpenSession(ctx, first.ID)
	require.NoError(t, err)
	messageID := uuid.NewString()
	_, err = firstDB.Exec(`INSERT INTO messages(id,session_id,role,status,created_at) VALUES(?,?,'user','complete',?)`,
		messageID, first.ID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	resolvedDB, sessionID, err := manager.OpenByResource(ctx, "message", messageID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, sessionID)
	var count int
	require.NoError(t, resolvedDB.QueryRow(`SELECT COUNT(*) FROM messages WHERE id=?`, messageID).Scan(&count))
	assert.Equal(t, 1, count)
	_, _, err = manager.OpenByResource(ctx, "unknown", messageID)
	assert.ErrorContains(t, err, "unsupported")
}

func TestListByProjectReadsSessionDatabases(t *testing.T) {
	manager, _, projectID := setupManager(t)
	ctx := context.Background()
	first, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "First"})
	require.NoError(t, err)
	second, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "Second"})
	require.NoError(t, err)

	sessions, err := manager.ListByProject(ctx, projectID, "active")
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	assert.ElementsMatch(t, []string{first.ID, second.ID}, []string{sessions[0].ID, sessions[1].ID})
}

func TestSessionManifestIdentityMismatchFailsClosed(t *testing.T) {
	manager, projects, projectID := setupManager(t)
	session, err := manager.Create(context.Background(), domain.CreateSessionInput{ProjectID: projectID, Title: "Session"})
	require.NoError(t, err)
	require.NoError(t, manager.Close())
	path := filepath.Join(projects.Root, projectID, "sessions", session.ID, "session.json")
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	contents = []byte(replaceOnce(string(contents), session.ID, uuid.NewString()))
	require.NoError(t, os.WriteFile(path, contents, 0o600))

	_, err = manager.OpenSession(context.Background(), session.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity does not match")
}

func setupManager(t *testing.T) (*sessionstore.Manager, *projectstore.Store, string) {
	t.Helper()
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects"), Now: fixedNow}
	project, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	manager := sessionstore.NewManager(projects.Root, projects)
	manager.Now = fixedNow
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	return manager, projects, project.ID
}

func fixedNow() time.Time {
	return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
}

func replaceOnce(value, old, replacement string) string {
	for index := 0; index+len(old) <= len(value); index++ {
		if value[index:index+len(old)] == old {
			return value[:index] + replacement + value[index+len(old):]
		}
	}
	return value
}
