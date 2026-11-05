package workspace

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// The command guard is the only barrier between a degraded (SandboxNone) shell
// command and the Ennote home, so it is a security boundary built out of string
// surgery on a shell grammar. These fuzz targets pin the properties that make it
// trustworthy: total (never panics), deterministic, sound (an allowed command
// carries no home literal), and not vacuous (an ordinary command is still
// allowed).

const fuzzProtectedHome = "/home/tester/.ennote"

func fuzzManager() *Manager {
	return &Manager{Mode: SandboxNone, ProtectedPaths: []string{fuzzProtectedHome}}
}

// FuzzRejectShellCommandIsTotal checks the two properties every caller relies
// on: arbitrary bytes cannot panic the parser or the walker, and the same input
// always produces the same verdict. A guard that panics on hostile quoting is a
// denial of service; one that answers differently twice cannot be reasoned about.
func FuzzRejectShellCommandIsTotal(f *testing.F) {
	for _, seed := range []string{
		"", "cat notes.txt", "'unterminated", "echo $((1+1))",
		"cat ~/.ennote/config/auth.json", "cat \"$HOME\"/.ennote/x",
		"ls {a,b}", "ls *.go", "sh -c 'cat x'", "cat /home/te''ster/.ennote/x",
		"\x00\xff", "echo $'\\x2f'",
	} {
		f.Add(seed)
	}

	guard := fuzzManager()
	f.Fuzz(func(t *testing.T, command string) {
		first := guard.rejectShellCommand(command)
		second := guard.rejectShellCommand(command)
		if (first == nil) != (second == nil) {
			t.Fatalf("verdict is not deterministic for %q: %v then %v", command, first, second)
		}
		// Soundness: anything the guard allows must be free of a literal home
		// reference under the same normalization it applies as its backstop.
		if first == nil && guard.literalHitsHome(normalizeShellCommand(command)) {
			t.Fatalf("allowed a command whose literal text names the Ennote home: %q", command)
		}
	})
}

// FuzzRejectShellCommandRejectsQuotedHomePaths drives the evasion shapes the
// guard was written for: the same protected path spelled through quoting, an
// empty quote pair inside the name, and escaped separators. Whatever prefix the
// fuzzer prepends, a command carrying the home path in any of those forms must
// be rejected.
func FuzzRejectShellCommandRejectsQuotedHomePaths(f *testing.F) {
	for _, seed := range []string{"", "cat", "ls -la", "grep -r x"} {
		f.Add(seed)
	}

	guard := fuzzManager()
	quoted := func(target string) []string {
		mid := len(target) / 2
		return []string{
			target,
			"'" + target + "'",
			`"` + target + `"`,
			strings.ReplaceAll(target, "/", `\/`),
			target[:mid] + "''" + target[mid:],
			target[:mid] + `""` + target[mid:],
		}
	}

	f.Fuzz(func(t *testing.T, prefix string) {
		for _, target := range quoted(fuzzProtectedHome + "/config/auth.json") {
			command := prefix + " " + target
			if err := guard.rejectShellCommand(command); err == nil {
				t.Fatalf("allowed a home path spelled as %q in %q", target, command)
			}
		}
	})
}

// FuzzRejectShellCommandAllowsOrdinaryPaths is the counter-property: the guard
// protects one path, so it must not become a ban on running commands at all.
func FuzzRejectShellCommandAllowsOrdinaryPaths(f *testing.F) {
	for _, seed := range []string{"notes.txt", "a/b/c.go", "x-y_z.1"} {
		f.Add(seed)
	}

	guard := fuzzManager()
	f.Fuzz(func(t *testing.T, name string) {
		// The point is an ordinary segment: letters, digits and the three path
		// characters. Anything else (metacharacters, invalid UTF-8) is a
		// different question than the one this counter-property asks.
		if name == "" || strings.Contains(name, "..") || !utf8.ValidString(name) {
			t.Skip("not a literal path segment")
		}
		for _, r := range name {
			if r != '.' && r != '_' && r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				t.Skip("not a literal path segment")
			}
		}
		require.NoErrorf(t, guard.rejectShellCommand("cat /tmp/"+name),
			"an ordinary path segment must not be rejected: %q", name)
	})
}
