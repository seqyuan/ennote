package sessionmigrations

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// A Session database written by a Worker that kept no migration prefix and no
// checksums must upgrade in place: the versions it already recorded are adopted,
// only the missing migrations run, and the database is usable afterwards. This is
// the shape every existing installation has on the first start of this build.
func TestApplyUpgradesALegacySessionDatabase(t *testing.T) {
	db := openTestDB(t)
	migrations, err := Sorted()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(migrations), 2)
	last := migrations[len(migrations)-1]

	// Build the database as the previous applier left it: one legacy version
	// table, every migration before the last already applied.
	_, err = db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
	require.NoError(t, err)
	for _, migration := range migrations {
		if migration.Version == last.Version {
			continue
		}
		_, err := db.Exec(migration.SQL)
		require.NoError(t, err, "apply migration %d", migration.Version)
		_, err = db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			migration.Version, "2026-09-23T00:00:00Z")
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO messages (id,session_id,role,status,created_at) VALUES ('m1','s1','user','complete','2026-09-23T00:00:00Z')`)
	require.NoError(t, err, "the legacy database must be writable before the upgrade")
	hasKindColumn := func() bool {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tool_approval_requests') WHERE name='kind'`).Scan(&count))
		return count == 1
	}
	require.False(t, hasKindColumn(), "the pre-upgrade database must actually predate the last migration")

	require.NoError(t, Apply(db))

	assert.True(t, hasKindColumn(), "the pending migration must have run")
	versions, err := recordedVersions(db)
	require.NoError(t, err)
	assert.Equal(t, len(migrations), len(versions), "every embedded version must be recorded: %v", versions)
	assert.Contains(t, versions, last.Version)
	var seq int
	require.NoError(t, db.QueryRow(`SELECT seq FROM messages WHERE id='m1'`).Scan(&seq))
	assert.Equal(t, 1, seq, "the post-migration backfill must have run on the upgraded database")
}

// A database the current applier created must not be adopted a second time: the
// migration history is authoritative once it exists.
func TestApplyIsIdempotentOnAFreshSessionDatabase(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Apply(db))
	migrations, err := Sorted()
	require.NoError(t, err)
	versions, err := recordedVersions(db)
	require.NoError(t, err)
	assert.Equal(t, len(migrations), len(versions))

	require.NoError(t, Apply(db))
	again, err := recordedVersions(db)
	require.NoError(t, err)
	assert.Equal(t, versions, again)
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	// One connection: a :memory: database is per connection.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func recordedVersions(db *sql.DB) ([]int, error) {
	rows, err := db.Query(`SELECT version FROM migration_history WHERE prefix='session' ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}
