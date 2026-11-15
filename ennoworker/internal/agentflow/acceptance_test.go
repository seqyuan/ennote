package agentflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAcceptanceEvidence is the test seam: content-backed files by default, with
// explicit stat/open overrides for the interesting failure shapes (FIFO, missing
// file, unreadable path, out-of-scope path).
type fakeAcceptanceEvidence struct {
	content map[string][]byte
	stat    map[string]AcceptanceFile
	statErr map[string]error
	openErr map[string]error
	openFn  func(rel string) (io.ReadCloser, error)
	opened  []string
}

func (f *fakeAcceptanceEvidence) Stat(rel string) (AcceptanceFile, error) {
	if err, ok := f.statErr[rel]; ok {
		return AcceptanceFile{}, err
	}
	if file, ok := f.stat[rel]; ok {
		return file, nil
	}
	if data, ok := f.content[rel]; ok {
		return AcceptanceFile{Size: int64(len(data)), Regular: true}, nil
	}
	return AcceptanceFile{Missing: true}, nil
}

func (f *fakeAcceptanceEvidence) Open(rel string) (io.ReadCloser, error) {
	f.opened = append(f.opened, rel)
	if f.openFn != nil {
		return f.openFn(rel)
	}
	if err, ok := f.openErr[rel]; ok {
		return nil, err
	}
	data, ok := f.content[rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func acceptanceCase(t *testing.T, spec domain.FlowAcceptance, evidence *fakeAcceptanceEvidence) AcceptanceCheck {
	t.Helper()
	verdict := EvaluateAcceptance(context.Background(), []domain.FlowAcceptance{spec}, evidence, nil)
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, AggregateAcceptance(verdict.Checks), verdict.Result)
	return verdict.Checks[0]
}

func TestEvaluateAcceptanceExists(t *testing.T) {
	tests := []struct {
		name   string
		spec   domain.FlowAcceptance
		ev     *fakeAcceptanceEvidence
		want   domain.AcceptanceResult
		code   string
		opened bool
	}{
		{
			name: "regular file holds",
			spec: domain.FlowAcceptance{File: "outputs/report.md", Check: "exists"},
			ev:   &fakeAcceptanceEvidence{content: map[string][]byte{"outputs/report.md": []byte("x")}},
			want: domain.AcceptanceHolds,
		},
		{
			name: "empty regular file still exists",
			spec: domain.FlowAcceptance{File: "outputs/report.md", Check: "exists"},
			ev:   &fakeAcceptanceEvidence{content: map[string][]byte{"outputs/report.md": nil}},
			want: domain.AcceptanceHolds,
		},
		{
			name: "missing file does not hold",
			spec: domain.FlowAcceptance{File: "outputs/report.md", Check: "exists"},
			ev:   &fakeAcceptanceEvidence{},
			want: domain.AcceptanceDoesNotHold,
			code: "file_missing",
		},
		{
			name: "directory is not a file",
			spec: domain.FlowAcceptance{File: "outputs", Check: "exists"},
			ev: &fakeAcceptanceEvidence{stat: map[string]AcceptanceFile{
				"outputs": {Size: 4096, Regular: false},
			}},
			want:   domain.AcceptanceDoesNotHold,
			code:   "evidence_not_a_file",
			opened: false,
		},
		{
			name: "fifo is refused without opening",
			spec: domain.FlowAcceptance{File: "outputs/pipe", Check: "exists"},
			ev: &fakeAcceptanceEvidence{stat: map[string]AcceptanceFile{
				"outputs/pipe": {Size: 0, Regular: false},
			}},
			want:   domain.AcceptanceDoesNotHold,
			code:   "evidence_not_a_file",
			opened: false,
		},
		{
			name: "unreadable evidence is unverified",
			spec: domain.FlowAcceptance{File: "outputs/report.md", Check: "exists"},
			ev:   &fakeAcceptanceEvidence{statErr: map[string]error{"outputs/report.md": os.ErrPermission}},
			want: domain.AcceptanceUnverified,
			code: "evidence_unreadable",
		},
		{
			name: "out of scope evidence is unverified",
			spec: domain.FlowAcceptance{File: "../etc/passwd", Check: "exists"},
			ev:   &fakeAcceptanceEvidence{statErr: map[string]error{"../etc/passwd": ErrEvidenceOutOfScope}},
			want: domain.AcceptanceUnverified,
			code: "evidence_out_of_scope",
		},
		{
			name: "deadline is unverified never does_not_hold",
			spec: domain.FlowAcceptance{File: "outputs/report.md", Check: "exists"},
			ev:   &fakeAcceptanceEvidence{statErr: map[string]error{"outputs/report.md": context.DeadlineExceeded}},
			want: domain.AcceptanceUnverified,
			code: "evidence_timeout",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := acceptanceCase(t, tc.spec, tc.ev)
			assert.Equal(t, tc.want, check.Verdict)
			assert.Equal(t, tc.code, check.Code)
			assert.Equal(t, tc.spec.Check, check.Check)
			if !tc.opened {
				assert.Empty(t, tc.ev.opened, "a non-regular file must never be opened")
			}
		})
	}
}

func TestEvaluateAcceptanceNonEmpty(t *testing.T) {
	tests := []struct {
		name string
		ev   *fakeAcceptanceEvidence
		want domain.AcceptanceResult
		code string
	}{
		{
			name: "non-empty holds",
			ev:   &fakeAcceptanceEvidence{content: map[string][]byte{"outputs/qc.json": []byte("{}")}},
			want: domain.AcceptanceHolds,
		},
		{
			name: "empty file does not hold",
			ev:   &fakeAcceptanceEvidence{content: map[string][]byte{"outputs/qc.json": nil}},
			want: domain.AcceptanceDoesNotHold,
			code: "file_empty",
		},
		{
			name: "missing file does not hold",
			ev:   &fakeAcceptanceEvidence{},
			want: domain.AcceptanceDoesNotHold,
			code: "file_missing",
		},
		{
			name: "fifo refused without opening",
			ev: &fakeAcceptanceEvidence{stat: map[string]AcceptanceFile{
				"outputs/qc.json": {Regular: false},
			}},
			want: domain.AcceptanceDoesNotHold,
			code: "evidence_not_a_file",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := acceptanceCase(t, domain.FlowAcceptance{File: "outputs/qc.json", Check: "non-empty"}, tc.ev)
			assert.Equal(t, tc.want, check.Verdict)
			assert.Equal(t, tc.code, check.Code)
			assert.Empty(t, tc.ev.opened)
		})
	}
}

func TestEvaluateAcceptanceCSVColumns(t *testing.T) {
	tests := []struct {
		name    string
		columns []string
		content []byte
		stat    *AcceptanceFile
		want    domain.AcceptanceResult
		code    string
	}{
		{
			name:    "columns in different order hold",
			columns: []string{"dup_rate", "sample", "reads"},
			content: []byte("sample,reads,dup_rate\ns1,10,0.1\n"),
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "extra columns are allowed",
			columns: []string{"sample"},
			content: []byte("sample,reads,dup_rate,extra\n"),
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "UTF-8 BOM header holds",
			columns: []string{"sample", "reads"},
			content: append([]byte{0xEF, 0xBB, 0xBF}, []byte("sample,reads\n1,2\n")...),
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "double quoted field with embedded newline holds",
			columns: []string{"sam\nple", "reads"},
			content: []byte("\"sam\nple\",reads\n1,2\n"),
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "escaped quotes hold",
			columns: []string{`sam"ple`, "reads"},
			content: []byte("\"sam\"\"ple\",reads\n"),
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "header without trailing newline holds",
			columns: []string{"sample", "reads"},
			content: []byte("sample,reads"),
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "header names match exactly",
			columns: []string{"Sample"},
			content: []byte("sample,reads\n"),
			want:    domain.AcceptanceDoesNotHold,
			code:    "csv_columns_missing",
		},
		{
			name:    "empty file does not hold",
			columns: []string{"sample"},
			content: []byte{},
			want:    domain.AcceptanceDoesNotHold,
			code:    "csv_header_invalid",
		},
		{
			name:    "missing declared column does not hold",
			columns: []string{"sample", "dup_rate"},
			content: []byte("sample,reads\n"),
			want:    domain.AcceptanceDoesNotHold,
			code:    "csv_columns_missing",
		},
		{
			name:    "empty column name is invalid",
			columns: []string{"sample"},
			content: []byte("sample,,reads\n"),
			want:    domain.AcceptanceDoesNotHold,
			code:    "csv_header_invalid",
		},
		{
			name:    "duplicate column name is invalid",
			columns: []string{"sample"},
			content: []byte("sample,reads,sample\n"),
			want:    domain.AcceptanceDoesNotHold,
			code:    "csv_header_invalid",
		},
		{
			name:    "invalid UTF-8 header is invalid",
			columns: []string{"sample"},
			content: []byte("sample,r\xffeads\n"),
			want:    domain.AcceptanceDoesNotHold,
			code:    "csv_header_invalid",
		},
		{
			name:    "unterminated quote inside the budget is invalid",
			columns: []string{"sample"},
			content: []byte("\"sample,reads\n"),
			want:    domain.AcceptanceDoesNotHold,
			code:    "csv_header_invalid",
		},
		{
			name:    "header over the 50000 byte budget is unverified",
			columns: []string{"sample"},
			content: []byte(strings.Repeat("a", AcceptanceCSVHeaderBudget+1)),
			want:    domain.AcceptanceUnverified,
			code:    "evidence_truncated",
		},
		{
			name:    "unterminated quote at the budget boundary is unverified",
			columns: []string{"sample"},
			content: []byte("\"" + strings.Repeat("a", AcceptanceCSVHeaderBudget+1)),
			want:    domain.AcceptanceUnverified,
			code:    "evidence_truncated",
		},
		{
			name:    "fifo refused without opening",
			columns: []string{"sample"},
			stat:    &AcceptanceFile{Regular: false},
			want:    domain.AcceptanceDoesNotHold,
			code:    "evidence_not_a_file",
		},
		{
			name:    "missing file does not hold",
			columns: []string{"sample"},
			content: nil,
			want:    domain.AcceptanceDoesNotHold,
			code:    "file_missing",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := &fakeAcceptanceEvidence{}
			if tc.stat != nil {
				ev.stat = map[string]AcceptanceFile{"outputs/qc.csv": *tc.stat}
			}
			if tc.content != nil {
				ev.content = map[string][]byte{"outputs/qc.csv": tc.content}
			}
			spec := domain.FlowAcceptance{File: "outputs/qc.csv", Check: "csv-columns", Columns: tc.columns}
			check := acceptanceCase(t, spec, ev)
			assert.Equal(t, tc.want, check.Verdict, "diagnostic: %+v", check)
			assert.Equal(t, tc.code, check.Code)
			if tc.code == "evidence_not_a_file" {
				assert.Empty(t, ev.opened)
			}
			assert.NotContains(t, check.Detail, "aaa", "detail must never echo file content")
		})
	}
}

func bigJSON(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString(`{"payload":"`)
	buf.WriteString(strings.Repeat("x", 1<<20))
	buf.WriteString(`"}`)
	return buf.Bytes()
}

func TestEvaluateAcceptanceJSONValid(t *testing.T) {
	// A multi-byte rune straddling the validating reader's chunk boundary.
	boundary := acceptanceJSONReadChunkBytes
	prefix := `{"a":"`
	straddle := append([]byte(prefix), []byte(strings.Repeat("x", boundary-len(prefix)-1))...)
	straddle = append(straddle, []byte("é")...)
	straddle = append(straddle, []byte(`"}`)...)

	// An invalid continuation byte straddling the same boundary.
	badBoundary := append([]byte(prefix), []byte(strings.Repeat("x", boundary-len(prefix)-1))...)
	badBoundary = append(badBoundary, 0xC3)
	badBoundary = append(badBoundary, 'A')
	badBoundary = append(badBoundary, []byte(`"}`)...)

	tests := []struct {
		name    string
		content []byte
		stat    *AcceptanceFile
		want    domain.AcceptanceResult
		code    string
	}{
		{
			name:    "valid object holds",
			content: []byte(`{"a":1,"b":[true,null,"x"]}`),
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "valid top-level array holds",
			content: []byte(`[1,2,{"a":3}]`),
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "valid JSON over 1 MiB holds under the 64 MiB read cap",
			content: bigJSON(t),
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "multi-byte rune across the chunk boundary holds",
			content: straddle,
			want:    domain.AcceptanceHolds,
		},
		{
			name:    "empty file does not hold",
			content: []byte{},
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name:    "BOM does not hold",
			content: append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"a":1}`)...),
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name:    "NaN does not hold",
			content: []byte(`{"a":NaN}`),
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name:    "Infinity does not hold",
			content: []byte(`{"a":Infinity}`),
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name:    "plain syntax error does not hold",
			content: []byte(`{a:1,}`),
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name:    "truncated JSON does not hold",
			content: []byte(`{"a":[1,2`),
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name:    "two JSON values do not hold",
			content: []byte(`{"a":1}{"b":2}`),
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name: "invalid UTF-8 inside a string does not hold",
			// encoding/json accepts this and substitutes U+FFFD; the evidence
			// reader must reject it explicitly.
			content: []byte(`{"a":"` + string([]byte{0xff, 0xfe}) + `"}`),
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name:    "trailing invalid UTF-8 after a complete value does not hold",
			content: []byte(`{"a":1}` + string([]byte{0xff})),
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name:    "invalid continuation across the chunk boundary does not hold",
			content: badBoundary,
			want:    domain.AcceptanceDoesNotHold,
			code:    "json_invalid",
		},
		{
			name: "fifo refused without opening",
			stat: &AcceptanceFile{Regular: false},
			want: domain.AcceptanceDoesNotHold,
			code: "evidence_not_a_file",
		},
		{
			name: "missing file does not hold",
			want: domain.AcceptanceDoesNotHold,
			code: "file_missing",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := &fakeAcceptanceEvidence{}
			if tc.stat != nil {
				ev.stat = map[string]AcceptanceFile{"outputs/qc.json": *tc.stat}
			}
			if tc.content != nil {
				ev.content = map[string][]byte{"outputs/qc.json": tc.content}
			}
			check := acceptanceCase(t, domain.FlowAcceptance{File: "outputs/qc.json", Check: "json-valid"}, ev)
			assert.Equal(t, tc.want, check.Verdict, "diagnostic: %+v", check)
			assert.Equal(t, tc.code, check.Code)
			if tc.code == "evidence_not_a_file" {
				assert.Empty(t, ev.opened)
			}
		})
	}
}

// TestEvaluateAcceptanceJSONEncodingCompat pins the finding behind the explicit
// UTF-8 validation: encoding/json alone accepts invalid UTF-8 in strings.
func TestEvaluateAcceptanceJSONEncodingCompat(t *testing.T) {
	invalid := []byte(`{"a":"` + string([]byte{0xff, 0xfe}) + `"}`)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(invalid, &decoded),
		"encoding/json accepts invalid UTF-8 by design; the evaluator must not rely on it")
	assert.NotEqual(t, domain.AcceptanceHolds,
		acceptanceCase(t, domain.FlowAcceptance{File: "x.json", Check: "json-valid"},
			&fakeAcceptanceEvidence{content: map[string][]byte{"x.json": invalid}}).Verdict)
}

// oneByteReader hands out the underlying bytes one at a time so multi-byte
// sequences are guaranteed to straddle read boundaries.
type oneByteReader struct {
	data []byte
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

// The UTF-8 gate must belong to the reader, not to encoding/json: a trailing
// invalid byte after a complete value is otherwise silently accepted.
func TestUTF8ValidatingReader(t *testing.T) {
	valid := []byte(`{"a":"é","b":[1,2]}`)
	got, err := io.ReadAll(newUTF8ValidatingReader(&oneByteReader{data: append([]byte(nil), valid...)}))
	require.NoError(t, err)
	assert.Equal(t, valid, got)

	tests := []struct {
		name  string
		input []byte
	}{
		{name: "invalid bytes inside a string", input: []byte(`{"a":"` + string([]byte{0xff, 0xfe}) + `"}`)},
		{name: "trailing invalid byte after a complete value", input: []byte(`{"a":1}` + string([]byte{0xff}))},
		{name: "invalid byte at the start", input: []byte(string([]byte{0xff}) + `{"a":1}`)},
		{name: "truncated sequence at EOF", input: []byte(`{"a":"` + string([]byte{0xc3}))},
		{name: "invalid continuation byte", input: []byte(`{"a":"` + string([]byte{0xc3}) + `A"}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := newUTF8ValidatingReader(&oneByteReader{data: append([]byte(nil), tc.input...)})
			_, err := io.ReadAll(reader)
			require.Error(t, err)
			assert.True(t, errors.Is(err, errAcceptanceInvalidUTF8), "want invalid-UTF-8 error, got %v", err)
			assert.True(t, reader.sawInvalidUTF8())
		})
	}
}

// slowJSONStream yields an endless valid JSON array one byte at a time so the
// per-leaf deadline can be exercised without a blocking filesystem.
type slowJSONStream struct {
	sent int
}

func (r *slowJSONStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	time.Sleep(200 * time.Microsecond)
	if r.sent == 0 {
		p[0] = '['
	} else if r.sent%2 == 1 {
		p[0] = '1'
	} else {
		p[0] = ','
	}
	r.sent++
	return 1, nil
}

func TestEvaluateAcceptanceDeadlineIsUnverified(t *testing.T) {
	previous := acceptanceLeafTimeout
	acceptanceLeafTimeout = 5 * time.Millisecond
	t.Cleanup(func() { acceptanceLeafTimeout = previous })

	ev := &fakeAcceptanceEvidence{
		stat: map[string]AcceptanceFile{"outputs/stream.json": {Size: 1, Regular: true}},
		openFn: func(rel string) (io.ReadCloser, error) {
			return io.NopCloser(&slowJSONStream{}), nil
		},
	}
	check := acceptanceCase(t, domain.FlowAcceptance{File: "outputs/stream.json", Check: "json-valid"}, ev)
	assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
	assert.Equal(t, "evidence_timeout", check.Code)
}

func TestEvaluateAcceptanceUnknownCheck(t *testing.T) {
	ev := &fakeAcceptanceEvidence{content: map[string][]byte{"outputs/x": []byte("x")}}
	check := acceptanceCase(t, domain.FlowAcceptance{File: "outputs/x", Check: "tests-passed"}, ev)
	assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
	assert.Equal(t, "evidence_check_unknown", check.Code)
	assert.Empty(t, ev.opened, "an unknown check must not touch the filesystem")
}

// TestEvaluateAcceptanceCSVReadFailure pins "truncated read" -> unverified: a
// header that could not be read is never a pass and never a failure.
func TestEvaluateAcceptanceCSVReadFailure(t *testing.T) {
	ev := &fakeAcceptanceEvidence{
		stat: map[string]AcceptanceFile{"outputs/qc.csv": {Size: 64, Regular: true}},
		openFn: func(rel string) (io.ReadCloser, error) {
			return io.NopCloser(io.MultiReader(
				strings.NewReader("sample,"),
				errAfterReader{err: io.ErrUnexpectedEOF},
			)), nil
		},
	}
	check := acceptanceCase(t, domain.FlowAcceptance{File: "outputs/qc.csv", Check: "csv-columns",
		Columns: []string{"sample"}}, ev)
	assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
	assert.Equal(t, "evidence_truncated", check.Code)
}

type errAfterReader struct {
	err error
}

func (r errAfterReader) Read(p []byte) (int, error) { return 0, r.err }

func TestEvaluateAcceptanceOpenOutOfScope(t *testing.T) {
	ev := &fakeAcceptanceEvidence{
		stat:    map[string]AcceptanceFile{"outputs/qc.csv": {Size: 4, Regular: true}},
		openErr: map[string]error{"outputs/qc.csv": ErrEvidenceOutOfScope},
	}
	check := acceptanceCase(t, domain.FlowAcceptance{File: "outputs/qc.csv", Check: "csv-columns",
		Columns: []string{"a"}}, ev)
	assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
	assert.Equal(t, "evidence_out_of_scope", check.Code)
}

// The verdict is a wire contract (the node column, the API field and the event
// payload all carry it), so pin the encoded form rather than only the round-trip
// through the same struct: a renamed json tag would otherwise pass every test.
func TestAcceptanceVerdictWireShape(t *testing.T) {
	encoded, err := json.Marshal(AcceptanceVerdictValue{
		Result: domain.AcceptanceUnverified,
		Checks: []AcceptanceCheck{{
			Check: "csv-columns", Verdict: domain.AcceptanceUnverified,
			Code: "evidence_truncated", Detail: "header exceeds the read budget",
		}},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"result":"unverified","checks":[{"check":"csv-columns","verdict":"unverified","code":"evidence_truncated","detail":"header exceeds the read budget"}]}`,
		string(encoded))
	empty, err := json.Marshal(AcceptanceVerdictValue{Result: domain.AcceptanceHolds, Checks: []AcceptanceCheck{}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"result":"holds","checks":[]}`, string(empty))
}

func TestAggregateAcceptance(t *testing.T) {
	holds := AcceptanceCheck{Check: "exists", Verdict: domain.AcceptanceHolds}
	unverified := AcceptanceCheck{Check: "json-valid", Verdict: domain.AcceptanceUnverified}
	fails := AcceptanceCheck{Check: "non-empty", Verdict: domain.AcceptanceDoesNotHold}

	tests := []struct {
		name   string
		checks []AcceptanceCheck
		want   domain.AcceptanceResult
	}{
		{name: "empty list holds", want: domain.AcceptanceHolds},
		{name: "single holds", checks: []AcceptanceCheck{holds}, want: domain.AcceptanceHolds},
		{name: "single unverified", checks: []AcceptanceCheck{unverified}, want: domain.AcceptanceUnverified},
		{name: "single does not hold", checks: []AcceptanceCheck{fails}, want: domain.AcceptanceDoesNotHold},
		{name: "holds plus unverified", checks: []AcceptanceCheck{holds, unverified}, want: domain.AcceptanceUnverified},
		{name: "unverified plus holds", checks: []AcceptanceCheck{unverified, holds}, want: domain.AcceptanceUnverified},
		{name: "unverified plus does not hold", checks: []AcceptanceCheck{unverified, fails}, want: domain.AcceptanceDoesNotHold},
		{name: "does not hold wins over unverified", checks: []AcceptanceCheck{fails, unverified}, want: domain.AcceptanceDoesNotHold},
		{name: "all three", checks: []AcceptanceCheck{holds, unverified, fails}, want: domain.AcceptanceDoesNotHold},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AggregateAcceptance(tc.checks))
		})
	}
}

// TestEvaluateAcceptanceAggregatesSpecs checks the list-level wiring: the task
// verdict is the fold of its leaves, and every leaf is reported.
func TestEvaluateAcceptanceAggregatesSpecs(t *testing.T) {
	ev := &fakeAcceptanceEvidence{content: map[string][]byte{
		"outputs/qc.csv":    []byte("sample,reads\n"),
		"outputs/qc.json":   []byte(`{"a":1}`),
		"outputs/report.md": nil,
	}}
	specs := []domain.FlowAcceptance{
		{File: "outputs/qc.csv", Check: "csv-columns", Columns: []string{"sample", "reads"}},
		{File: "outputs/qc.json", Check: "json-valid"},
		{File: "outputs/report.md", Check: "non-empty"},
	}
	verdict := EvaluateAcceptance(context.Background(), specs, ev, nil)
	require.Len(t, verdict.Checks, 3)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Checks[0].Verdict)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Checks[1].Verdict)
	assert.Equal(t, domain.AcceptanceDoesNotHold, verdict.Checks[2].Verdict)
	assert.Equal(t, "file_empty", verdict.Checks[2].Code)
	assert.Equal(t, domain.AcceptanceDoesNotHold, verdict.Result)

	// No declared acceptance -> holds with no leaves.
	empty := EvaluateAcceptance(context.Background(), nil, ev, nil)
	assert.Equal(t, domain.AcceptanceHolds, empty.Result)
	assert.Empty(t, empty.Checks)
}

func TestKnownChecksIsTheLeafVocabulary(t *testing.T) {
	assert.Equal(t, []string{"exists", "non-empty", "csv-columns", "json-valid", "artifact-published"}, KnownChecks)
	for _, check := range KnownChecks {
		assert.True(t, knownAcceptanceCheck(check))
	}
	assert.False(t, knownAcceptanceCheck("tests-passed"))
}

// fakeAcceptanceArtifacts is the artifact seam: refs are fixed and the managed
// store answers per (artifactID, sha256) pair, so every verdict is reachable
// without a store. lookups records whether the leaf even asked.
type fakeAcceptanceArtifacts struct {
	refs    []domain.ArtifactReference
	present map[string]bool
	err     error
	lookups int
}

func newFakeAcceptanceArtifacts(refs ...domain.ArtifactReference) *fakeAcceptanceArtifacts {
	return &fakeAcceptanceArtifacts{refs: refs, present: map[string]bool{}}
}

func (f *fakeAcceptanceArtifacts) Refs() []domain.ArtifactReference { return f.refs }

func (f *fakeAcceptanceArtifacts) Present(ctx context.Context, artifactID, sha256 string) (bool, error) {
	f.lookups++
	if f.err != nil {
		return false, f.err
	}
	return f.present[artifactID+"@"+sha256], nil
}

// artifactCase evaluates one artifact leaf with no workspace evidence at all,
// which is exactly the production shape: the leaf must not need one.
func artifactCase(t *testing.T, spec domain.FlowAcceptance, artifacts AcceptanceArtifacts) AcceptanceCheck {
	t.Helper()
	verdict := EvaluateAcceptance(context.Background(), []domain.FlowAcceptance{spec}, nil, artifacts)
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, AggregateAcceptance(verdict.Checks), verdict.Result)
	return verdict.Checks[0]
}

// Every artifact leaf verdict is reachable, and none of them reads the
// workspace or the child's prose.
func TestEvaluateAcceptanceArtifactPublished(t *testing.T) {
	published := domain.ArtifactReference{ArtifactID: "art-1", Name: "outputs/qc.csv", SHA256: "abc123"}
	spec := domain.FlowAcceptance{Artifact: "outputs/qc.csv", Check: "artifact-published"}

	tests := []struct {
		name      string
		artifacts AcceptanceArtifacts
		spec      domain.FlowAcceptance
		want      domain.AcceptanceResult
		code      string
		lookups   int
	}{
		{
			name: "retrievable with the recorded digest holds",
			artifacts: func() AcceptanceArtifacts {
				a := newFakeAcceptanceArtifacts(published)
				a.present["art-1@abc123"] = true
				return a
			}(),
			want:    domain.AcceptanceHolds,
			lookups: 1,
		},
		{
			name:      "no ref with that name does not hold",
			artifacts: newFakeAcceptanceArtifacts(domain.ArtifactReference{ArtifactID: "art-2", Name: "outputs/other.csv", SHA256: "def"}),
			want:      domain.AcceptanceDoesNotHold,
			code:      "artifact_missing",
			lookups:   0,
		},
		{
			name:      "empty digest does not hold and is never looked up",
			artifacts: newFakeAcceptanceArtifacts(domain.ArtifactReference{ArtifactID: "art-1", Name: "outputs/qc.csv"}),
			want:      domain.AcceptanceDoesNotHold,
			code:      "artifact_digest_missing",
			lookups:   0,
		},
		{
			name: "a ref with a digest wins over an earlier digest-less one",
			artifacts: func() AcceptanceArtifacts {
				a := newFakeAcceptanceArtifacts(
					domain.ArtifactReference{ArtifactID: "art-9", Name: "outputs/qc.csv"},
					published,
				)
				a.present["art-1@abc123"] = true
				return a
			}(),
			want:    domain.AcceptanceHolds,
			lookups: 1,
		},
		{
			name:      "store says absent does not hold",
			artifacts: newFakeAcceptanceArtifacts(published), // no present entry: the row is gone
			want:      domain.AcceptanceDoesNotHold,
			code:      "artifact_not_retrievable",
			lookups:   1,
		},
		{
			name: "store holds a different digest does not hold",
			artifacts: func() AcceptanceArtifacts {
				a := newFakeAcceptanceArtifacts(published)
				a.present["art-1@rewritten"] = true
				return a
			}(),
			want:    domain.AcceptanceDoesNotHold,
			code:    "artifact_not_retrievable",
			lookups: 1,
		},
		{
			name: "lookup failure is unverified never a pass",
			artifacts: func() AcceptanceArtifacts {
				a := newFakeAcceptanceArtifacts(published)
				a.err = errors.New("database is locked")
				return a
			}(),
			want:    domain.AcceptanceUnverified,
			code:    "evidence_unavailable",
			lookups: 1,
		},
		{
			name: "lookup timeout is unverified",
			artifacts: func() AcceptanceArtifacts {
				a := newFakeAcceptanceArtifacts(published)
				a.err = context.DeadlineExceeded
				return a
			}(),
			want:    domain.AcceptanceUnverified,
			code:    "evidence_unavailable",
			lookups: 1,
		},
		{
			name:      "nil artifacts is unverified",
			artifacts: nil,
			want:      domain.AcceptanceUnverified,
			code:      "evidence_unavailable",
			lookups:   0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := artifactCase(t, spec, tc.artifacts)
			assert.Equal(t, tc.want, check.Verdict)
			assert.Equal(t, tc.code, check.Code)
			assert.Equal(t, acceptanceCheckArtifactPublished, check.Check)
			if fake, ok := tc.artifacts.(*fakeAcceptanceArtifacts); ok {
				assert.Equal(t, tc.lookups, fake.lookups, "the store is asked exactly once, and only when a digest exists")
			}
		})
	}
}

// A clause that mixes a file leaf and an artifact leaf decides each on its own
// evidence: a workspace the runtime cannot read must not poison the artifact
// verdict, and a missing artifact must not read as an unreadable file.
func TestEvaluateAcceptanceMixedFileAndArtifactLeaves(t *testing.T) {
	published := domain.ArtifactReference{ArtifactID: "art-1", Name: "outputs/qc.csv", SHA256: "abc123"}
	artifacts := newFakeAcceptanceArtifacts(published)
	artifacts.present["art-1@abc123"] = true
	specs := []domain.FlowAcceptance{
		{File: "outputs/report.md", Check: "non-empty"},
		{Artifact: "outputs/qc.csv", Check: "artifact-published"},
	}
	verdict := EvaluateAcceptance(context.Background(), specs, nil, artifacts)
	require.Len(t, verdict.Checks, 2)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Checks[0].Verdict)
	assert.Equal(t, "evidence_unavailable", verdict.Checks[0].Code)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Checks[1].Verdict)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
}

// endlessStringReader streams one JSON string that never closes, so the only
// thing that can end the leaf is the read cap. Its budget is a guard for the
// test itself: a reader with no end would hang the suite instead of failing it
// if the cap regressed.
type endlessStringReader struct {
	prefix []byte
	offset int
	sent   int64
	budget int64
}

func (r *endlessStringReader) Read(p []byte) (int, error) {
	if r.sent >= r.budget {
		return 0, errors.New("endless reader exhausted: the read cap did not fire")
	}
	if r.offset < len(r.prefix) {
		written := copy(p, r.prefix[r.offset:])
		r.offset += written
		r.sent += int64(written)
		return written, nil
	}
	for index := range p {
		p[index] = 'x'
	}
	r.sent += int64(len(p))
	return len(p), nil
}

func (r *endlessStringReader) Close() error { return nil }

// json-valid stops at the read cap. One enormous string token is a single call
// into the decoder, and the context is only observably expired between tokens,
// so without a cap the fold could outlive every deadline it set — and it runs
// synchronously, blocking every other in-flight child's settlement.
func TestEvaluateAcceptanceJSONValidReadCap(t *testing.T) {
	const capBytes = AcceptanceJSONReadCapBytes
	reader := &endlessStringReader{
		prefix: []byte(`{"payload":"`),
		budget: capBytes + 8*acceptanceJSONReadChunkBytes,
	}
	ev := &fakeAcceptanceEvidence{
		stat:   map[string]AcceptanceFile{"outputs/qc.json": {Size: capBytes + 1<<20, Regular: true}},
		openFn: func(string) (io.ReadCloser, error) { return reader, nil },
	}
	started := time.Now()
	check := acceptanceCase(t, domain.FlowAcceptance{File: "outputs/qc.json", Check: "json-valid"}, ev)

	assert.Equal(t, domain.AcceptanceUnverified, check.Verdict,
		"a document past the read cap is undecidable, never a failed check: %+v", check)
	assert.Equal(t, "evidence_too_large", check.Code)
	// The bound is proven by the byte count, not by a stopwatch: under -race a
	// synthetic 64 MiB stream takes seconds, and a wall-clock assertion would
	// fail for the instrumentation rather than for the code.
	assert.Greater(t, reader.sent, int64(capBytes), "the cap is only proven by reading past it")
	assert.LessOrEqual(t, reader.sent, int64(capBytes)+int64(acceptanceJSONReadChunkBytes)*16,
		"the read must stop at the cap, not run to the end of the document")
	_ = started
}
