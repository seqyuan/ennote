package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

// CanonicalWorkspaceRoot resolves a workspace host path to its canonical form:
// Abs → EvalSymlinks → Clean → Stat(directory). This is the single entry point
// used by initial runs, resume verification, Manager, hooks, and project context.
func CanonicalWorkspaceRoot(hostPath string) (string, error) {
	abs, err := filepath.Abs(hostPath)
	if err != nil {
		return "", fmt.Errorf("workspace root: abs %s: %w", hostPath, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspace root: evalsymlinks %s: %w", abs, err)
	}
	clean := filepath.Clean(resolved)
	fi, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("workspace root: stat %s: %w", clean, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("workspace root is not a directory: %s", clean)
	}
	return clean, nil
}
