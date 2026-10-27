package store

import (
	"database/sql"

	"github.com/seqyuan/ennote/ennoworker/sessionmigrations"
)

// MigrateSession applies the Session schema migrations (V2). It is the single
// authority for the per-Session database shape and is used by the Session
// store manager and the store test fixtures. The applier lives in
// sessionmigrations so production and tests cannot diverge.
func MigrateSession(db *sql.DB) error {
	return sessionmigrations.Apply(db)
}
