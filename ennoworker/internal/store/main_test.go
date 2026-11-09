package store

import (
	"fmt"
	"os"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
)

// TestMain points the session manager at the migrated fixture template, so the
// tests that drive a real Manager copy a database instead of replaying the
// Session migration chain for every Session they create. The store package
// builds that template itself (ensureTemplate) and cannot import
// sessionstoretest, which imports this package.
func TestMain(m *testing.M) {
	template, err := ensureTemplate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare session database template: %v\n", err)
		os.Exit(1)
	}
	sessionstore.SetMigratedTemplate(template)
	code := m.Run()
	sessionstore.SetMigratedTemplate("")
	os.Exit(code)
}
