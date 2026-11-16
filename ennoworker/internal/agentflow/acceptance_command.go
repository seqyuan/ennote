package agentflow

// The tests-passed leaf: anchor a declared test command to one recorded shell
// execution, then decide whether that execution's projected output shows a
// passing summary. Matching and runner classification are here; the evaluator,
// the file leaves and the artifact leaf stay in acceptance.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// compoundCommandToken returns the first part of command that carries shell
// control syntax, or "" when the command is one plain invocation.
//
// The rule is deliberately token-shaped: a bare operator token, a token that
// STARTS with a redirection/pipe/background operator, a command or variable
// substitution, or a backtick is refused. An operator inside a quoted argument
// (`pytest -k "a && b"`, `go test -run "TestA|TestB" ./...`) is allowed: the
// shell keeps it inside one argument, and the recorded command is re-split into
// tokens the same way, so the invocation is still anchored token for token.
// A newline or carriage return is refused because it can join two invocations
// without leaving an operator token behind.
func compoundCommandToken(command string) string {
	if strings.ContainsAny(command, "\n\r") {
		return acceptanceCompoundDetail
	}
	for _, token := range ParseCheckCommand(command) {
		for _, operator := range acceptanceShellOperators {
			if token == operator {
				return token
			}
		}
		if strings.Contains(token, "$(") || strings.Contains(token, "${") || strings.Contains(token, "`") {
			return token
		}
		if strings.HasPrefix(token, ">") || strings.HasPrefix(token, "<") ||
			strings.HasPrefix(token, "|") || strings.HasPrefix(token, "&") {
			return token
		}
	}
	return ""
}

// acceptanceInvocationKey normalises one invocation into its match key: the
// tokens with a leading `env` and any leading VAR=value assignments dropped.
// Environment is not part of the match key, so the same program with the same
// arguments is the same execution whichever variable the caller exported.
func acceptanceInvocationKey(tokens []string) []string {
	start := 0
	if start < len(tokens) && tokens[start] == "env" {
		start++
	}
	for start < len(tokens) && isEnvironmentAssignment(tokens[start]) {
		start++
	}
	return tokens[start:]
}

// isEnvironmentAssignment reports whether the token is a leading `NAME=value`
// assignment the shell consumes before it starts the program.
func isEnvironmentAssignment(token string) bool {
	name, _, found := strings.Cut(token, "=")
	if !found || name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// executionTokens is the recorded token vector of one execution: exec records an
// argv and bash records a command string, which is split with the same
// ParseCheckCommand the check tasks use.
func executionTokens(execution domain.ToolExecution) ([]string, bool) {
	switch execution.Tool {
	case "bash":
		return ParseCheckCommand(execution.Command), strings.TrimSpace(execution.Command) != ""
	case "exec":
		return execution.Argv, len(execution.Argv) > 0
	default:
		return nil, false
	}
}

// matchingExecution picks the execution a verdict is decided on: the match with
// the highest seq. A failed attempt followed by a successful re-run is therefore
// a pass, and the reverse is not. It never falls back to "some successful call":
// an execution that does not match token for token is not evidence at all.
func matchingExecution(executions []domain.ToolExecution, declared []string) (domain.ToolExecution, bool) {
	var best domain.ToolExecution
	found := false
	for _, execution := range executions {
		tokens, ok := executionTokens(execution)
		if !ok {
			continue
		}
		if !equalTokens(acceptanceInvocationKey(tokens), declared) {
			continue
		}
		if !found || execution.Seq >= best.Seq {
			best, found = execution, true
		}
	}
	return best, found
}

func equalTokens(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func hasTokenPrefix(tokens []string, prefix ...string) bool {
	if len(tokens) < len(prefix) {
		return false
	}
	for i := range prefix {
		if tokens[i] != prefix[i] {
			return false
		}
	}
	return true
}

// acceptanceRunnerOf infers the runner from the declared tokens. The four
// supported prefixes are `go test`, `pytest` / `python -m pytest` /
// `python3 -m pytest`, `node --test` and `cargo test`; anything else has no
// summary rule this Worker can decide, so publish refuses it
// (acceptance_runner_unsupported) instead of publishing a clause that would be
// permanently unverified. The runtime repeat of the refusal is defence in depth
// for a clause frozen before that gate existed.
func acceptanceRunnerOf(tokens []string) acceptanceRunner {
	switch {
	case hasTokenPrefix(tokens, "go", "test"):
		return runnerGoTest
	case hasTokenPrefix(tokens, "pytest"):
		return runnerPytest
	case hasTokenPrefix(tokens, "python", "-m", "pytest"), hasTokenPrefix(tokens, "python3", "-m", "pytest"):
		return runnerPytest
	case hasTokenPrefix(tokens, "node", "--test"):
		return runnerNodeTest
	case hasTokenPrefix(tokens, "cargo", "test"):
		return runnerCargoTest
	default:
		return ""
	}
}

// classifyAcceptanceOutput reports what one execution's projected output shows.
// A failure signature takes precedence over any passing summary: an output that
// prints both did not pass. `0 failed` / `0 errors` are not failure signatures -
// they are the counters every passing summary carries.
func classifyAcceptanceOutput(runner acceptanceRunner, output string) acceptanceOutput {
	switch runner {
	case runnerGoTest:
		if acceptanceFailLine.MatchString(output) || acceptanceGoSubtest.MatchString(output) ||
			acceptanceCargoFailed.MatchString(output) {
			return acceptanceOutputFailed
		}
		// `go test -json` prints events instead of the plain lines; -v and -json
		// are the two shapes publish accepts, so both are decided here.
		if summary := scanGoTestJSON(output); summary.seen {
			if summary.failed {
				return acceptanceOutputFailed
			}
			if summary.passed {
				return acceptanceOutputPassed
			}
			return acceptanceOutputUnknown
		}
		// A pass needs a per-test `--- PASS:` line: `ok <pkg>` alone is printed
		// by a zero-test package and by an all-skipped one, so it is not
		// evidence that a test ran. `[no tests to run]` is never a pass either.
		if acceptanceGoNoTests.MatchString(output) || !acceptanceGoOKLine.MatchString(output) ||
			!acceptanceGoPassLine.MatchString(output) {
			return acceptanceOutputUnknown
		}
		return acceptanceOutputPassed
	case runnerPytest:
		// The remaining signatures are pytest's own: its FAILED/ERRORS summary
		// lines and its counters. A stray `error:` line is tests' stdout.
		if acceptancePytestFailed.MatchString(output) || positiveCounter(acceptanceFailCounter, output) {
			return acceptanceOutputFailed
		}
		if positiveCounter(acceptancePassCounter, output) {
			return acceptanceOutputPassed
		}
		return acceptanceOutputUnknown
	case runnerNodeTest:
		if acceptanceNotOKLine.MatchString(output) {
			return acceptanceOutputFailed
		}
		// Only the FINAL summary block counts. Summing every `pass N` in the
		// output would let a test that prints `# pass 1` satisfy the clause.
		tests, passed, failed, cancelled, ok := acceptanceNodeFinalCounters(output)
		if !ok {
			return acceptanceOutputUnknown
		}
		if failed > 0 || cancelled > 0 {
			return acceptanceOutputFailed
		}
		if passed < 1 || passed > tests {
			return acceptanceOutputUnknown
		}
		return acceptanceOutputPassed
	case runnerCargoTest:
		// `cargo test` prints one `test result:` line per target, so the first
		// line is not the whole answer: any FAILED result anywhere fails the run,
		// and a pass is a positive `N passed` in any result line.
		if acceptanceCargoFailed.MatchString(output) || acceptanceErrorLine.MatchString(output) {
			return acceptanceOutputFailed
		}
		if positiveCounter(acceptanceCargoPassCount, output) {
			return acceptanceOutputPassed
		}
		return acceptanceOutputUnknown
	default:
		return acceptanceOutputUnknown
	}
}

// scanGoTestJSON reads the events of a `go test -json` run. A pass counts only
// for an event that names a Test: the package-level pass event is printed for a
// package with no tests at all. A fail event (test or package scope) is a
// failing run.
func scanGoTestJSON(output string) goTestJSONSummary {
	var summary goTestJSONSummary
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") || !strings.Contains(line, `"Action"`) {
			continue
		}
		var event struct {
			Action  string
			Test    string
			Package string
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Package == "" {
			continue
		}
		summary.seen = true
		switch event.Action {
		case "pass":
			if event.Test != "" {
				summary.passed = true
			}
		case "fail":
			summary.failed = true
		}
	}
	return summary
}

// acceptanceNodeFinalCounters reads the last summary block of a `node --test`
// run: the last `tests N`, `pass N`, `fail N` and `cancelled N` line, matching
// TAP (`# pass 1`) and the spec reporter (`ℹ pass 1`). No counter is summed, so
// a test that prints `# pass 1` cannot add itself to the summary. ok is false
// when any of the four is missing: a partial block is not a summary.
func acceptanceNodeFinalCounters(output string) (tests, passed, failed, cancelled int, ok bool) {
	tests, haveTests := acceptanceLastNodeCounter(output, "tests")
	passed, havePass := acceptanceLastNodeCounter(output, "pass")
	failed, haveFail := acceptanceLastNodeCounter(output, "fail")
	cancelled, haveCancelled := acceptanceLastNodeCounter(output, "cancelled")
	if !haveTests || !havePass || !haveFail || !haveCancelled {
		return 0, 0, 0, 0, false
	}
	return tests, passed, failed, cancelled, true
}

// acceptanceLastNodeCounter returns the value of the last counter line for key.
func acceptanceLastNodeCounter(output, key string) (int, bool) {
	value, found := 0, false
	for _, match := range acceptanceNodeCounter.FindAllStringSubmatch(output, -1) {
		if match[1] != key {
			continue
		}
		count, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		value, found = count, true
	}
	return value, found
}

// executionOutputCut reports that the recorded output is not the whole projected
// output: either the reader flagged it, or one of the truncation markers the
// producers write is inside it.
func executionOutputCut(execution domain.ToolExecution) bool {
	return execution.Truncated || strings.Contains(execution.Output, acceptanceTruncationMarker)
}

// acceptanceRetainedTail splits a recorded output at its last tail marker and
// returns what follows it: the bytes that are the true END of the run. retained is
// false for an output that carries no tail marker, which means either that nothing
// was cut (the whole output is returned) or that only a head was kept (the whole
// output is still returned, and the caller must not read it as a verdict).
//
// Reading the end is what makes a long, verbose test run decidable: a runner
// prints its summary last, so the end of the output is the part that says whether
// the suite passed. The head being cut matters for the opposite reason - a failure
// line could be in the omitted middle - which is why a retained tail is only
// trusted together with a recorded success (see the leaf below).
func acceptanceRetainedTail(output string) (tail string, retained bool) {
	index := strings.LastIndex(output, domain.OutputTailTruncationMarker)
	if index < 0 {
		return output, false
	}
	return output[index+len(domain.OutputTailTruncationMarker):], true
}

// executionExitStatus reports the recorded exit status of one execution, and
// whether it is known at all.
//
// domain.ExitCodeUnrecorded is "unknown", never a value: rows written before the
// recorder wrote the column, and calls that never ran a process, carry it, and a
// reader must not read it as success or as failure. Every other value is the
// status itself: 0 for success, the process's own non-zero code, 128+signal for
// a signal death, and the negative sentinels domain.ExitCodeTimedOut /
// domain.ExitCodeCancelled for a process that never completed. The leaf does not
// need to know which non-zero value it got: any of them means the run did not
// pass, and the detail names the value it decided on.
func executionExitStatus(execution domain.ToolExecution) (int, bool) {
	if execution.ExitCode == domain.ExitCodeUnrecorded {
		return 0, false
	}
	return execution.ExitCode, true
}

// evaluateAcceptanceTestsPassed decides tests-passed from recorded executions
// only. The declared command must be anchored to one execution of the child Run;
// nothing is executed here and the child's prose is never read.
func evaluateAcceptanceTestsPassed(ctx context.Context, check string, spec domain.FlowAcceptance,
	executions AcceptanceExecutions) AcceptanceCheck {
	if executions == nil {
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceUnavailable,
			"recorded shell executions are unavailable")
	}
	if token := compoundCommandToken(spec.Command); token != "" {
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceCommandCompound,
			fmt.Sprintf("the declared command contains shell control syntax (%q) and cannot be anchored to a single recorded execution", token))
	}
	declared := acceptanceInvocationKey(ParseCheckCommand(spec.Command))
	if len(declared) == 0 {
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceNoExecution,
			"tests-passed declares no command to match")
	}
	runner := acceptanceRunnerOf(declared)
	if runner == "" {
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceRunnerUnsupported,
			fmt.Sprintf("%q is not a supported test runner (go test, pytest, python -m pytest, node --test, cargo test)", strings.Join(declared, " ")))
	}
	if err := ctx.Err(); err != nil {
		return acceptanceEvidenceFailure(check, err)
	}
	execution, found := matchingExecution(executions.Executions(), declared)
	if !found {
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceNoExecution,
			fmt.Sprintf("the task recorded no shell execution of %q", strings.Join(declared, " ")))
	}
	// The verdict is read off the end of the output, where every runner prints its
	// summary; when nothing was cut this is the whole output.
	tail, tailRetained := acceptanceRetainedTail(execution.Output)
	outcome := classifyAcceptanceOutput(runner, tail)
	status, statusKnown := executionExitStatus(execution)
	switch {
	case statusKnown && status != 0:
		// A recorded exit status decides, and it is checked before anything the
		// output says: a runner prints its pass summary before it exits, so a
		// summary in the visible output is not evidence about a process that then
		// failed, and an incomplete output does not make a known non-zero status
		// undecidable either.
		return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceCommandFailed,
			fmt.Sprintf("the execution of %q exited with status %d", strings.Join(declared, " "), status))
	case outcome == acceptanceOutputFailed:
		return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceCommandFailed,
			fmt.Sprintf("the output of %q shows a failing test run", strings.Join(declared, " ")))
	case execution.IsError:
		// The recorded tool-level failure fact. It is the only failure signal an
		// execution with an unknown exit status has (a row written before the
		// recorder wrote the column), and it stays authoritative next to a known
		// status because a tool can fail without the process exiting, e.g. when
		// publishing its retained output failed.
		return newAcceptanceCheck(check, domain.AcceptanceDoesNotHold, codeAcceptanceCommandFailed,
			fmt.Sprintf("the execution of %q ended in error", strings.Join(declared, " ")))
	case executionOutputCut(execution) && !tailRetained:
		// Only the head was kept: the omitted bytes can hide the summary, or the
		// failure, the verdict is about, so a visible passing head proves nothing.
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceTruncated,
			"the recorded output was truncated and only its head was retained, so the omitted middle cannot decide the run")
	case executionOutputCut(execution) && !statusKnown:
		// The end of the output IS retained, but its head was cut, and no exit
		// status was recorded: an earlier failure line could be in the omitted
		// middle, and without a recorded success the tail alone cannot outvote that
		// possibility. A run that recorded its exit status does not land here.
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceTruncated,
			"the recorded output was cut and no exit status was recorded, so its retained tail cannot decide the run on its own")
	case outcome == acceptanceOutputPassed:
		return newAcceptanceCheck(check, domain.AcceptanceHolds, "", "")
	default:
		return newAcceptanceCheck(check, domain.AcceptanceUnverified, codeAcceptanceNoPassSummary,
			fmt.Sprintf("the recorded output of %q shows no passing test summary (an empty run, only skipped/todo tests, or a run that printed nothing)", strings.Join(declared, " ")))
	}
}
