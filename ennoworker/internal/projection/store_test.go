package projection_test

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/catalogmigrations"
	"github.com/seqyuan/ennote/ennoworker/internal/projection"
	"github.com/seqyuan/ennote/ennoworker/internal/sqlmigrations"
	"github.com/seqyuan/ennote/ennoworker/usagemigrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Projection databases written before versioning existed carry the InitialSchema
// tables and no version table. Opening one must adopt that state, keep the rows,
// and record the version instead of quarantining and rebuilding the file.
func TestOpenAdoptsLegacyProjectionDatabases(t *testing.T) {
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "catalog.db")
	usagePath := filepath.Join(directory, "usage.db")
	writeLegacyProjection(t, catalogPath, catalogmigrations.FS, `INSERT INTO project_summaries
		(project_id,name,description,status,workspace_path,created_at,updated_at)
		VALUES ('project-1','Project','','active','/tmp/project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	writeLegacyProjection(t, usagePath, usagemigrations.FS, `INSERT INTO usage_aggregates
		(usage_day,project_id,session_id,provider_id,model_id,updated_at)
		VALUES ('2026-01-01','project-1','session-1','provider-1','model-1','2026-01-01T00:00:00Z')`)

	stores, err := projection.Open(catalogPath, usagePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stores.Close()) })

	quarantined, err := filepath.Glob(catalogPath + ".corrupt-*")
	require.NoError(t, err)
	assert.Empty(t, quarantined, "a legacy projection must be upgraded, not quarantined")

	var name string
	require.NoError(t, stores.Catalog.QueryRow(`SELECT name FROM project_summaries WHERE project_id='project-1'`).Scan(&name))
	assert.Equal(t, "Project", name, "the legacy rows must survive the upgrade")
	var tokens int
	require.NoError(t, stores.Usage.QueryRow(`SELECT input_tokens FROM usage_aggregates WHERE session_id='session-1'`).Scan(&tokens))
	assert.Zero(t, tokens)
	assert.Equal(t, []int{1}, recordedVersions(t, stores.Catalog, "catalog"))
	assert.Equal(t, []int{1}, recordedVersions(t, stores.Usage, "usage"))
}

// writeLegacyProjection builds a database the way the pre-versioning applier
// left it: the InitialSchema SQL executed directly, no version table at all.
func writeLegacyProjection(t *testing.T, path string, migrations fs.FS, seed string) {
	t.Helper()
	history, err := sqlmigrations.Sorted(migrations)
	require.NoError(t, err)
	require.NotEmpty(t, history)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(history[0].SQL)
	require.NoError(t, err)
	_, err = db.Exec(seed)
	require.NoError(t, err)
}

func recordedVersions(t *testing.T, db *sql.DB, prefix string) []int {
	t.Helper()
	rows, err := db.Query(`SELECT version FROM migration_history WHERE prefix=? ORDER BY version`, prefix)
	require.NoError(t, err)
	defer rows.Close()
	var versions []int
	for rows.Next() {
		var version int
		require.NoError(t, rows.Scan(&version))
		versions = append(versions, version)
	}
	require.NoError(t, rows.Err())
	return versions
}
