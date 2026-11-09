package sessionstore

import (
	"errors"
	"fmt"
	"os"
)

// migratedTemplate, when set, is the path of an empty Session database with the
// full migration chain already applied. A Manager copies that file instead of
// replaying the chain for every new Session.
//
// It exists for tests. Replaying the chain costs seconds per database under the
// race detector — the suite creates one Session per test, so the migrations end
// up dominating the run — while a byte copy costs about a millisecond. This is
// the same trade internal/store's SetupDB makes for fixture databases; that
// comment carries the measurements. Production never sets a template, so
// Create and OpenSession migrate exactly as before.
var migratedTemplate string

// SetMigratedTemplate points the Manager at a pre-migrated, empty Session
// database, or clears the template when path is empty. It is a test seam: a
// caller that sets it is responsible for the file carrying every migration this
// build embeds, which sessionstoretest.Use guarantees.
func SetMigratedTemplate(path string) {
	migratedTemplate = path
}

// MigratedTemplatePath reports the installed template path, or "" when no
// template is installed. Tests use it to hand a migrated database to code that
// opens its own connection.
func MigratedTemplatePath() string {
	return migratedTemplate
}

// seedFromTemplate copies the pre-migrated database to databasePath when no
// database exists there yet. Templates are checkpointed before they are
// published, so the main file is complete and a byte copy cannot drop WAL rows.
func seedFromTemplate(databasePath string) error {
	if migratedTemplate == "" {
		return nil
	}
	if _, err := os.Stat(databasePath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("probe session database: %w", err)
	}
	data, err := os.ReadFile(migratedTemplate)
	if err != nil {
		return fmt.Errorf("read migrated session template: %w", err)
	}
	if err := os.WriteFile(databasePath, data, 0o600); err != nil {
		return fmt.Errorf("seed session database from template: %w", err)
	}
	return nil
}
