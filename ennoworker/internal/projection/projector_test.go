package projection_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projection"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectorAppliesSessionOutboxIdempotently(t *testing.T) {
	stores, projects, sessions, projectID := setupProjection(t)
	session, err := sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: projectID, Title: "Analysis"})
	require.NoError(t, err)
	projector := &projection.Projector{Stores: stores, Sessions: sessions}

	processed, err := projector.DrainSession(context.Background(), session.ID, 100)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	processed, err = projector.DrainSession(context.Background(), session.ID, 100)
	require.NoError(t, err)
	assert.Zero(t, processed)

	var title, storedProject string
	require.NoError(t, stores.Catalog.QueryRow(`SELECT title,project_id FROM session_summaries WHERE session_id=?`, session.ID).Scan(&title, &storedProject))
	assert.Equal(t, "Analysis", title)
	assert.Equal(t, projects.Root, filepath.Join(filepath.Dir(projects.Root), "projects"))
	assert.Equal(t, projectID, storedProject)
	var catalogEvents, usageEvents int
	require.NoError(t, stores.Catalog.QueryRow(`SELECT COUNT(*) FROM applied_projection_events`).Scan(&catalogEvents))
	require.NoError(t, stores.Usage.QueryRow(`SELECT COUNT(*) FROM applied_projection_events`).Scan(&usageEvents))
	assert.Equal(t, 1, catalogEvents)
	assert.Equal(t, 1, usageEvents)
}

func TestRebuildRestoresSessionOwnerAndUsageProjections(t *testing.T) {
	stores, projects, sessions, projectID := setupProjection(t)
	ctx := context.Background()
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "Analysis"})
	require.NoError(t, err)
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	messageID, runID, callID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO messages(id,session_id,role,status,created_at) VALUES(?,?,'user','complete',?)`, messageID, session.ID, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_runs(id,session_id,run_kind,base_message_id,status,created_at)
		VALUES(?,?,'context_compaction',?,'completed',?)`, runID, session.ID, messageID, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO model_calls(id,run_id,seq,provider_profile_id,model_profile_id,actual_model,
		input_tokens,output_tokens,cache_read_tokens,reasoning_tokens,started_at)
		VALUES(?,?,1,'deepseek','deepseek/deepseek-chat','deepseek-chat',100,20,5,2,?)`, callID, runID, now)
	require.NoError(t, err)

	rebuilder := &projection.Rebuilder{Stores: stores, Projects: projects, Sessions: sessions}
	require.NoError(t, rebuilder.Rebuild(ctx))
	var ownerSession string
	require.NoError(t, stores.Catalog.QueryRow(`SELECT session_id FROM owner_index WHERE resource_kind='run' AND resource_id=?`, runID).Scan(&ownerSession))
	assert.Equal(t, session.ID, ownerSession)
	var input, output int64
	require.NoError(t, stores.Usage.QueryRow(`SELECT input_tokens,output_tokens FROM usage_aggregates WHERE session_id=?`, session.ID).Scan(&input, &output))
	assert.Equal(t, int64(100), input)
	assert.Equal(t, int64(20), output)
}

func TestOpenQuarantinesCorruptProjection(t *testing.T) {
	directory := t.TempDir()
	catalog := filepath.Join(directory, "catalog.db")
	usage := filepath.Join(directory, "usage.db")
	require.NoError(t, os.WriteFile(catalog, []byte("not a sqlite database"), 0o600))

	stores, err := projection.Open(catalog, usage)
	require.NoError(t, err)
	require.NoError(t, stores.Close())
	matches, err := filepath.Glob(catalog + ".corrupt-*")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.FileExists(t, catalog)
}

func setupProjection(t *testing.T) (*projection.Stores, *projectstore.Store, *sessionstore.Manager, string) {
	t.Helper()
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	stores, err := projection.Open(filepath.Join(home, "data", "catalog.db"), filepath.Join(home, "data", "usage.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sessions.Close())
		require.NoError(t, stores.Close())
	})
	return stores, projects, sessions, project.ID
}
