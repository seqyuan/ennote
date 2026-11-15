package workspace

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEvidenceFixture(t *testing.T) (agentflow.AcceptanceEvidence, string) {
	t.Helper()
	root := t.TempDir()
	jail, err := NewJail(root)
	require.NoError(t, err)
	return NewAcceptanceEvidence(jail), root
}

// A path outside the workspace is out of scope, never a verdict. The evaluator
// maps this error to unverified.
func TestAcceptanceEvidenceRejectsOutOfScopePaths(t *testing.T) {
	evidence, _ := newEvidenceFixture(t)
	for _, path := range []string{"../outside.txt", "/etc/passwd", "/workspace/../../etc/passwd"} {
		_, statErr := evidence.Stat(path)
		assert.True(t, errors.Is(statErr, agentflow.ErrEvidenceOutOfScope), "%s Stat: %v", path, statErr)
		_, openErr := evidence.Open(path)
		assert.True(t, errors.Is(openErr, agentflow.ErrEvidenceOutOfScope), "%s Open: %v", path, openErr)
	}
}

// A symlink that only leaves the jail after resolution is out of scope: the
// jail check runs on the canonical path, not on the link name.
func TestAcceptanceEvidenceRejectsSymlinkEscape(t *testing.T) {
	evidence, root := newEvidenceFixture(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.csv"), []byte("a,b\n"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.csv"), filepath.Join(root, "link.csv")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))

	for _, path := range []string{"link.csv", "escape/secret.csv"} {
		_, err := evidence.Stat(path)
		assert.True(t, errors.Is(err, agentflow.ErrEvidenceOutOfScope), "%s: %v", path, err)
	}
	// The parent-directory escape is out of scope even though the leaf does not
	// exist: escape and missing must stay distinguishable.
	_, err := evidence.Stat("escape/absent.csv")
	assert.True(t, errors.Is(err, agentflow.ErrEvidenceOutOfScope), "%v", err)
}

// A FIFO must be reported as non-regular and must never be opened: opening one
// for reading blocks until a writer appears, which would hang the meta-Run.
func TestAcceptanceEvidenceReportsFIFOWithoutBlocking(t *testing.T) {
	evidence, root := newEvidenceFixture(t)
	fifo := filepath.Join(root, "outputs.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo is unavailable: %v", err)
	}

	done := make(chan agentflow.AcceptanceFile, 1)
	go func() {
		file, _ := evidence.Stat("outputs.fifo")
		done <- file
	}()
	select {
	case file := <-done:
		assert.False(t, file.Regular, "a FIFO is not a regular file")
		assert.False(t, file.Missing, "a FIFO exists")
	case <-time.After(2 * time.Second):
		t.Fatal("Stat blocked on a FIFO: a stat must never open the file")
	}

	openDone := make(chan struct{})
	go func() {
		defer close(openDone)
		reader, err := evidence.Open("outputs.fifo")
		if reader != nil {
			_ = reader.Close()
		}
		assert.Error(t, err, "Open must refuse a FIFO")
	}()
	select {
	case <-openDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Open blocked on a FIFO")
	}
}

// A missing file is a fact, not an error: exists has to report does_not_hold
// for it rather than unverified.
func TestAcceptanceEvidenceReportsMissingFile(t *testing.T) {
	evidence, root := newEvidenceFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "outputs"), 0o755))

	for _, path := range []string{"outputs/absent.csv", "/workspace/outputs/absent.csv"} {
		file, err := evidence.Stat(path)
		require.NoError(t, err, path)
		assert.True(t, file.Missing, "%s must be definitely missing", path)
		assert.False(t, file.Regular)
	}
}

// A missing parent directory is still inside the jail, so the file is
// definitely absent instead of unreadable.
func TestAcceptanceEvidenceReportsMissingParentDirectory(t *testing.T) {
	evidence, _ := newEvidenceFixture(t)
	file, err := evidence.Stat("outputs/2026/absent.csv")
	require.NoError(t, err)
	assert.True(t, file.Missing, "a missing parent inside the jail is a definite absence")
	assert.False(t, file.Regular)
}

// Stat reports the size without opening the file: a large artifact must be
// cheap to verify with exists / non-empty.
func TestAcceptanceEvidenceReportsLargeFileSizeWithoutOpening(t *testing.T) {
	evidence, root := newEvidenceFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "outputs"), 0o755))
	const size = 8 << 20
	require.NoError(t, os.WriteFile(filepath.Join(root, "outputs", "matrix.bin"), make([]byte, size), 0o644))

	file, err := evidence.Stat("outputs/matrix.bin")
	require.NoError(t, err)
	assert.False(t, file.Missing)
	assert.True(t, file.Regular)
	assert.Equal(t, int64(size), file.Size)
}

// Open is bounded by the caller's read budget, not by a hidden cap: the reader
// returns exactly what was asked for and the caller decides about truncation.
func TestAcceptanceEvidenceOpenIsBoundedByTheReader(t *testing.T) {
	evidence, root := newEvidenceFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "outputs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "outputs", "qc.csv"),
		[]byte("sample,reads,dup_rate\ns1,10,0.1\n"), 0o644))

	reader, err := evidence.Open("outputs/qc.csv")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	header := make([]byte, 3)
	_, err = io.ReadFull(reader, header)
	require.NoError(t, err)
	assert.Equal(t, "sam", string(header))
}
