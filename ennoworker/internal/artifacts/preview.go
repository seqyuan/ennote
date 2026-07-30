package artifacts

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/xuri/excelize/v2"
)

const (
	DefaultPreviewRows             = 100
	DefaultPreviewColumns          = 30
	DefaultTextPreviewBytes        = 256 << 10
	maxXLSXSourceBytes             = int64(20 << 20)
	maxXLSXEntries                 = 10_000
	maxXLSXEntryBytes       uint64 = 100 << 20
	maxXLSXExpanded         uint64 = 200 << 20
	maxXLSXCompressionRatio uint64 = 100
)

type TablePreview struct {
	Format           string     `json:"format"`
	Sheets           []string   `json:"sheets,omitempty"`
	Sheet            string     `json:"sheet,omitempty"`
	Columns          []string   `json:"columns"`
	Rows             [][]string `json:"rows"`
	TruncatedRows    bool       `json:"truncatedRows"`
	TruncatedColumns bool       `json:"truncatedColumns"`
	RowLimit         int        `json:"rowLimit"`
	ColumnLimit      int        `json:"columnLimit"`
}

type TextPreview struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

func (s *Service) PreviewTable(ctx context.Context, artifactID, sessionID, sheet string) (*domain.Artifact, TablePreview, error) {
	artifact, data, err := s.ReadForSession(ctx, artifactID, sessionID)
	if err != nil {
		return nil, TablePreview{}, err
	}
	if artifact.Kind != domain.ArtifactKindTable {
		return artifact, TablePreview{}, ErrPreviewUnsupported
	}
	name := strings.ToLower(artifact.Name)
	switch {
	case strings.HasSuffix(name, ".csv"):
		preview, err := parseDelimitedPreview(bytes.NewReader(data), ',', DefaultPreviewRows, DefaultPreviewColumns)
		preview.Format = "csv"
		if err != nil {
			return artifact, preview, fmt.Errorf("%w: parse CSV preview: %v", ErrArtifactCorrupt, err)
		}
		return artifact, preview, nil
	case strings.HasSuffix(name, ".tsv"):
		preview, err := parseDelimitedPreview(bytes.NewReader(data), '\t', DefaultPreviewRows, DefaultPreviewColumns)
		preview.Format = "tsv"
		if err != nil {
			return artifact, preview, fmt.Errorf("%w: parse TSV preview: %v", ErrArtifactCorrupt, err)
		}
		return artifact, preview, nil
	case strings.HasSuffix(name, ".xlsx"):
		preview, err := parseXLSXPreview(data, sheet, DefaultPreviewRows, DefaultPreviewColumns)
		if err != nil {
			return artifact, preview, fmt.Errorf("%w: parse XLSX preview: %v", ErrArtifactCorrupt, err)
		}
		return artifact, preview, nil
	default:
		return artifact, TablePreview{}, ErrPreviewUnsupported
	}
}

func (s *Service) PreviewText(ctx context.Context, artifactID, sessionID string) (*domain.Artifact, TextPreview, error) {
	artifact, data, err := s.ReadForSession(ctx, artifactID, sessionID)
	if err != nil {
		return nil, TextPreview{}, err
	}
	if artifact.Kind != domain.ArtifactKindText {
		return artifact, TextPreview{}, ErrPreviewUnsupported
	}
	if !utf8.Valid(data) {
		return artifact, TextPreview{}, ErrArtifactCorrupt
	}
	if len(data) <= DefaultTextPreviewBytes {
		return artifact, TextPreview{Text: string(data)}, nil
	}
	end := DefaultTextPreviewBytes
	for end > 0 && !utf8.Valid(data[:end]) {
		end--
	}
	return artifact, TextPreview{Text: string(data[:end]), Truncated: true}, nil
}

func (s *Service) PreviewHTML(ctx context.Context, artifactID, sessionID string) (*domain.Artifact, string, error) {
	artifact, data, err := s.ReadForSession(ctx, artifactID, sessionID)
	if err != nil {
		return nil, "", err
	}
	if artifact.Kind != domain.ArtifactKindStaticHTML || !utf8.Valid(data) {
		return artifact, "", ErrPreviewUnsupported
	}
	return artifact, sanitizeStaticHTML(string(data)), nil
}

func parseDelimitedPreview(source io.Reader, delimiter rune, rowLimit, columnLimit int) (TablePreview, error) {
	if rowLimit <= 0 || columnLimit <= 0 {
		return TablePreview{}, errors.New("preview limits must be positive")
	}
	buffered := bufio.NewReader(source)
	if prefix, err := buffered.Peek(3); err == nil && bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = buffered.Discard(3)
	}
	reader := csv.NewReader(buffered)
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = false
	preview := TablePreview{RowLimit: rowLimit, ColumnLimit: columnLimit}
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return preview, nil
	}
	if err != nil {
		return preview, err
	}
	preview.TruncatedColumns = len(header) > columnLimit
	preview.Columns = normalizedCells(header, columnLimit)
	for len(preview.Rows) <= rowLimit {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return preview, err
		}
		if len(record) > columnLimit {
			preview.TruncatedColumns = true
		}
		if len(preview.Rows) == rowLimit {
			preview.TruncatedRows = true
			break
		}
		preview.Rows = append(preview.Rows, normalizedCells(record, columnLimit))
	}
	return preview, nil
}

func parseXLSXPreview(data []byte, requestedSheet string, rowLimit, columnLimit int) (TablePreview, error) {
	if err := validateXLSXArchive(data); err != nil {
		return TablePreview{}, err
	}
	book, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{RawCellValue: true})
	if err != nil {
		return TablePreview{}, err
	}
	defer func() { _ = book.Close() }()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		return TablePreview{}, errors.New("workbook has no worksheets")
	}
	sheet := requestedSheet
	if sheet == "" {
		sheet = sheets[0]
	}
	found := false
	for _, value := range sheets {
		if value == sheet {
			found = true
			break
		}
	}
	if !found {
		return TablePreview{}, fmt.Errorf("worksheet not found")
	}
	rows, err := book.Rows(sheet)
	if err != nil {
		return TablePreview{}, err
	}
	defer func() { _ = rows.Close() }()
	preview := TablePreview{Format: "xlsx", Sheets: sheets, Sheet: sheet,
		RowLimit: rowLimit, ColumnLimit: columnLimit}
	if !rows.Next() {
		return preview, rows.Error()
	}
	header, err := rows.Columns()
	if err != nil {
		return preview, err
	}
	preview.TruncatedColumns = len(header) > columnLimit
	preview.Columns = normalizedCells(header, columnLimit)
	for rows.Next() {
		record, err := rows.Columns()
		if err != nil {
			return preview, err
		}
		if len(record) > columnLimit {
			preview.TruncatedColumns = true
		}
		if len(preview.Rows) == rowLimit {
			preview.TruncatedRows = true
			break
		}
		preview.Rows = append(preview.Rows, normalizedCells(record, columnLimit))
	}
	if err := rows.Error(); err != nil {
		return preview, err
	}
	return preview, nil
}

func normalizedCells(values []string, limit int) []string {
	width := len(values)
	if width > limit {
		width = limit
	}
	result := make([]string, width)
	copy(result, values[:width])
	return result
}

func validateXLSXArchive(data []byte) error {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return errors.New("invalid XLSX ZIP container")
	}
	if len(archive.File) == 0 || len(archive.File) > maxXLSXEntries {
		return errors.New("XLSX entry count exceeds policy")
	}
	var expanded uint64
	hasTypes, hasWorkbook := false, false
	for _, entry := range archive.File {
		if entry.UncompressedSize64 > 0 && (entry.CompressedSize64 == 0 ||
			entry.UncompressedSize64 > entry.CompressedSize64*maxXLSXCompressionRatio) {
			return errors.New("XLSX compression ratio exceeds policy")
		}
		if entry.UncompressedSize64 > maxXLSXEntryBytes || expanded > maxXLSXExpanded-entry.UncompressedSize64 {
			return errors.New("XLSX expanded size exceeds policy")
		}
		expanded += entry.UncompressedSize64
		switch entry.Name {
		case "[Content_Types].xml":
			hasTypes = true
		case "xl/workbook.xml":
			hasWorkbook = true
		}
	}
	if !hasTypes || !hasWorkbook {
		return errors.New("XLSX container is missing required workbook entries")
	}
	return nil
}

func sanitizeStaticHTML(input string) string {
	policy := bluemonday.NewPolicy()
	policy.AllowElements(
		"html", "head", "body", "title", "style", "main", "section", "article", "header", "footer",
		"div", "span", "p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "strong", "b", "em", "i",
		"small", "sub", "sup", "pre", "code", "blockquote", "ul", "ol", "li", "dl", "dt", "dd",
		"table", "caption", "thead", "tbody", "tfoot", "tr", "th", "td", "colgroup", "col", "figure", "figcaption", "img",
	)
	policy.AllowAttrs("class", "id", "style", "title", "role", "aria-label", "aria-hidden").Globally()
	policy.AllowAttrs("colspan", "rowspan", "scope", "headers").OnElements("th", "td")
	policy.AllowAttrs("span", "width").OnElements("col", "colgroup")
	policy.AllowAttrs("src", "alt", "width", "height").OnElements("img")
	policy.AllowDataURIImages()
	clean := policy.Sanitize(input)
	return "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">" +
		"<style>html{color-scheme:light}body{margin:0;padding:16px;background:#fff;color:#111827;font:14px system-ui,sans-serif;overflow-wrap:anywhere}" +
		"img{max-width:100%;height:auto}table{border-collapse:collapse;max-width:100%}th,td{border:1px solid #d1d5db;padding:4px 6px;text-align:left}</style>" +
		"</head><body>" + clean + "</body></html>"
}
