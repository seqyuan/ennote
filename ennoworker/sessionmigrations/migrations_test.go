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
	// table, every migration up to the question-approval schema already applied.
	// The boundary is pinned rather than "everything except the last one" because
	// the assertion below names a column that version 10 introduced; later
	// migrations are exercised by the same Apply call.
	const legacyBoundary = 10
	_, err = db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
	require.NoError(t, err)
	for _, migration := range migrations {
		if migration.Version >= legacyBoundary {
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
	require.False(t, hasKindColumn(), "the pre-upgrade database must actually predate migration 10")

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

// The released checksums are part of the contract with every installation: a
// migration that changes after it shipped makes the applier fail closed on a
// database a released build wrote. That failure mode has happened once already
// (the hero rename edited 0001 in place), so it is pinned here: this table is
// the checksum v0.7.0 recorded, and it must keep matching.
func TestReleasedMigrationChecksumsAreFrozen(t *testing.T) {
	released := map[int]string{
		1:  "sha256:0dd36084b608b671c7ed935700d571736fdca36d74d416659c6a91408af02d9e",
		2:  "sha256:17b1beaaa7f68574c0ee057fc64151d9171c37eb247ce0897b5dacfa7415dd0e",
		4:  "sha256:9d9bf2522d171730bd3d6bd6cef07c035020168c888ece0603b278b4e113fd31",
		5:  "sha256:a14684677d90fc9a9470ba4f392b3dc5bae5779a18c92a7faf9dc3b41ebf690e",
		6:  "sha256:0b1ed32df81c110119c935476c2a6ade9d57ab375be413898c66b9c700bc2c16",
		7:  "sha256:833bfe26450b39e3344cf208e7788a315d3bf74dea96eb95b4bb011c486142ca",
		8:  "sha256:4d9abc061ad759932c9c67a3344a0514c2f59fe092c0908bca292193930f1e00",
		9:  "sha256:592165580856e6fb06f3524fa3810c87422f22cc219104b37f7b41ed153f81e2",
		10: "sha256:78f849bf6acc7bcc3dc49ec954d47b9abff1af2b768bfccb6a76b581b7ea1592",
	}
	migrations, err := Sorted()
	require.NoError(t, err)
	for _, migration := range migrations {
		want, ok := released[migration.Version]
		if !ok {
			continue // a migration newer than the release under test
		}
		assert.Equal(t, want, migration.Checksum,
			"migration %d changed after it shipped: released databases would fail the checksum guard. "+
				"Add a new migration instead of editing this one", migration.Version)
	}
}

// Migration 0011 moves the schema and the attribution values together. A row
// written by the released build carries `speaker_kind='role'`, and the released
// UPDATE trigger rejects `'hero'`; the migration has to drop that trigger before
// the backfill and put the new one back afterwards.
func TestMigrationElevenBackfillsAttributionValues(t *testing.T) {
	db := openTestDB(t)
	migrations, err := Sorted()
	require.NoError(t, err)
	// Build the released schema and record it the way the applier does, so Apply
	// runs only the migrations this test is about.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS migration_history (
		prefix TEXT NOT NULL, version INTEGER NOT NULL, checksum TEXT NOT NULL,
		applied_at TEXT NOT NULL, PRIMARY KEY (prefix, version))`)
	require.NoError(t, err)
	for _, migration := range migrations {
		if migration.Version >= 11 {
			continue
		}
		_, err := db.Exec(migration.SQL)
		require.NoError(t, err, "apply migration %d", migration.Version)
		_, err = db.Exec(`INSERT INTO migration_history (prefix, version, checksum, applied_at) VALUES ('session', ?, ?, '2026-11-09T00:00:00Z')`,
			migration.Version, migration.Checksum)
		require.NoError(t, err, "record migration %d", migration.Version)
	}
	// The released schema still accepts its own vocabulary.
	_, err = db.Exec(`INSERT INTO messages (id,session_id,role,status,speaker_kind,addressee_kind,created_at)
		VALUES ('m1','s1','assistant','complete','role','role','2026-11-09T00:00:00Z')`)
	require.NoError(t, err, "the pre-migration schema must accept a role attribution")

	require.NoError(t, Apply(db))

	var speaker, addressee string
	require.NoError(t, db.QueryRow(`SELECT speaker_kind, addressee_kind FROM messages WHERE id='m1'`).Scan(&speaker, &addressee))
	assert.Equal(t, "hero", speaker)
	assert.Equal(t, "hero", addressee)
	// The recreated trigger still guards the column.
	_, err = db.Exec(`UPDATE messages SET speaker_kind='role' WHERE id='m1'`)
	require.Error(t, err, "the rebuilt attribution trigger must reject the retired value")
}

// Migration 0012 pins artifacts with a column rather than a table, so the state
// belongs to the one row that has it: `pinned_at` on the artifact, inside the
// Session database that owns that artifact row.
//
// Pinned here because the Go layer reads `pinned_at` unconditionally: a database
// that skipped this step would fail every artifact read, not only the pin routes,
// and the failure would surface as an internal error rather than as a missing
// feature.
func TestMigrationTwelveAddsThePinColumn(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Apply(db))

	var columns int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('artifacts') WHERE name='pinned_at'`).Scan(&columns))
	assert.Equal(t, 1, columns, "artifact reads scan pinned_at")

	var indexes int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM pragma_index_list('artifacts') WHERE name='idx_artifacts_pinned_at'`).Scan(&indexes))
	assert.Equal(t, 1, indexes, "the pinned list is read per Session and needs its index")

	// The column is nullable, so every artifact published before the pin existed
	// keeps reading as "not pinned" instead of needing a backfill.
	const now = "2026-11-11T00:00:00Z"
	_, err := db.Exec(`INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('s1','p1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO artifacts(id,project_id,session_id,name,storage_path,created_at)
		VALUES('a1','p1','s1','note.txt','blobs/a1',?)`, now)
	require.NoError(t, err)

	var pinnedAt sql.NullString
	require.NoError(t, db.QueryRow(`SELECT pinned_at FROM artifacts WHERE id='a1'`).Scan(&pinnedAt))
	assert.False(t, pinnedAt.Valid, "a pre-existing artifact must read as unpinned")
}
