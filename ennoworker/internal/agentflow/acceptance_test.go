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
	verdict := EvaluateAcceptance(context.Background(), []domain.FlowAcceptance{spec}, AcceptanceFacts{Evidence: evidence})
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
	check := acceptanceCase(t, domain.FlowAcceptance{File: "outputs/x", Check: "no-such-leaf"}, ev)
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
	verdict := EvaluateAcceptance(context.Background(), specs, AcceptanceFacts{Evidence: ev})
	require.Len(t, verdict.Checks, 3)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Checks[0].Verdict)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Checks[1].Verdict)
	assert.Equal(t, domain.AcceptanceDoesNotHold, verdict.Checks[2].Verdict)
	assert.Equal(t, "file_empty", verdict.Checks[2].Code)
	assert.Equal(t, domain.AcceptanceDoesNotHold, verdict.Result)

	// No declared acceptance -> holds with no leaves.
	empty := EvaluateAcceptance(context.Background(), nil, AcceptanceFacts{Evidence: ev})
	assert.Equal(t, domain.AcceptanceHolds, empty.Result)
	assert.Empty(t, empty.Checks)
}

func TestKnownChecksIsTheLeafVocabulary(t *testing.T) {
	assert.Equal(t, []string{"exists", "non-empty", "csv-columns", "json-valid", "artifact-published", "tests-passed"}, KnownChecks)
	for _, check := range KnownChecks {
		assert.True(t, knownAcceptanceCheck(check))
	}
	assert.False(t, knownAcceptanceCheck("no-such-leaf"))
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
	verdict := EvaluateAcceptance(context.Background(), []domain.FlowAcceptance{spec}, AcceptanceFacts{Artifacts: artifacts})
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
	verdict := EvaluateAcceptance(context.Background(), specs, AcceptanceFacts{Artifacts: artifacts})
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

// fakeAcceptanceExecutions is the execution seam: it exposes exactly what the
// recorder projected, so a verdict can only be decided from projected output.
type fakeAcceptanceExecutions struct {
	executions []domain.ToolExecution
	nilSlice   bool
}

func (f fakeAcceptanceExecutions) Executions() []domain.ToolExecution {
	if f.nilSlice {
		return nil
	}
	return f.executions
}

func executionCase(t *testing.T, command string, executions []domain.ToolExecution) AcceptanceCheck {
	t.Helper()
	spec := domain.FlowAcceptance{Check: "tests-passed", Command: command}
	verdict := EvaluateAcceptance(context.Background(), []domain.FlowAcceptance{spec}, AcceptanceFacts{
		Executions: fakeAcceptanceExecutions{executions: executions},
	})
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, AggregateAcceptance(verdict.Checks), verdict.Result)
	return verdict.Checks[0]
}

func bashExecution(seq int64, command, output string) domain.ToolExecution {
	return domain.ToolExecution{Seq: seq, ToolCallID: "call-" + command, Tool: "bash",
		Command: command, ExitCode: 0, Output: output}
}

// The "not a pass" case comes first for every runner: a zero exit status and an
// `ok` banner are not enough when no test actually ran.
func TestEvaluateAcceptanceTestsPassedNotAPass(t *testing.T) {
	tests := []struct {
		name    string
		command string
		output  string
		code    string
	}{
		{
			name:    "go test with only no test files",
			command: "go test -v ./...",
			output:  "?   \texample.com/x\t[no test files]\n?   \texample.com/y\t[no test files]\n",
			code:    "evidence_no_pass_summary",
		},
		{
			// F1: the documented consequence. Without -v there is no per-test
			// line at all, so the ok banner cannot distinguish a zero-test run
			// from a real one. Never a pass.
			name:    "go test without verbose output",
			command: "go test ./...",
			output:  "ok  \texample.com/x\t0.006s\n",
			code:    "evidence_no_pass_summary",
		},
		{
			// F1: `ok <pkg> 0.006s [no tests to run]` is the zero-test banner.
			name:    "go test with no tests to run",
			command: "go test -v -run NoSuchTest ./...",
			output:  "ok  \texample.com/x\t0.006s [no tests to run]\n",
			code:    "evidence_no_pass_summary",
		},
		{
			// F1: even a visible PASS line does not outvote the zero-test marker.
			name:    "go test with a pass line but no tests to run",
			command: "go test -v ./...",
			output:  "=== RUN   TestA\n--- PASS: TestA (0.00s)\nok  \texample.com/x\t0.006s [no tests to run]\n",
			code:    "evidence_no_pass_summary",
		},
		{
			name:    "go test with only skips",
			command: "go test -v ./...",
			output:  "=== RUN   TestSkipped\n--- SKIP: TestSkipped (0.00s)\n    x_test.go:4: not today\nok  \texample.com/x\t0.002s\n",
			code:    "evidence_no_pass_summary",
		},
		{
			name:    "go test with no output",
			command: "go test -v ./...",
			output:  "",
			code:    "evidence_no_pass_summary",
		},
		{
			name:    "go test failure",
			command: "go test -v ./...",
			output:  "--- FAIL: TestA (0.00s)\nFAIL\texample.com/x\t0.003s\nFAIL\n",
			code:    "command_failed",
		},
		{
			// F1: with -json the package-level pass event is printed even when
			// the package executed no test, so it is not a pass on its own.
			name:    "go test -json with only a package pass event",
			command: "go test -json ./...",
			output: "{\"Time\":\"2026-11-15T00:00:00Z\",\"Action\":\"run\",\"Package\":\"example.com/x\"}\n" +
				"{\"Time\":\"2026-11-15T00:00:00Z\",\"Action\":\"pass\",\"Package\":\"example.com/x\",\"Elapsed\":0.006}\n",
			code: "evidence_no_pass_summary",
		},
		{
			name:    "pytest with zero passed",
			command: "pytest -q",
			output:  "0 passed, 2 skipped in 0.04s\n",
			code:    "evidence_no_pass_summary",
		},
		{
			name:    "pytest all skipped",
			command: "pytest -q",
			output:  "2 skipped in 0.03s\n",
			code:    "evidence_no_pass_summary",
		},
		{
			name:    "pytest failure summary",
			command: "pytest -q",
			output:  "FAILED tests/test_a.py::test_x - AssertionError\n1 failed, 1 passed in 0.05s\n",
			code:    "command_failed",
		},
		{
			// F4: an `error:` line is the tests' own stdout, not a failure fact.
			// A run with no pass counter stays unverified, not does_not_hold.
			name:    "pytest with a stray error line and no summary",
			command: "pytest -q",
			output:  "error: retrying connection\n2 skipped in 0.03s\n",
			code:    "evidence_no_pass_summary",
		},
		{
			name:    "pytest with a real failure summary next to a stray error line",
			command: "pytest -q",
			output:  "error: retrying connection\nFAILED tests/test_a.py::test_x\n1 failed in 0.05s\n",
			code:    "command_failed",
		},
		{
			name:    "node --test with only todos",
			command: "node --test",
			output:  "TAP version 13\n# tests 0\n# pass 0\n# fail 0\n# cancelled 0\n# skipped 0\n# todo 2\n1..0\n",
			code:    "evidence_no_pass_summary",
		},
		{
			// F2: a test printing `# pass 1` must not add itself to the summary.
			name:    "node --test with a console.log pass injection",
			command: "node --test",
			output: "TAP version 13\nok 1 - prints\n" +
				"console.log(\"# pass 1\")\n# pass 1\n1..1\n# tests 1\n# pass 0\n# fail 0\n# cancelled 0\n",
			code: "evidence_no_pass_summary",
		},
		{
			name:    "node --test with a printed pass counter next to a real failure",
			command: "node --test",
			output:  "TAP version 13\nnot ok 1 - adds\n# pass 1\n# tests 1\n# pass 0\n# fail 1\n# cancelled 0\n",
			code:    "command_failed",
		},
		{
			name:    "node --test with a failing test line",
			command: "node --test",
			output:  "TAP version 13\nnot ok 1 - adds\n# pass 0\n# fail 1\n",
			code:    "command_failed",
		},
		{
			// F2: the counters are read as one summary block, and the block must
			// be complete: a missing counter is not 0.
			name:    "node --test with a partial summary block",
			command: "node --test",
			output:  "TAP version 13\nok 1 - adds\n# tests 1\n# pass 1\n",
			code:    "evidence_no_pass_summary",
		},
		{
			// F2: pass <= tests, so a summary that claims more passes than tests
			// is not a pass.
			name:    "node --test with more passes than tests",
			command: "node --test",
			output:  "TAP version 13\n# tests 1\n# pass 2\n# fail 0\n# cancelled 0\n",
			code:    "evidence_no_pass_summary",
		},
		{
			name:    "cargo test with zero tests",
			command: "cargo test",
			output:  "running 0 tests\n\ntest result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\n",
			code:    "evidence_no_pass_summary",
		},
		{
			name:    "cargo test failure",
			command: "cargo test",
			output:  "test result: FAILED. 1 passed; 1 failed; 0 ignored\n",
			code:    "command_failed",
		},
		{
			// F5: any FAILED result line fails the run, not just the first one.
			name:    "cargo test where a later target fails",
			command: "cargo test",
			output: "test result: ok. 2 passed; 0 failed; 0 ignored\n\n" +
				"test result: FAILED. 0 passed; 1 failed; 0 ignored\n",
			code: "command_failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := executionCase(t, tc.command, []domain.ToolExecution{bashExecution(1, tc.command, tc.output)})
			assert.NotEqual(t, domain.AcceptanceHolds, check.Verdict, "an unclear run must never be a pass")
			require.Equal(t, tc.code, check.Code)
			want := domain.AcceptanceDoesNotHold
			if tc.code == "evidence_no_pass_summary" {
				want = domain.AcceptanceUnverified
			}
			assert.Equal(t, want, check.Verdict)
		})
	}
}

// The passing summary of every supported runner, including the counters a naive
// "failed"/"error" substring scan would misread as a failure.
func TestEvaluateAcceptanceTestsPassedHolds(t *testing.T) {
	tests := []struct {
		name    string
		command string
		output  string
	}{
		{
			name:    "go test verbose ok",
			command: "go test -v ./...",
			output:  "=== RUN   TestA\n--- PASS: TestA (0.00s)\nPASS\nok  \texample.com/x\t0.002s\n?   \texample.com/y\t[no test files]\n",
		},
		{
			name:    "go test mixed skip and pass",
			command: "go test -v ./...",
			output:  "--- PASS: TestA (0.00s)\n--- SKIP: TestB (0.00s)\nok  \texample.com/x\t0.002s\n",
		},
		{
			// F4: go test replays the tests' own stdout, so a stray `error:`
			// line in a passing suite is not a failure.
			name:    "go test verbose ok with a stray error line",
			command: "go test -v ./...",
			output:  "=== RUN   TestA\n    a_test.go:9: error: retrying\n--- PASS: TestA (0.00s)\nok  \texample.com/x\t0.002s\n",
		},
		{
			// F1: -json is what publish accepts instead of -v, so it is decided
			// here too: a pass event that names a Test is a passing test.
			name:    "go test -json",
			command: "go test -json ./...",
			output: "{\"Time\":\"2026-11-15T00:00:00Z\",\"Action\":\"run\",\"Package\":\"example.com/x\",\"Test\":\"TestA\"}\n" +
				"{\"Time\":\"2026-11-15T00:00:00Z\",\"Action\":\"output\",\"Package\":\"example.com/x\",\"Test\":\"TestA\",\"Output\":\"=== RUN   TestA\\n\"}\n" +
				"{\"Time\":\"2026-11-15T00:00:00Z\",\"Action\":\"pass\",\"Package\":\"example.com/x\",\"Test\":\"TestA\",\"Elapsed\":0}\n" +
				"{\"Time\":\"2026-11-15T00:00:00Z\",\"Action\":\"pass\",\"Package\":\"example.com/x\",\"Elapsed\":0.006}\n",
		},
		{
			name:    "pytest passed",
			command: "pytest tests/test_qc.py -q",
			output:  "....\n4 passed in 0.12s\n",
		},
		{
			// F4: a passing suite that prints `error:` holds.
			name:    "pytest passed with a stray error line",
			command: "pytest -q",
			output:  "retrying\nerror: connection reset\nerror: retrying\n.\n4 passed in 0.12s\n",
		},
		{
			name:    "python -m pytest passed",
			command: "python -m pytest -q",
			output:  "3 passed, 1 warning in 0.20s\n",
		},
		{
			name:    "node --test spec reporter",
			command: "node --test",
			output:  "TESTS 3\n\u2139 tests 3\n\u2139 pass 3\n\u2139 fail 0\n\u2139 cancelled 0\n",
		},
		{
			name:    "node --test tap",
			command: "node --test test/x.test.js",
			output:  "TAP version 13\nok 1 - adds\n1..1\n# tests 1\n# pass 1\n# fail 0\n# cancelled 0\n",
		},
		{
			name:    "cargo test ok with zero failed",
			command: "cargo test",
			output:  "test result: ok. 7 passed; 0 failed; 0 ignored; 0 measured; 2 filtered out\n",
		},
		{
			// F5: one `test result:` line per target, so an empty first target
			// must not hide a later target's pass.
			name:    "cargo test with an empty first target",
			command: "cargo test",
			output: "running 0 tests\n\ntest result: ok. 0 passed; 0 failed; 0 ignored\n\n" +
				"running 5 tests\n\ntest result: ok. 5 passed; 0 failed; 0 ignored\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := executionCase(t, tc.command, []domain.ToolExecution{bashExecution(1, tc.command, tc.output)})
			assert.Equal(t, domain.AcceptanceHolds, check.Verdict, "code=%s detail=%s", check.Code, check.Detail)
			assert.Empty(t, check.Code)
		})
	}
}

// Matching is token-for-token against the execution the child actually ran. A
// compound recorded command is never the same invocation, and a later unrelated
// success is not evidence for the declared command.
func TestEvaluateAcceptanceTestsPassedMatching(t *testing.T) {
	passGo := "=== RUN   TestA\n--- PASS: TestA (0.00s)\nok  \texample.com/x\t0.002s\n"
	tests := []struct {
		name       string
		command    string
		executions []domain.ToolExecution
		verdict    domain.AcceptanceResult
		code       string
	}{
		{
			name:    "or-true recorded run does not satisfy the plain command",
			command: "pytest a.py",
			executions: []domain.ToolExecution{
				bashExecution(1, "pytest a.py || true", "1 failed in 0.1s\n"),
			},
			verdict: domain.AcceptanceUnverified, code: "evidence_no_execution",
		},
		{
			name:    "cd && recorded run does not satisfy the plain command",
			command: "pytest",
			executions: []domain.ToolExecution{
				bashExecution(1, "cd x && pytest", "2 passed in 0.1s\n"),
			},
			verdict: domain.AcceptanceUnverified, code: "evidence_no_execution",
		},
		{
			name:    "a different working directory expressed as an argument matches",
			command: "pytest tests/sub/test_x.py",
			executions: []domain.ToolExecution{
				bashExecution(1, "pytest tests/sub/test_x.py", "1 passed in 0.1s\n"),
			},
			verdict: domain.AcceptanceHolds,
		},
		{
			name:    "substring of a larger invocation does not match",
			command: "go test",
			executions: []domain.ToolExecution{
				bashExecution(1, "go test ./...", passGo),
			},
			verdict: domain.AcceptanceUnverified, code: "evidence_no_execution",
		},
		{
			name:    "a later unrelated success never satisfies the criterion",
			command: "go test ./pkg",
			executions: []domain.ToolExecution{
				bashExecution(1, "go test ./other", passGo),
			},
			verdict: domain.AcceptanceUnverified, code: "evidence_no_execution",
		},
		{
			name:    "failed attempt then a passing re-run holds",
			command: "go test ./...",
			executions: []domain.ToolExecution{
				{Seq: 1, Tool: "bash", Command: "go test ./...", ExitCode: 1, IsError: true,
					Output: "--- FAIL: TestA (0.00s)\nFAIL\texample.com/x\t0.003s\n"},
				bashExecution(2, "go test ./...", passGo),
			},
			verdict: domain.AcceptanceHolds,
		},
		{
			name:    "passing run then a failing re-run does not hold",
			command: "go test ./...",
			executions: []domain.ToolExecution{
				bashExecution(1, "go test ./...", passGo),
				{Seq: 2, Tool: "bash", Command: "go test ./...", ExitCode: 1, IsError: true,
					Output: "--- FAIL: TestA (0.00s)\nFAIL\texample.com/x\t0.003s\n"},
			},
			verdict: domain.AcceptanceDoesNotHold, code: "command_failed",
		},
		{
			name:    "exec argv matches its own recorded vector",
			command: "go test ./...",
			executions: []domain.ToolExecution{
				{Seq: 3, Tool: "exec", Argv: []string{"go", "test", "./..."}, ExitCode: 0, Output: passGo},
			},
			verdict: domain.AcceptanceHolds,
		},
		{
			name:    "leading environment assignments are not part of the match key",
			command: "GOFLAGS=-count=1 go test ./...",
			executions: []domain.ToolExecution{
				bashExecution(1, "go test ./...", passGo),
			},
			verdict: domain.AcceptanceHolds,
		},
		{
			name:    "recorded env prefix is not part of the match key",
			command: "go test ./...",
			executions: []domain.ToolExecution{
				bashExecution(1, "env GOCACHE=/tmp/cache go test ./...", passGo),
			},
			verdict: domain.AcceptanceHolds,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := executionCase(t, tc.command, tc.executions)
			assert.Equal(t, tc.verdict, check.Verdict, "code=%s detail=%s", check.Code, check.Detail)
			if tc.code != "" {
				assert.Equal(t, tc.code, check.Code)
			}
		})
	}
}

// The evidence can also be missing or unusable; every such case is unverified,
// never a pass and never a failed check.
func TestEvaluateAcceptanceTestsPassedUnverified(t *testing.T) {
	passGo := "=== RUN   TestA\n--- PASS: TestA (0.00s)\nok  \texample.com/x\t0.002s\n"

	t.Run("nil executions", func(t *testing.T) {
		spec := domain.FlowAcceptance{Check: "tests-passed", Command: "go test ./..."}
		verdict := EvaluateAcceptance(context.Background(), []domain.FlowAcceptance{spec}, AcceptanceFacts{})
		assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
		assert.Equal(t, "evidence_unavailable", verdict.Checks[0].Code)
	})

	t.Run("nil execution slice", func(t *testing.T) {
		spec := domain.FlowAcceptance{Check: "tests-passed", Command: "go test ./..."}
		verdict := EvaluateAcceptance(context.Background(), []domain.FlowAcceptance{spec}, AcceptanceFacts{
			Executions: fakeAcceptanceExecutions{nilSlice: true},
		})
		assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
		assert.Equal(t, "evidence_no_execution", verdict.Checks[0].Code)
	})

	t.Run("no recorded execution of the command", func(t *testing.T) {
		check := executionCase(t, "go test ./...", []domain.ToolExecution{
			bashExecution(1, "ls -la", "a.go\n"),
		})
		assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
		assert.Equal(t, "evidence_no_execution", check.Code)
	})

	t.Run("unsupported runner", func(t *testing.T) {
		check := executionCase(t, "make test", []domain.ToolExecution{
			bashExecution(1, "make test", "all good\n"),
		})
		assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
		assert.Equal(t, "evidence_runner_unsupported", check.Code)
	})

	t.Run("truncated output without a summary", func(t *testing.T) {
		execution := bashExecution(1, "go test -v ./...", "=== RUN   TestA\n")
		execution.Truncated = true
		check := executionCase(t, "go test -v ./...", []domain.ToolExecution{execution})
		assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
		assert.Equal(t, "evidence_truncated", check.Code)
	})

	// F3: the opposite of what this test used to assert. Only the head of the
	// output was kept, so a visible passing head is not the whole run.
	t.Run("truncated output with a visible passing summary is truncated, not a pass", func(t *testing.T) {
		execution := bashExecution(1, "go test -v ./...", passGo)
		execution.Truncated = true
		check := executionCase(t, "go test -v ./...", []domain.ToolExecution{execution})
		assert.Equal(t, domain.AcceptanceUnverified, check.Verdict,
			"a truncated run is undecidable, never a pass")
		assert.Equal(t, "evidence_truncated", check.Code)
	})

	// F3: the recorder never sets Truncated from the preview, so the tool's own
	// marker is the signal real truncation leaves behind.
	t.Run("output ending in the tool truncation marker is truncated", func(t *testing.T) {
		execution := bashExecution(1, "go test -v ./...", passGo+"\n[output truncated]")
		check := executionCase(t, "go test -v ./...", []domain.ToolExecution{execution})
		assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
		assert.Equal(t, "evidence_truncated", check.Code)
	})

	// Truncation does not excuse a visible failure: the head that was kept says
	// the run failed, so the clause does not hold.
	t.Run("truncated output whose visible head shows a failure still fails", func(t *testing.T) {
		execution := bashExecution(1, "go test -v ./...",
			"--- FAIL: TestA (0.00s)\nFAIL\texample.com/x\t0.003s\n\n[output truncated]")
		check := executionCase(t, "go test -v ./...", []domain.ToolExecution{execution})
		assert.Equal(t, domain.AcceptanceDoesNotHold, check.Verdict)
		assert.Equal(t, "command_failed", check.Code)
	})

	t.Run("compound declared command is refused again at run time", func(t *testing.T) {
		check := executionCase(t, "pytest a.py || true", []domain.ToolExecution{
			bashExecution(1, "pytest a.py || true", "1 passed in 0.1s\n"),
		})
		assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
		assert.Equal(t, "acceptance_command_compound", check.Code)
	})

	t.Run("empty declared command", func(t *testing.T) {
		check := executionCase(t, "", []domain.ToolExecution{bashExecution(1, "go test ./...", passGo)})
		assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
		assert.Equal(t, "evidence_no_execution", check.Code)
	})
}

// A passing summary in the visible prefix does not outvote a failed execution,
// and an unrecorded exit status (-1) does not outvote a passing summary: the
// recorded IsError is the failure fact, the exit column may simply be absent.
func TestEvaluateAcceptanceTestsPassedExitStatus(t *testing.T) {
	passGo := "=== RUN   TestA\n--- PASS: TestA (0.00s)\nok  \texample.com/x\t0.002s\n"

	errored := bashExecution(1, "go test ./...", passGo)
	errored.IsError = true
	errored.ExitCode = 1
	check := executionCase(t, "go test ./...", []domain.ToolExecution{errored})
	assert.Equal(t, domain.AcceptanceDoesNotHold, check.Verdict)
	assert.Equal(t, "command_failed", check.Code)

	recordedExitOnly := bashExecution(1, "go test ./...", "=== RUN TestA\n")
	recordedExitOnly.ExitCode = 2
	check = executionCase(t, "go test ./...", []domain.ToolExecution{recordedExitOnly})
	assert.Equal(t, domain.AcceptanceDoesNotHold, check.Verdict)
	assert.Equal(t, "command_failed", check.Code)

	unrecorded := bashExecution(1, "go test -v ./...", passGo)
	unrecorded.ExitCode = domain.ExitCodeUnrecorded
	check = executionCase(t, "go test -v ./...", []domain.ToolExecution{unrecorded})
	assert.Equal(t, domain.AcceptanceHolds, check.Verdict,
		"an exit status the recorder never wrote is unknown, not a failure")

	// F9: any non-zero status that is not the "unrecorded" sentinel is a
	// failure, including a negative signal code a future writer may record.
	for _, code := range []int{-9, 137, 1} {
		terminated := bashExecution(1, "go test -v ./...", passGo)
		terminated.ExitCode = code
		check = executionCase(t, "go test -v ./...", []domain.ToolExecution{terminated})
		assert.Equal(t, domain.AcceptanceDoesNotHold, check.Verdict, "exit status %d", code)
		assert.Equal(t, "command_failed", check.Code)
	}
}

// A tests-passed clause decides on its own evidence and never asks for a
// workspace reader; a file clause next to it stays unverified when the runtime
// has no reader, and the aggregate follows the three-valued rule.
func TestEvaluateAcceptanceMixedCommandAndFileLeaves(t *testing.T) {
	specs := []domain.FlowAcceptance{
		{Check: "tests-passed", Command: "go test -v ./..."},
		{File: "outputs/report.md", Check: "non-empty"},
	}
	verdict := EvaluateAcceptance(context.Background(), specs, AcceptanceFacts{
		Executions: fakeAcceptanceExecutions{executions: []domain.ToolExecution{
			bashExecution(1, "go test -v ./...", "=== RUN   TestA\n--- PASS: TestA (0.00s)\nok  \texample.com/x\t0.002s\n"),
		}},
	})
	require.Len(t, verdict.Checks, 2)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Checks[0].Verdict)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Checks[1].Verdict)
	assert.Equal(t, "evidence_unavailable", verdict.Checks[1].Code)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
}

// needsWorkspaceEvidence stays false for the leaves that read another source:
// opening a jail for a clause that never touches the workspace would turn an
// unavailable jail into a false unverified.
func TestNeedsWorkspaceEvidenceByLeaf(t *testing.T) {
	assert.True(t, needsWorkspaceEvidence([]domain.FlowAcceptance{{File: "x", Check: "exists"}}))
	assert.False(t, needsWorkspaceEvidence([]domain.FlowAcceptance{{Artifact: "x", Check: "artifact-published"}}))
	assert.False(t, needsWorkspaceEvidence([]domain.FlowAcceptance{{Command: "go test", Check: "tests-passed"}}))
	assert.True(t, needsExecutions([]domain.FlowAcceptance{{Command: "go test", Check: "tests-passed"}}))
	assert.False(t, needsExecutions([]domain.FlowAcceptance{{File: "x", Check: "exists"}}))
}
