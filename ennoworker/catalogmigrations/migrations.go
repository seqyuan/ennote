package catalogmigrations

import (
	"database/sql"
	"embed"

	"github.com/seqyuan/ennote/ennoworker/internal/sqlmigrations"
)

//go:embed *.sql
var FS embed.FS

// Set is the catalog projection history. Catalog databases written before
// versioning existed carry the InitialSchema tables and no version table at all:
// InitialSchemaTables lets the applier adopt that state instead of replaying the
// schema on a populated database.
func Set() sqlmigrations.Set {
	return sqlmigrations.Set{Prefix: "catalog", FS: FS, InitialSchemaTables: []string{
		"project_summaries", "session_summaries", "owner_index",
		"attention_summaries", "resource_diagnostics", "applied_projection_events",
	}}
}

// Apply brings one catalog projection database to the current schema.
func Apply(db *sql.DB) error {
	return sqlmigrations.Apply(db, Set())
}
