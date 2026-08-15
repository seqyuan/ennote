package store

import (
	"database/sql"
)

// MigrateFixtureSchema migrates a database to the production Session schema
// (sessionmigrations). The legacy global fixture tables (projects, provider/
// model/agent/flow/MCP definitions, settings, policies, graph builder, skill
// roots) were removed in V2: production Sessions never contained them, and the
// test fixture snapshot is gone.
func MigrateFixtureSchema(db *sql.DB) error {
	return MigrateSession(db)
}
