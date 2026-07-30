package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
)

func TestDelimitedPreviewHonorsQuotesAndBounds(t *testing.T) {
	var source strings.Builder
	source.WriteString("name,description")
	for column := 3; column <= 31; column++ {
		fmt.Fprintf(&source, ",c%d", column)
	}
	source.WriteByte('\n')
	for row := 0; row < 101; row++ {
		fmt.Fprintf(&source, "%d,\"line %d, quoted\"", row, row)
		for column := 3; column <= 31; column++ {
			fmt.Fprintf(&source, ",%d", column)
		}
		source.WriteByte('\n')
	}
	preview, err := parseDelimitedPreview(strings.NewReader(source.String()), ',', 100, 30)
	require.NoError(t, err)
	assert.Len(t, preview.Columns, 30)
	assert.Len(t, preview.Rows, 100)
	assert.Equal(t, "line 0, quoted", preview.Rows[0][1])
	assert.True(t, preview.TruncatedRows)
	assert.True(t, preview.TruncatedColumns)
}

func TestServicePreviewsCSVTSVAndXLSX(t *testing.T) {
	service := setupArtifactService(t)
	ctx := context.Background()
	csvArtifact, err := service.Store(ctx, PublishInput{ProjectID: "p", SessionID: "s", Name: "results.csv"},
		strings.NewReader("gene,value\nA,1\nB,2\n"))
	require.NoError(t, err)
	_, csvPreview, err := service.PreviewTable(ctx, csvArtifact.ID, "s", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"gene", "value"}, csvPreview.Columns)
	assert.Equal(t, [][]string{{"A", "1"}, {"B", "2"}}, csvPreview.Rows)

	bomArtifact, err := service.Store(ctx, PublishInput{ProjectID: "p", SessionID: "s", Name: "bom.csv"},
		bytes.NewReader(append([]byte{0xef, 0xbb, 0xbf}, []byte("gene,value\nA,1\n")...)))
	require.NoError(t, err)
	_, bomPreview, err := service.PreviewTable(ctx, bomArtifact.ID, "s", "")
	require.NoError(t, err)
	assert.Equal(t, "gene", bomPreview.Columns[0])

	tsvArtifact, err := service.Store(ctx, PublishInput{ProjectID: "p", SessionID: "s", Name: "results.tsv"},
		strings.NewReader("gene\tvalue\nA\t1\n"))
	require.NoError(t, err)
	_, tsvPreview, err := service.PreviewTable(ctx, tsvArtifact.ID, "s", "")
	require.NoError(t, err)
	assert.Equal(t, "tsv", tsvPreview.Format)

	book := excelize.NewFile()
	defer func() { _ = book.Close() }()
	first := book.GetSheetName(0)
	require.NoError(t, book.SetCellValue(first, "A1", "gene"))
	require.NoError(t, book.SetCellValue(first, "B1", "value"))
	require.NoError(t, book.SetCellValue(first, "A2", "A"))
	require.NoError(t, book.SetCellValue(first, "B2", 7))
	_, err = book.NewSheet("Second")
	require.NoError(t, err)
	require.NoError(t, book.SetCellValue("Second", "A1", "sample"))
	require.NoError(t, book.SetCellValue("Second", "A2", "S1"))
	var encoded bytes.Buffer
	require.NoError(t, book.Write(&encoded))
	xlsxArtifact, err := service.Store(ctx, PublishInput{ProjectID: "p", SessionID: "s", Name: "results.xlsx"}, bytes.NewReader(encoded.Bytes()))
	require.NoError(t, err)
	_, xlsxPreview, err := service.PreviewTable(ctx, xlsxArtifact.ID, "s", "Second")
	require.NoError(t, err)
	assert.Equal(t, []string{first, "Second"}, xlsxPreview.Sheets)
	assert.Equal(t, "Second", xlsxPreview.Sheet)
	assert.Equal(t, [][]string{{"S1"}}, xlsxPreview.Rows)
}

func TestStaticHTMLPreviewRemovesActiveContent(t *testing.T) {
	service := setupArtifactService(t)
	artifact, err := service.Store(context.Background(), PublishInput{ProjectID: "p", SessionID: "s", Name: "report.html"},
		strings.NewReader(`<h1 onclick="alert(1)">Report</h1><script>parent.pwned=true</script><form action="https://example.test"><input></form><img src="https://example.test/x.png"><img src="data:image/png;base64,AA==">`))
	require.NoError(t, err)
	_, preview, err := service.PreviewHTML(context.Background(), artifact.ID, "s")
	require.NoError(t, err)
	assert.Contains(t, preview, "Report")
	assert.NotContains(t, preview, "script")
	assert.NotContains(t, preview, "onclick")
	assert.NotContains(t, preview, "form")
	assert.NotContains(t, preview, "https://example.test")
	assert.Contains(t, preview, "data:image/png")
}

func TestArtifactServiceSerializesQuotaAndReconcilesOrphans(t *testing.T) {
	service := setupArtifactService(t)
	service.MaxProjectBytes = 100
	var successes int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := service.Store(context.Background(), PublishInput{ProjectID: "p", SessionID: "s",
				Name: fmt.Sprintf("concurrent-%d.bin", index)}, strings.NewReader(strings.Repeat("x", 60)))
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(index)
	}
	wait.Wait()
	assert.Equal(t, 1, successes)

	orphan := filepath.Join(service.Root, "blobs", "ff", "orphan")
	require.NoError(t, os.MkdirAll(filepath.Dir(orphan), 0o700))
	require.NoError(t, os.WriteFile(orphan, []byte("orphan"), 0o600))
	pendingDir := filepath.Join(service.Root, ".pending")
	require.NoError(t, os.MkdirAll(pendingDir, 0o700))
	pending := filepath.Join(pendingDir, ".artifact-stale")
	require.NoError(t, os.WriteFile(pending, []byte("pending"), 0o600))
	old := time.Now().Add(-25 * time.Hour)
	require.NoError(t, os.Chtimes(pending, old, old))
	require.NoError(t, service.Reconcile(context.Background()))
	_, err := os.Stat(orphan)
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(pending)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestXLSXArchiveRejectsExtremeCompressionRatio(t *testing.T) {
	var encoded bytes.Buffer
	archive := zip.NewWriter(&encoded)
	entry, err := archive.Create("[Content_Types].xml")
	require.NoError(t, err)
	_, err = entry.Write([]byte(strings.Repeat("x", 1<<20)))
	require.NoError(t, err)
	entry, err = archive.Create("xl/workbook.xml")
	require.NoError(t, err)
	_, err = entry.Write([]byte("workbook"))
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	assert.ErrorContains(t, validateXLSXArchive(encoded.Bytes()), "compression ratio")
}

func TestArtifactServiceRejectsQuotaAndDetectsCorruption(t *testing.T) {
	service := setupArtifactService(t)
	service.MaxProjectBytes = 5
	_, err := service.Store(context.Background(), PublishInput{ProjectID: "p", SessionID: "s", Name: "large.bin"}, strings.NewReader("123456"))
	assert.ErrorIs(t, err, ErrArtifactQuota)

	service.MaxProjectBytes = 100
	artifact, err := service.Store(context.Background(), PublishInput{ProjectID: "p", SessionID: "s", Name: "data.bin"}, strings.NewReader("1234"))
	require.NoError(t, err)
	assert.False(t, strings.HasPrefix(artifact.StoragePath, service.Root), "new rows store a managed-root-relative key")
	path, err := service.resolveStoragePath(artifact.StoragePath)
	require.NoError(t, err)
	require.NoError(t, osWriteFile(path, []byte("xxxx")))
	_, _, err = service.ReadForSession(context.Background(), artifact.ID, "s")
	assert.ErrorIs(t, err, ErrArtifactCorrupt)
	_, _, err = service.ReadForSession(context.Background(), artifact.ID, "other")
	assert.ErrorIs(t, err, ErrArtifactNotFound)
}

func setupArtifactService(t *testing.T) *Service {
	t.Helper()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	now := "2026-07-28T00:00:00Z"
	_, err = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','project',?,?),('other-project','other',?,?);
		INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('s','p',?,?),('other','other-project',?,?)`,
		now, now, now, now, now, now, now, now)
	require.NoError(t, err)
	return &Service{DB: db, Root: t.TempDir()}
}

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
