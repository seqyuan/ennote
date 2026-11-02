package sqlmigrations

import (
	"database/sql"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	// One connection: a :memory: database is per connection.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqlSet(prefix string, files fstest.MapFS) Set {
	return Set{Prefix: prefix, FS: files}
}

func appliedVersions(t *testing.T, db *sql.DB, prefix string) []int {
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

func TestSortedOrdersMigrationsAndRejectsBadNames(t *testing.T) {
	migrations, err := Sorted(fstest.MapFS{
		"0002_second.sql": {Data: []byte("SELECT 2;")},
		"0001_first.sql":  {Data: []byte("SELECT 1;")},
		"notes.txt":       {Data: []byte("ignored")},
	})
	require.NoError(t, err)
	require.Len(t, migrations, 2)
	assert.Equal(t, 1, migrations[0].Version)
	assert.Equal(t, "first", migrations[0].Name)
	assert.Equal(t, "SELECT 1;", migrations[0].SQL)
	assert.Equal(t, checksum([]byte("SELECT 1;")), migrations[0].Checksum)
	assert.Equal(t, 2, migrations[1].Version)

	_, err = Sorted(fstest.MapFS{"oops.sql": {Data: []byte("SELECT 1;")}})
	require.ErrorContains(t, err, "invalid migration file name")

	_, err = Sorted(fstest.MapFS{"0001_one.sql": {Data: []byte("SELECT 1;")}, "0001_two.sql": {Data: []byte("SELECT 2;")}})
	require.ErrorContains(t, err, "duplicate migration version 1")
}

func TestApplyRunsPendingMigrationsOnceAndRecordsChecksums(t *testing.T) {
	db := openTestDB(t)
	files := fstest.MapFS{
		"0001_table.sql": {Data: []byte(`CREATE TABLE t (id TEXT PRIMARY KEY);`)},
		"0002_row.sql":   {Data: []byte(`INSERT INTO t (id) VALUES ('a');`)},
	}
	require.NoError(t, Apply(db, sqlSet("demo", files)))
	assert.Equal(t, []int{1, 2}, appliedVersions(t, db, "demo"))

	// A second start must not replay anything.
	require.NoError(t, Apply(db, sqlSet("demo", files)))
	assert.Equal(t, []int{1, 2}, appliedVersions(t, db, "demo"))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&count))
	assert.Equal(t, 1, count)
}

// An embedded migration that changed after it ran means production and tests are
// on different schemas. Failing closed is the only safe answer.
func TestApplyFailsClosedOnAChangedMigration(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Apply(db, sqlSet("demo", fstest.MapFS{
		"0001_table.sql": {Data: []byte(`CREATE TABLE t (id TEXT PRIMARY KEY);`)},
	})))

	err := Apply(db, sqlSet("demo", fstest.MapFS{
		"0001_table.sql": {Data: []byte(`CREATE TABLE t (id TEXT PRIMARY KEY, extra TEXT);`)},
	}))
	require.ErrorContains(t, err, "demo migration 1 changed after it was applied")
}

// A version recorded by the database that this build does not know is a schema
// from the future; applying a shorter history must not pretend otherwise.
func TestApplyFailsClosedOnAnUnknownAppliedVersion(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Apply(db, sqlSet("demo", fstest.MapFS{
		"0001_table.sql": {Data: []byte(`CREATE TABLE t (id TEXT PRIMARY KEY);`)},
		"0002_row.sql":   {Data: []byte(`INSERT INTO t (id) VALUES ('a');`)},
	})))

	err := Apply(db, sqlSet("demo", fstest.MapFS{
		"0001_table.sql": {Data: []byte(`CREATE TABLE t (id TEXT PRIMARY KEY);`)},
	}))
	require.ErrorContains(t, err, "records migration 2, which this build does not carry")
}

// A lower-numbered migration that appears after a higher one was recorded is an
// out-of-order history: applying it would run a step against a schema that
// already moved past it.
func TestApplyFailsClosedOnAnOutOfOrderMigration(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Apply(db, sqlSet("demo", fstest.MapFS{
		"0002_second.sql": {Data: []byte(`CREATE TABLE b (id TEXT PRIMARY KEY);`)},
	})))

	err := Apply(db, sqlSet("demo", fstest.MapFS{
		"0001_first.sql":  {Data: []byte(`CREATE TABLE a (id TEXT PRIMARY KEY);`)},
		"0002_second.sql": {Data: []byte(`CREATE TABLE b (id TEXT PRIMARY KEY);`)},
	}))
	require.ErrorContains(t, err, "demo migration 1 is out of order: version 2 is already applied")
}

// One database file serving two histories must not let either adopt or rewrite
// the other's rows.
func TestApplyRefusesAForeignHistory(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Apply(db, sqlSet("catalog", fstest.MapFS{
		"0001_table.sql": {Data: []byte(`CREATE TABLE c (id TEXT PRIMARY KEY);`)},
	})))

	err := Apply(db, sqlSet("usage", fstest.MapFS{
		"0001_table.sql": {Data: []byte(`CREATE TABLE u (id TEXT PRIMARY KEY);`)},
	}))
	require.ErrorContains(t, err, "database holds the catalog migration history, refusing to apply the usage history")
}

// Sessions written before versioning kept their applied versions in an
// unprefixed, unchecked table. Those versions must be adopted so the upgrade
// runs only the migrations the database is actually missing.
func TestApplyAdoptsALegacyVersionTable(t *testing.T) {
	db := openTestDB(t)
	_, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-01-01T00:00:00Z')`)
	require.NoError(t, err)

	set := Set{
		Prefix: "session", FS: fstest.MapFS{
			"0001_table.sql": {Data: []byte(`CREATE TABLE t (id TEXT PRIMARY KEY);`)},
			// A replayed version 2 would fail on the existing table.
			"0002_unique.sql": {Data: []byte(`CREATE UNIQUE INDEX t_id ON t (id);`)},
		},
		LegacyVersionTable: "schema_migrations",
	}
	require.NoError(t, Apply(db, set))
	assert.Equal(t, []int{1, 2}, appliedVersions(t, db, "session"))
	var indexCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='t_id'`).Scan(&indexCount))
	assert.Equal(t, 1, indexCount, "the pending migration must have run")
	// The legacy table stays: a rollback to the previous binary still needs it.
	var legacy int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&legacy))
	assert.Equal(t, 1, legacy)
}

// Projections written before versioning have no version table at all: the
// InitialSchema tables are the only evidence that version 1 already ran.
func TestApplyAdoptsInitialSchemaWithoutAVersionTable(t *testing.T) {
	db := openTestDB(t)
	_, err := db.Exec(`CREATE TABLE p (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	set := Set{
		Prefix: "catalog",
		FS: fstest.MapFS{
			// Replaying this would fail: the table already exists.
			"0001_initial.sql": {Data: []byte(`CREATE TABLE p (id TEXT PRIMARY KEY);`)},
			"0002_column.sql":  {Data: []byte(`ALTER TABLE p ADD COLUMN name TEXT;`)},
		},
		InitialSchemaTables: []string{"p"},
	}
	require.NoError(t, Apply(db, set))
	assert.Equal(t, []int{1, 2}, appliedVersions(t, db, "catalog"))
	var columns int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('p') WHERE name='name'`).Scan(&columns))
	assert.Equal(t, 1, columns, "the pending migration must have run")
}

// A partially created schema is not a pre-versioning database: adopting it would
// hide the missing tables, so the migration runs and fails loudly instead.
func TestApplyDoesNotAdoptAPartiallyCreatedInitialSchema(t *testing.T) {
	db := openTestDB(t)
	_, err := db.Exec(`CREATE TABLE p (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	set := Set{
		Prefix: "catalog",
		FS: fstest.MapFS{
			// The named tables do not all exist, so this is not adopted and the
			// replay collides with the table that does.
			"0001_initial.sql": {Data: []byte(`CREATE TABLE p (id TEXT PRIMARY KEY);`)},
		},
		InitialSchemaTables: []string{"p", "q"},
	}
	require.ErrorContains(t, Apply(db, set), "catalog migration 1")
	assert.Empty(t, appliedVersions(t, db, "catalog"))
}

func TestApplyRejectsASetWithoutAPrefix(t *testing.T) {
	db := openTestDB(t)
	require.ErrorContains(t, Apply(db, Set{FS: fstest.MapFS{
		"0001_table.sql": {Data: []byte(`CREATE TABLE t (id TEXT PRIMARY KEY);`)},
	}}), "migration set has no prefix")
}
