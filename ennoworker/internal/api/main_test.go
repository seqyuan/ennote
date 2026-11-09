package api

import (
	"fmt"
	"os"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore/sessionstoretest"
)

// TestMain installs the migrated Session database template: every test here
// creates a Session, and replaying the migration chain for each one costs more
// than the test itself (see sessionstoretest).
func TestMain(m *testing.M) {
	cleanup, err := sessionstoretest.Use()
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare session database template: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
