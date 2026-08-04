package store_test

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleIdentityVersionMigrationPreservesLegacyProfilesAndEnforcesInvariants(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, migration := range migrations.Sorted() {
		if migration.Version > 18 {
			break
		}
		_, err = db.Exec(migration.SQL)
		require.NoError(t, err, "migration %d", migration.Version)
		_, err = db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?, '2026-08-03T00:00:00Z')`, migration.Version)
		require.NoError(t, err)
	}
	const now = "2026-08-03T00:00:00Z"
	_, err = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('project','Project',?,?);
		INSERT INTO agent_profiles(id,name,created_at,updated_at) VALUES('legacy-host','Legacy Host',?,?)`, now, now, now, now)
	require.NoError(t, err)

	var roleMigration migrations.Migration
	for _, migration := range migrations.Sorted() {
		if migration.Version == 19 {
			roleMigration = migration
			break
		}
	}
	require.Equal(t, 19, roleMigration.Version)
	_, err = db.Exec(roleMigration.SQL)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(19, '2026-08-03T00:00:01Z')`)
	require.NoError(t, err)
	var kind string
	require.NoError(t, db.QueryRow(`SELECT object_kind FROM agent_profiles WHERE id='legacy-host'`).Scan(&kind))
	assert.Equal(t, "host_profile", kind)
	var writer string
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key='hosted_commit_format_version'`).Scan(&writer))
	assert.Equal(t, "1", writer, "Role schema migration must not activate the format-2 writer")

	require.NoError(t, store.Migrate(db))
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key='hosted_commit_format_version'`).Scan(&writer))
	assert.Equal(t, "2", writer, "migration 20 activates the qualified speaker-ledger writer")
	_, err = db.Exec(`UPDATE settings SET value='3' WHERE key='hosted_commit_format_version'`)
	assert.ErrorContains(t, err, "hosted_commit_format_setting_invalid")

	_, err = db.Exec(`INSERT INTO agent_profiles(id,name,object_kind,handle,scope,project_id,draft_json,draft_revision,created_at,updated_at)
		VALUES('role-a','Reviewer','role','security-reviewer','project','project','{}',0,?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_profiles(id,name,object_kind,handle,scope,project_id,draft_json,draft_revision,created_at,updated_at)
		VALUES('role-invalid','Invalid','role','Bad Handle','project','project','{}',0,?,?)`, now, now)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO agent_profiles(id,name,object_kind,handle,scope,project_id,draft_json,draft_revision,created_at,updated_at)
		VALUES('role-duplicate','Duplicate','role','security-reviewer','project','project','{}',0,?,?)`, now, now)
	require.Error(t, err)

	const definition = `{"schemaVersion":1,"rolePrompt":"Review evidence."}`
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err = db.Exec(`INSERT INTO agent_profile_versions(id,agent_profile_id,version,definition_json,config_digest,status,created_at)
		VALUES('version-a','role-a',1,?,?,'published',?)`, definition, digest, now)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE agent_profiles SET current_version_id='version-a' WHERE id='role-a'`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO agent_profiles(id,name,object_kind,handle,scope,project_id,draft_json,draft_revision,created_at,updated_at)
		VALUES('role-b','Second','role','second-reviewer','project','project','{}',0,?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE agent_profiles SET current_version_id='version-a' WHERE id='role-b'`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE agent_profile_versions SET definition_json='{}' WHERE id='version-a'`)
	require.Error(t, err)
	_, err = db.Exec(`DELETE FROM agent_profile_versions WHERE id='version-a'`)
	require.Error(t, err)

	_, err = db.Exec(`UPDATE agent_profiles SET status='archived' WHERE id='role-a'`)
	require.NoError(t, err)
	var versions int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM agent_profile_versions WHERE agent_profile_id='role-a'`).Scan(&versions))
	assert.Equal(t, 1, versions)
}

func TestRoleMigrationAddsExpectedSchema(t *testing.T) {
	db := store.SetupDB(t)
	for _, column := range []string{
		"object_kind", "handle", "scope", "project_id", "icon", "color", "positioning",
		"draft_json", "draft_revision", "current_version_id", "delegation_enabled",
		"delegation_revocation_epoch", "delegation_disabled_at",
	} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_profiles') WHERE name=?`, column).Scan(&count))
		assert.Equal(t, 1, count, column)
	}
	var table int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_profile_versions'`).Scan(&table))
	assert.Equal(t, 1, table)
}
