package agentflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// Acceptance leaves. These four names are the whole vocabulary of the first
// acceptance slice; there is no shell/command leaf, so tests-passed is not
// expressible yet.
const (
	acceptanceCheckExists     = "exists"
	acceptanceCheckNonEmpty   = "non-empty"
	acceptanceCheckCSVColumns = "csv-columns"
	acceptanceCheckJSONValid  = "json-valid"
)

// KnownChecks is the single source of truth for the acceptance leaf names. The
// publish validator reads this list instead of keeping its own copy, so adding
// a leaf cannot leave one gate behind.
var KnownChecks = []string{
	acceptanceCheckExists,
	acceptanceCheckNonEmpty,
	acceptanceCheckCSVColumns,
	acceptanceCheckJSONValid,
}

func knownAcceptanceCheck(check string) bool {
	for _, known := range KnownChecks {
		if check == known {
			return true
		}
	}
	return false
}

// Acceptance diagnostic codes reported per leaf.
const (
	codeAcceptanceFileMissing       = "file_missing"
	codeAcceptanceNotAFile          = "evidence_not_a_file"
	codeAcceptanceUnreadable        = "evidence_unreadable"
	codeAcceptanceOutOfScope        = "evidence_out_of_scope"
	codeAcceptanceTruncated         = "evidence_truncated"
	codeAcceptanceTimeout           = "evidence_timeout"
	codeAcceptanceCheckUnknown      = "evidence_check_unknown"
	codeAcceptanceFileEmpty         = "file_empty"
	codeAcceptanceCSVHeaderInvalid  = "csv_header_invalid"
	codeAcceptanceCSVColumnsMissing = "csv_columns_missing"
	codeAcceptanceJSONInvalid       = "json_invalid"
	// codeAcceptanceUnavailable is the whole-task code: the runtime could not
	// even obtain an evidence reader, so every declared leaf is unverified.
	// Never a pass, never a silent skip.
	codeAcceptanceUnavailable    = "evidence_unavailable"
	acceptanceJSONReadChunkBytes = 32 * 1024
)

// acceptanceTaskTimeout bounds one task's whole acceptance evaluation. The fold
// runs it synchronously, so this also bounds how long other in-flight children
// wait for their own fold.
const acceptanceTaskTimeout = 8 * time.Second

// AcceptanceCSVHeaderBudget bounds how many bytes of an artifact csv-columns
// may read. Only the first logical record (the header) is inspected; the file
// itself may be arbitrarily large.
const AcceptanceCSVHeaderBudget = 50000

// acceptanceLeafTimeout bounds one leaf evaluation. It is a variable only so
// tests can shorten it; production always uses two seconds.
var acceptanceLeafTimeout = 2 * time.Second

// ErrEvidenceOutOfScope marks evidence whose path resolves outside the shared
// workspace (or whose resolution escaped it, e.g. through a symlink). The
// evaluator maps it to unverified/evidence_out_of_scope: an inaccessible path
// is never a pass.
var ErrEvidenceOutOfScope = errors.New("acceptance evidence is outside the workspace")

// errAcceptanceInvalidUTF8 marks invalid UTF-8 in JSON evidence. encoding/json
// silently replaces invalid UTF-8 inside strings with U+FFFD, so the byte
// stream is validated before the decoder sees it.
var errAcceptanceInvalidUTF8 = errors.New("acceptance evidence is not valid UTF-8")

// AcceptanceEvidence is the injected seam for every filesystem effect of
// acceptance evaluation. Implementations must be read-only and must fail
// closed: anything they cannot decide is an error, and an error is mapped to
// unverified — never to a pass.
type AcceptanceEvidence interface {
	// Stat reports the file's type and size without opening it.
	Stat(rel string) (AcceptanceFile, error)
	// Open returns a reader for the file's content; the caller owns closing.
	Open(rel string) (io.ReadCloser, error)
}

// AcceptanceFile is the stat answer for one evidence path. Missing and Regular
// are returned as facts rather than as errors because exists must distinguish
// "definitely absent" (does_not_hold) from "cannot be decided" (unverified).
type AcceptanceFile struct {
	Size    int64
	Regular bool
	Missing bool
}

// AcceptanceVerdictValue is the complete acceptance result of one task: the
// aggregate verdict plus the per-leaf detail. It is what the orchestrator
// persists in the task checkpoint.
type AcceptanceVerdictValue struct {
	Result domain.AcceptanceResult `json:"result"`
	Checks []AcceptanceCheck       `json:"checks"`
}

// AcceptanceCheck is one leaf's verdict.
type AcceptanceCheck struct {
	Check   string                  `json:"check"`
	Verdict domain.AcceptanceResult `json:"verdict"`
	Code    string                  `json:"code,omitempty"`
	Detail  string                  `json:"detail,omitempty"` // human-readable, never file content
}

// AggregateAcceptance folds per-leaf verdicts into the task verdict: any
// does_not_hold wins, then any unverified, otherwise holds. An empty list holds
// (publish rejects an empty acceptance list before a Run can exist).
func AggregateAcceptance(checks []AcceptanceCheck) domain.AcceptanceResult {
	result := domain.AcceptanceHolds
	for _, check := range checks {
		switch check.Verdict {
		case domain.AcceptanceDoesNotHold:
			return domain.AcceptanceDoesNotHold
		case domain.AcceptanceUnverified:
			result = domain.AcceptanceUnverified
		}
	}
	return result
}

// EvaluateAcceptance evaluates every declared acceptance condition of one task
// and aggregates the verdicts. It is pure with respect to filesystem effects:
// all evidence comes through the injected AcceptanceEvidence, and every leaf is
// bounded by its own deadline so a caller cannot forget one.
func EvaluateAcceptance(ctx context.Context, specs []domain.FlowAcceptance, evidence AcceptanceEvidence) AcceptanceVerdictValue {
	if ctx == nil {
		ctx = context.Background()
	}
	checks := make([]AcceptanceCheck, 0, len(specs))
	for _, spec := range specs {
		checks = append(checks, evaluateAcceptanceLeaf(ctx, spec, evidence))
	}
	return AcceptanceVerdictValue{Result: AggregateAcceptance(checks), Checks: checks}
}

func newAcceptanceCheck(check string, verdict domain.AcceptanceResult, code, detail string) AcceptanceCheck {
	return AcceptanceCheck{Check: check, Verdict: verdict, Code: code, Detail: detail}
}

// unverifiedAcceptanceVerdict reports every declared leaf as unverified with one
// code. It is what a task with an acceptance clause gets when the runtime cannot
// read evidence at all (no reader configured, or the reader factory failed):
// the clause stays visible in the checkpoint and never reads as a pass.
func unverifiedAcceptanceVerdict(specs []domain.FlowAcceptance, code, detail string) AcceptanceVerdictValue {
	checks := make([]AcceptanceCheck, 0, len(specs))
	for _, spec := range specs {
		checks = append(checks, newAcceptanceCheck(spec.Check, domain.AcceptanceUnverified, code, detail))
	}
	return AcceptanceVerdictValue{Result: domain.AcceptanceUnverified, Checks: checks}
}

// renderAcceptanceAssignment appends the declared conditions to a task's goal as
// task DATA: the child must know what will be checked, or a mismatch is only
// discoverable as an after-the-fact failure. A condition is data, not an
// instruction — it grants no permission, and the runtime decides the verdict
// from the workspace, never from the child's own report. No clause adds no bytes.
func renderAcceptanceAssignment(goal string, specs []domain.FlowAcceptance) string {
	if len(specs) == 0 {
		return goal
	}
	var builder strings.Builder
	builder.WriteString(goal)
	builder.WriteString("\n\nAcceptance (checked by the runtime against the workspace after you finish; your own report is not evidence):\n")
	for _, spec := range specs {
		builder.WriteString("- ")
		builder.WriteString(renderAcceptanceCondition(spec))
		builder.WriteString("\n")
	}
	return builder.String()
}

// renderAcceptanceCondition renders one condition deterministically from the
// frozen spec, so the same definition always produces the same assignment.
func renderAcceptanceCondition(spec domain.FlowAcceptance) string {
	switch spec.Check {
	case acceptanceCheckExists:
		return spec.File + " must exist as a regular file"
	case acceptanceCheckNonEmpty:
		return spec.File + " must be a non-empty regular file"
	case acceptanceCheckCSVColumns:
		return spec.File + " must have columns [" + strings.Join(spec.Columns, ", ") + "]"
	case acceptanceCheckJSONValid:
		return spec.File + " must be valid JSON"
	default:
		return spec.File + " must satisfy the " + spec.Check + " check"
	}
}

// evaluateAcceptanceLeaf evaluates one condition under its own deadline.
func evaluateAcceptanceLeaf(ctx context.Context, spec domain.FlowAcceptance, evidence AcceptanceEvidence) AcceptanceCheck {
	check := spec.Check
	leafCtx, cancel := context.WithTimeout(ctx, acceptanceLeafTimeout)
	defer cancel()
	switch check {
	case acceptanceCheckExists:
		return evaluateAcceptanceExistence(leafCtx, check, spec, evidence, false)
	case acceptanceCheckNonEmpty:
		return evaluateAcceptanceExistence(leafCtx, check, spec, evidence, true)
	case acceptanceCheckCSVColumns:
		return evaluateAcceptanceCSVColumns(leafCtx, check, spec, evidence)
	case acceptanceCheckJSONValid:
		return evaluateAcceptanceJSONValid(leafCtx, check, spec, evidence)
	default:
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceCheckUnknown,
			fmt.Sprintf("unknown acceptance check %q", check))
	}
}

// acceptanceEvidenceFailure maps an evidence error to a code. A fired deadline
// is always unverified, never does_not_hold.
func acceptanceEvidenceFailure(check string, err error) AcceptanceCheck {
	switch {
	case errors.Is(err, ErrEvidenceOutOfScope):
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceOutOfScope,
			"evidence path resolves outside the workspace")
	case errors.Is(err, context.DeadlineExceeded):
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceTimeout,
			"evidence evaluation exceeded its deadline")
	case errors.Is(err, context.Canceled):
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceUnreadable,
			"evidence evaluation was cancelled")
	default:
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceUnreadable,
			"evidence could not be read")
	}
}

// acceptanceEvidenceReadFailure maps a read error: a read that did not complete
// could have been cut short by the read budget, so it is evidence_truncated.
func acceptanceEvidenceReadFailure(check string, err error) AcceptanceCheck {
	if errors.Is(err, ErrEvidenceOutOfScope) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return acceptanceEvidenceFailure(check, err)
	}
	return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceTruncated,
		"evidence read did not complete")
}

// statAcceptanceEvidence stats the evidence path before anything is opened. A
// non-regular file (directory, FIFO, socket, device) is never opened: one
// mkfifo in the workspace must not be able to hang the meta-Run. Stat takes no
// context by design (it must not block on content), so the deadline is only
// observable after the call returns.
func statAcceptanceEvidence(ctx context.Context, check string, spec domain.FlowAcceptance,
	evidence AcceptanceEvidence) (AcceptanceFile, *AcceptanceCheck) {
	if evidence == nil {
		failed := newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceUnreadable,
			"no acceptance evidence source is available")
		return AcceptanceFile{}, &failed
	}
	file, err := evidence.Stat(spec.File)
	if err != nil {
		failed := acceptanceEvidenceFailure(check, err)
		return AcceptanceFile{}, &failed
	}
	if err := ctx.Err(); err != nil {
		failed := acceptanceEvidenceFailure(check, err)
		return AcceptanceFile{}, &failed
	}
	if file.Missing {
		failed := newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceFileMissing,
			fmt.Sprintf("%q does not exist", spec.File))
		return file, &failed
	}
	if !file.Regular {
		failed := newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceNotAFile,
			fmt.Sprintf("%q is not a regular file", spec.File))
		return file, &failed
	}
	return file, nil
}

func openAcceptanceEvidence(ctx context.Context, check string, spec domain.FlowAcceptance,
	evidence AcceptanceEvidence) (io.ReadCloser, *AcceptanceCheck) {
	reader, err := evidence.Open(spec.File)
	if err != nil {
		failed := acceptanceEvidenceFailure(check, err)
		return nil, &failed
	}
	if err := ctx.Err(); err != nil {
		_ = reader.Close()
		failed := acceptanceEvidenceFailure(check, err)
		return nil, &failed
	}
	return reader, nil
}

// evaluateAcceptanceExistence covers exists and non-empty: both stat only.
func evaluateAcceptanceExistence(ctx context.Context, check string, spec domain.FlowAcceptance,
	evidence AcceptanceEvidence, requireNonEmpty bool) AcceptanceCheck {
	file, failed := statAcceptanceEvidence(ctx, check, spec, evidence)
	if failed != nil {
		return *failed
	}
	if requireNonEmpty && file.Size == 0 {
		return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceFileEmpty,
			fmt.Sprintf("%q is empty", spec.File))
	}
	return newAcceptanceCheck(check, domain.AcceptanceHolds, "", "")
}

// evaluateAcceptanceCSVColumns checks that the first logical CSV record
// contains every declared column name. Names match exactly (case and
// whitespace significant), in any order; extra columns are allowed. Data rows
// are never inspected.
func evaluateAcceptanceCSVColumns(ctx context.Context, check string, spec domain.FlowAcceptance,
	evidence AcceptanceEvidence) AcceptanceCheck {
	file, failed := statAcceptanceEvidence(ctx, check, spec, evidence)
	if failed != nil {
		return *failed
	}
	if file.Size == 0 {
		return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceCSVHeaderInvalid,
			fmt.Sprintf("%q is empty", spec.File))
	}
	reader, failed := openAcceptanceEvidence(ctx, check, spec, evidence)
	if failed != nil {
		return *failed
	}
	defer func() { _ = reader.Close() }()

	data, eof, err := readAcceptancePrefix(ctx, reader, AcceptanceCSVHeaderBudget)
	if err != nil {
		return acceptanceEvidenceReadFailure(check, err)
	}
	if !eof && len(data) >= AcceptanceCSVHeaderBudget {
		// The budget ran out; a single extra byte tells us whether the file
		// simply ends there.
		eof = acceptanceReaderAtEOF(reader)
	}
	fields, record, terminated, openQuote := parseCSVHeader(data)
	switch {
	case terminated:
		// Header fits in the budget.
	case eof && openQuote:
		return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceCSVHeaderInvalid,
			"CSV header has an unterminated quoted field")
	case eof:
		// EOF terminates the last record: the header has no trailing newline.
	default:
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceTruncated,
			fmt.Sprintf("CSV header could not be read within the %d-byte evidence budget", AcceptanceCSVHeaderBudget))
	}
	if !utf8.Valid(record) {
		return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceCSVHeaderInvalid,
			"CSV header is not valid UTF-8")
	}
	seen := make(map[string]bool, len(fields))
	for i, name := range fields {
		if name == "" {
			return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceCSVHeaderInvalid,
				fmt.Sprintf("CSV header column %d is empty", i+1))
		}
		if seen[name] {
			return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceCSVHeaderInvalid,
				fmt.Sprintf("CSV header column %d repeats an earlier column name", i+1))
		}
		seen[name] = true
	}
	var missing []string
	for _, want := range spec.Columns {
		if !seen[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceCSVColumnsMissing,
			fmt.Sprintf("%q is missing declared column(s): %s", spec.File, strings.Join(missing, ", ")))
	}
	return newAcceptanceCheck(check, domain.AcceptanceHolds, "", "")
}

// readAcceptancePrefix reads at most limit bytes. eof reports that the stream
// ended at or before the limit.
func readAcceptancePrefix(ctx context.Context, reader io.Reader, limit int) (data []byte, eof bool, err error) {
	buf := make([]byte, limit)
	n := 0
	for n < limit {
		if err := ctx.Err(); err != nil {
			return buf[:n], false, err
		}
		m, readErr := reader.Read(buf[n:])
		n += m
		if readErr == io.EOF {
			return buf[:n], true, nil
		}
		if readErr != nil {
			return buf[:n], false, readErr
		}
	}
	return buf[:n], false, nil
}

// acceptanceReaderAtEOF reports whether the stream has no bytes left. It is
// only used after the read budget was exhausted, to tell "header longer than
// the budget" from "file ends exactly at the budget".
func acceptanceReaderAtEOF(reader io.Reader) bool {
	var probe [1]byte
	for {
		n, err := reader.Read(probe[:])
		if n > 0 {
			return false
		}
		if err == nil {
			continue
		}
		return err == io.EOF
	}
}

// parseCSVHeader parses the first logical CSV record, honouring double quotes,
// escaped "" quotes, and embedded newlines. A leading UTF-8 BOM is skipped.
// record is the header without the BOM and without its terminator.
func parseCSVHeader(data []byte) (fields []string, record []byte, terminated bool, openQuote bool) {
	start := 0
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		start = 3
	}
	var field []byte
	i := start
	for i < len(data) {
		c := data[i]
		if openQuote {
			if c == '"' {
				if i+1 < len(data) && data[i+1] == '"' {
					field = append(field, '"')
					i += 2
					continue
				}
				openQuote = false
				i++
				continue
			}
			field = append(field, c)
			i++
			continue
		}
		switch c {
		case '"':
			if len(field) == 0 {
				openQuote = true
			} else {
				field = append(field, c)
			}
			i++
		case ',':
			fields = append(fields, string(field))
			field = nil
			i++
		case '\r', '\n':
			fields = append(fields, string(field))
			return fields, data[start:i], true, false
		default:
			field = append(field, c)
			i++
		}
	}
	return append(fields, string(field)), data[start:], false, openQuote
}

// evaluateAcceptanceJSONValid streams the evidence through encoding/json. There
// is no total size limit: the leaf semantics are "syntactically valid JSON", so
// the per-leaf deadline is the only bound.
func evaluateAcceptanceJSONValid(ctx context.Context, check string, spec domain.FlowAcceptance,
	evidence AcceptanceEvidence) AcceptanceCheck {
	if _, failed := statAcceptanceEvidence(ctx, check, spec, evidence); failed != nil {
		return *failed
	}
	reader, failed := openAcceptanceEvidence(ctx, check, spec, evidence)
	if failed != nil {
		return *failed
	}
	defer func() { _ = reader.Close() }()

	validating := newUTF8ValidatingReader(reader)
	decoder := json.NewDecoder(validating)
	depth := 0
	values := 0
	for {
		if err := ctx.Err(); err != nil {
			return acceptanceEvidenceFailure(check, err)
		}
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if validating.sawInvalidUTF8() {
				return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceJSONInvalid,
					"evidence is not valid UTF-8")
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return acceptanceEvidenceFailure(check, ctxErr)
			}
			return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceJSONInvalid,
				"evidence is not valid JSON")
		}
		switch token {
		case json.Delim('{'), json.Delim('['):
			if depth == 0 && values > 0 {
				return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceJSONInvalid,
					"evidence contains more than one JSON value")
			}
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
			if depth == 0 {
				values++
			}
		default:
			if depth == 0 {
				values++
				if values > 1 {
					return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceJSONInvalid,
						"evidence contains more than one JSON value")
				}
			}
		}
	}
	if values != 1 {
		return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceJSONInvalid,
			"evidence is not a single JSON value")
	}
	return newAcceptanceCheck(check, domain.AcceptanceHolds, "", "")
}

// utf8ValidatingReader streams bytes while rejecting invalid UTF-8, which
// encoding/json would otherwise accept inside strings (it substitutes U+FFFD).
// Sequences straddling a read boundary are carried to the next chunk.
type utf8ValidatingReader struct {
	reader  io.Reader
	ready   []byte // validated bytes not yet handed out
	carry   []byte // trailing bytes of a possibly incomplete sequence
	err     error  // sticky terminal error
	invalid bool
}

func newUTF8ValidatingReader(reader io.Reader) *utf8ValidatingReader {
	return &utf8ValidatingReader{reader: reader}
}

func (r *utf8ValidatingReader) sawInvalidUTF8() bool { return r.invalid }

func (r *utf8ValidatingReader) Read(p []byte) (int, error) {
	for len(r.ready) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		chunk := make([]byte, acceptanceJSONReadChunkBytes)
		n, readErr := r.reader.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			if len(r.carry) > 0 {
				merged := make([]byte, 0, len(r.carry)+len(data))
				merged = append(merged, r.carry...)
				merged = append(merged, data...)
				data = merged
				r.carry = nil
			}
			complete, tail, bad := splitValidUTF8Prefix(data)
			r.ready = complete
			if len(tail) > 0 {
				r.carry = append([]byte(nil), tail...)
			}
			if readErr != nil {
				if len(r.carry) > 0 {
					// A sequence cut off at the end of the stream is invalid.
					bad = true
					r.carry = nil
				}
				if !bad {
					r.err = readErr
				}
			}
			if bad {
				r.invalid = true
				r.err = errAcceptanceInvalidUTF8
			}
			if len(r.ready) == 0 && r.err != nil {
				return 0, r.err
			}
			continue
		}
		if readErr != nil {
			if readErr == io.EOF && len(r.carry) > 0 {
				r.invalid = true
				r.err = errAcceptanceInvalidUTF8
				continue
			}
			r.err = readErr
			continue
		}
	}
	n := copy(p, r.ready)
	r.ready = r.ready[n:]
	return n, nil
}

// splitValidUTF8Prefix splits data into a valid prefix and, when the data ends
// inside a multi-byte sequence, the incomplete tail. bad reports invalid UTF-8
// inside the prefix.
func splitValidUTF8Prefix(data []byte) (complete []byte, tail []byte, bad bool) {
	i := 0
	for i < len(data) {
		b := data[i]
		if b < utf8.RuneSelf {
			i++
			continue
		}
		size := utf8SequenceLen(b)
		if size == 0 {
			return data[:i], nil, true
		}
		if i+size > len(data) {
			for j := i + 1; j < len(data); j++ {
				if data[j]&0xC0 != 0x80 {
					return data[:i], nil, true
				}
			}
			return data[:i], data[i:], false
		}
		decoded, decodedSize := utf8.DecodeRune(data[i : i+size])
		if decoded == utf8.RuneError && decodedSize == 1 {
			return data[:i], nil, true
		}
		i += size
	}
	return data[:i], nil, false
}

// utf8SequenceLen returns the byte length a UTF-8 sequence starting with b
// claims, or 0 when b cannot start a sequence.
func utf8SequenceLen(b byte) int {
	switch {
	case b < utf8.RuneSelf:
		return 1
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 0
	}
}
