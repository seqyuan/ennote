package usagemigrations

import (
	"database/sql"
	"embed"

	"github.com/seqyuan/ennote/ennoworker/internal/sqlmigrations"
)

//go:embed *.sql
var FS embed.FS

// Set is the usage projection history. Usage databases written before
// versioning existed carry the InitialSchema tables and no version table at all:
// InitialSchemaTables lets the applier adopt that state instead of replaying the
// schema on a populated database.
func Set() sqlmigrations.Set {
	return sqlmigrations.Set{Prefix: "usage", FS: FS, InitialSchemaTables: []string{
		"usage_aggregates", "applied_projection_events",
	}}
}

// Apply brings one usage projection database to the current schema.
func Apply(db *sql.DB) error {
	return sqlmigrations.Apply(db, Set())
}
