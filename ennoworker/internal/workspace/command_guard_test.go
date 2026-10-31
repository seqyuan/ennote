package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

// guardedHome is the Ennote home used by the table below. It is a literal path
// so the expectations do not depend on the machine running the test.
const guardedHome = "/home/user/.ennote"

func guardManager() *Manager {
	return &Manager{Mode: SandboxNone, ProtectedPaths: []string{guardedHome}}
}

func TestValidateCommandDegradedGuard(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		wantErr string // substring the rejection must carry; "" means allowed
	}{
		// Paths the shell assembles from a plain-looking command.
		{name: "backslash escape", command: `cat /home/user/\.ennote/config/x`, wantErr: "cannot access the Ennote home"},
		{name: "empty quote pair", command: `cat /home/user/.en''note/config/x`, wantErr: "cannot access the Ennote home"},
		{name: "double quoted empty pair", command: `cat /home/user/.en""note/config/x`, wantErr: "cannot access the Ennote home"},
		{name: "unquoted glob", command: `cat /home/us?r/.ennote/config/x`, wantErr: "unquoted glob pattern"},
		{name: "star glob", command: `cat /home/user/.enn*/config/x`, wantErr: "unquoted glob pattern"},
		{name: "brace expansion", command: `cat /home/user/.enn{ote,ote}/config/x`, wantErr: "brace expansion"},
		{name: "escaped brace expansion", command: `cat /home/user/.enn\{ote,ote\}/config/x`, wantErr: "brace expansion"},
		{name: "ansi-c quoting", command: `cat $'\x2fhome\x2fuser\x2f.ennote\x2fx'`, wantErr: "ANSI-C quoting"},
		{name: "parameter expansion", command: `cat "$HOME/.ennote/config/x"`, wantErr: "parameter expansion"},
		{name: "unrelated variable", command: `echo $PATH`, wantErr: "parameter expansion"},
		{name: "command substitution", command: `cat $(echo /home/user/.ennote/config/x)`, wantErr: "command substitution"},
		{name: "process substitution", command: `cat <(echo hello)`, wantErr: "process substitution"},
		{name: "arithmetic expansion", command: `cat $((1+2))`, wantErr: "arithmetic expansion"},
		{name: "tilde prefix", command: `cat ~/.ennote/config/x`, wantErr: "cannot access the Ennote home"},
		{name: "any tilde prefix", command: `cat ~/notes.txt`, wantErr: "cannot access the Ennote home"},
		{name: "bare home tilde", command: `cat ~/notes`, wantErr: "cannot access the Ennote home"},
		{name: "bare tilde", command: `ls ~`, wantErr: "cannot access the Ennote home"},
		// A quoted tilde is literal text: the shell only expands a leading
		// unquoted ~, so these name ordinary files.
		{name: "double quoted tilde", command: `grep -rn "~backup" notes.txt`},
		{name: "single quoted tilde", command: `cat '~notes'`},
		{name: "tilde not at word start", command: `echo foo~bar`},
		{name: "quoted home literal still names the home", command: `cat "~/.ennote/x"`, wantErr: "cannot access the Ennote home"},
		// A bare {} is find's placeholder, not a brace expansion.
		{name: "find exec placeholder", command: `find . -name '*.go' -exec gofmt -l {} +`},
		{name: "literal braces without comma", command: `find . -name '*.go' -exec sh -c 'echo {x}' {} +`},
		{name: "plain literal path", command: `cat /home/user/.ennote/config/x`, wantErr: "cannot access the Ennote home"},
		{name: "home name elsewhere", command: `cat /root/.ennote/data/catalog.db`, wantErr: "cannot access the Ennote home"},
		// P1b: a plain syntax error is not a home-path violation.
		{name: "parse error", command: `cat 'unterminated`, wantErr: "shell syntax error"},

		// Ordinary degraded-mode commands must keep working.
		{name: "git status", command: `git status --porcelain`},
		{name: "ls", command: `ls -la`},
		{name: "cat relative", command: `cat notes.txt`},
		{name: "go test", command: `go test ./...`},
		{name: "quoted glob is literal", command: `git log --grep='fix*'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := guardManager().ValidateCommand(tc.command)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateCommand(%q) = %v, want nil", tc.command, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateCommand(%q) = nil, want error containing %q", tc.command, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateCommand(%q) = %q, want it to contain %q", tc.command, err, tc.wantErr)
			}
		})
	}
}

func TestValidateCommandParseErrorIsNotAHomeViolation(t *testing.T) {
	err := guardManager().ValidateCommand(`cat 'unterminated`)
	if err == nil {
		t.Fatal("unterminated quote was accepted")
	}
	message := err.Error()
	if !strings.Contains(message, "shell syntax error") {
		t.Fatalf("parse failure = %q, want a syntax error message", message)
	}
	if !strings.Contains(message, "ENNOTE_SANDBOX=bwrap") {
		t.Fatalf("parse failure = %q, want a remediation hint", message)
	}
}

func TestValidateCommandExpansionAndHomeErrorsDiffer(t *testing.T) {
	manager := guardManager()
	expansion := manager.ValidateCommand(`echo $PATH`)
	if expansion == nil {
		t.Fatal("expansion was accepted")
	}
	home := manager.ValidateCommand("cat " + guardedHome + "/config/x")
	if home == nil {
		t.Fatal("protected path was accepted")
	}
	if expansion.Error() == home.Error() {
		t.Fatalf("expansion and protected-path rejections share one message: %q", home.Error())
	}
	if !strings.Contains(expansion.Error(), "parameter expansion") {
		t.Fatalf("expansion rejection = %q, want it to name the construct", expansion.Error())
	}
}

func TestValidateCommandSkippedForIsolatedSandbox(t *testing.T) {
	manager := guardManager()
	manager.Mode = SandboxBubblewrap
	for _, command := range []string{"cat " + guardedHome + "/config/x", "echo $PATH", `cat 'unterminated`} {
		if err := manager.ValidateCommand(command); err != nil {
			t.Fatalf("bwrap mode rejected %q: %v", command, err)
		}
	}
}

func TestValidateArgvRejectsOnlyLiteralPaths(t *testing.T) {
	manager := guardManager()
	for _, tc := range []struct {
		argv    []string
		wantErr bool
	}{
		{argv: []string{"cat", guardedHome + "/config/provider-auth.json"}, wantErr: true},
		{argv: []string{"cat", "/root/.ennote/data/catalog.db"}, wantErr: true},
		{argv: []string{"echo", "$PATH"}},
		{argv: []string{"echo", "~/.ennote"}, wantErr: true}, // exec never expands ~; reject anyway
		{argv: []string{"git", "log", "--format=%H"}},
	} {
		err := manager.ValidateArgv(tc.argv)
		if tc.wantErr && err == nil {
			t.Fatalf("ValidateArgv(%v) = nil, want rejection", tc.argv)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("ValidateArgv(%v) = %v, want nil", tc.argv, err)
		}
	}
}

func TestProtectedPathsAreCleanedOnce(t *testing.T) {
	manager := &Manager{Mode: SandboxNone}
	home := filepath.Join(t.TempDir(), ".ennote")
	manager.ProtectHome(home)
	if err := manager.ValidateCommand("cat " + filepath.Join(home, "config", "x")); err == nil {
		t.Fatal("literal protected path was accepted")
	}
	if err := manager.ValidateCommand("cat " + home + "/../elsewhere/notes.txt"); err == nil {
		t.Fatal("path escaping the home directory was accepted")
	}
}
