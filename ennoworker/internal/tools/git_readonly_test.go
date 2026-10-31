package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func initGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
		run("add", name)
	}
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestGitReadonlyEnvOmitsHostSecrets(t *testing.T) {
	t.Setenv("ENNOTE_SECRET_SENTINEL", "super-secret")
	env := gitReadonlyEnv(t.TempDir())
	for _, entry := range env {
		assert.NotContains(t, entry, "super-secret")
		assert.False(t, strings.HasPrefix(entry, "ENNOTE_SECRET_SENTINEL="))
	}
	assert.Contains(t, env, "GIT_CONFIG_GLOBAL=/dev/null")
}

func TestGitReadonlyStatusDiffLog(t *testing.T) {
	dir := initGitRepo(t, map[string]string{"README.md": "# repo\n"})
	tool := &GitReadonlyTool{WorkingDir: dir, MaxOutput: 64 * 1024}

	status, err := tool.Execute(context.Background(), domain.ToolCall{ID: "c1", Name: "git_readonly",
		Arguments: []byte(`{"subcommand":"status","args":["--short"]}`)})
	require.NoError(t, err)
	assert.False(t, status.IsError)
	assert.Contains(t, status.Content, "")

	log, err := tool.Execute(context.Background(), domain.ToolCall{ID: "c2", Name: "git_readonly",
		Arguments: []byte(`{"subcommand":"log","args":["--oneline","-1"]}`)})
	require.NoError(t, err)
	assert.False(t, log.IsError)
	assert.Contains(t, log.Content, "initial")
}

func TestGitReadonlyRejectsMutationSubcommandsAndDangerousArgs(t *testing.T) {
	dir := initGitRepo(t, map[string]string{"README.md": "# repo\n"})
	tool := &GitReadonlyTool{WorkingDir: dir}

	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "push", args: `{"subcommand":"push"}`},
		{name: "commit", args: `{"subcommand":"commit"}`},
		{name: "checkout", args: `{"subcommand":"checkout","args":["master"]}`},
		{name: "reset", args: `{"subcommand":"reset","args":["--hard"]}`},
		{name: "config override", args: `{"subcommand":"log","args":["-c","user.email=x"]}`},
		{name: "work-tree redirect", args: `{"subcommand":"status","args":["--work-tree=/tmp"]}`},
		{name: "output redirect", args: `{"subcommand":"log","args":["--output=/tmp/x"]}`},
		{name: "external diff", args: `{"subcommand":"diff","args":["--ext-diff"]}`},
		{name: "text conversion", args: `{"subcommand":"show","args":["--textconv","HEAD:README.md"]}`},
		{name: "outside no-index", args: `{"subcommand":"diff","args":["--no-index","/etc/hosts","/etc/passwd"]}`},
		{name: "blame external contents", args: `{"subcommand":"blame","args":["--contents=/etc/passwd","README.md"]}`},
		{name: "abbreviated external diff", args: `{"subcommand":"diff","args":["--ext-d"]}`},
		{name: "abbreviated textconv", args: `{"subcommand":"show","args":["--textcon","HEAD:README.md"]}`},
		{name: "abbreviated no-index", args: `{"subcommand":"diff","args":["--no-in","/etc/hosts","/etc/passwd"]}`},
		{name: "abbreviated git-dir", args: `{"subcommand":"log","args":["--git-di=/tmp"]}`},
		{name: "abbreviated pathspec file", args: `{"subcommand":"ls-files","args":["--pathspec-from-fil=f"]}`},
		{name: "blame orderfile", args: `{"subcommand":"blame","args":["-Oorderfile","README.md"]}`},
		{name: "branch removed", args: `{"subcommand":"branch","args":["new-branch"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "c", Name: "git_readonly", Arguments: []byte(tc.args)})
			require.NoError(t, err)
			assert.True(t, result.IsError, "expected rejection for %s", tc.args)
			assert.NotContains(t, result.Content, "Author")
		})
	}
}

func TestGitReadonlyNeverRunsRepositoryDiffDriver(t *testing.T) {
	dir := initGitRepo(t, map[string]string{"README.md": "old\n"})
	marker := filepath.Join(t.TempDir(), "executed")
	helper := filepath.Join(dir, "diff-helper.sh")
	require.NoError(t, os.WriteFile(helper, []byte("#!/bin/sh\nprintf executed > \""+marker+"\"\nprintf helper-output\n"), 0o700))
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("config", "diff.evil.command", helper)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.md diff=evil\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("new\n"), 0o600))

	tool := &GitReadonlyTool{WorkingDir: dir}
	result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "safe", Name: "git_readonly",
		Arguments: []byte(`{"subcommand":"diff"}`)})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content, "+new")
	assert.NotContains(t, result.Content, "helper-output")
	_, statErr := os.Stat(marker)
	assert.ErrorIs(t, statErr, os.ErrNotExist)

	rejected, err := tool.Execute(context.Background(), domain.ToolCall{ID: "blocked", Name: "git_readonly",
		Arguments: []byte(`{"subcommand":"diff","args":["--ext-diff"]}`)})
	require.NoError(t, err)
	assert.True(t, rejected.IsError)
	_, statErr = os.Stat(marker)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

// TestGitReadonlyDeniedOptionMatching pins the option deny list against git's
// long-option abbreviation rules: any unambiguous prefix of a denied option is
// the denied option as far as git is concerned.
func TestGitReadonlyDeniedOptionMatching(t *testing.T) {
	for _, tc := range []struct {
		name     string
		argument string
		denied   string // "" means the argument must be allowed
	}{
		// Exact forms.
		{name: "ext-diff", argument: "--ext-diff", denied: "--ext-diff"},
		{name: "textconv", argument: "--textconv", denied: "--textconv"},
		{name: "no-index", argument: "--no-index", denied: "--no-index"},
		{name: "git-dir", argument: "--git-dir", denied: "--git-dir"},
		{name: "output", argument: "--output", denied: "--output"},
		{name: "output equals", argument: "--output=/tmp/x", denied: "--output"},
		{name: "exec", argument: "--exec", denied: "--exec"},
		{name: "open-files-in-pager", argument: "--open-files-in-pager", denied: "--open-files-in-pager"},
		{name: "short output", argument: "-o", denied: "-o"},
		{name: "short config attached", argument: "-cuser.email=x", denied: "-c"},
		{name: "short orderfile", argument: "-O", denied: "-O"},

		// Combined diff: a bare -c/-cc is not the global config form.
		{name: "combined diff", argument: "-c"},
		{name: "doubled combined diff", argument: "-cc"},

		// Unambiguous long-option abbreviations (the reported bypass).
		{name: "abbrev ext-diff", argument: "--ext-d", denied: "--ext-diff"},
		{name: "abbrev textconv", argument: "--textcon", denied: "--textconv"},
		{name: "abbrev no-index", argument: "--no-in", denied: "--no-index"},
		{name: "abbrev git-dir with value", argument: "--git-di=/x", denied: "--git-dir"},
		{name: "abbrev pathspec file", argument: "--pathspec-from-fil=f", denied: "--pathspec-from-file"},
		{name: "abbrev orderfile", argument: "-Oorderfile", denied: "-O"},
		{name: "abbrev pager", argument: "--open-files-in-pag", denied: "--open-files-in-pager"},
		{name: "prefix that a denied option extends", argument: "--wor", denied: "--work-tree"},
		// --exclude-from reads a host file, so it and its abbreviations stay
		// denied, while the plain --exclude path filter is legitimate.
		{name: "exclude-from", argument: "--exclude-from=x", denied: "--exclude-from"},
		{name: "abbrev exclude-from", argument: "--exclude-f=x", denied: "--exclude-from"},
		{name: "trimmed exclude-from", argument: "--exclud", denied: "--exclude-from"},

		// Legitimate read-only flags must keep working.
		{name: "stat", argument: "--stat"},
		{name: "no-color", argument: "--no-color"},
		{name: "short lines", argument: "-n"},
		{name: "format", argument: "--format=%H"},
		{name: "oneline", argument: "--oneline"},
		{name: "short status", argument: "--short"},
		{name: "no-ext-diff", argument: "--no-ext-diff"},
		{name: "no-textconv", argument: "--no-textconv"},
		{name: "exclude filter", argument: "--exclude"},
		{name: "exclude filter with value", argument: "--exclude=*.tmp"},
		{name: "end of options", argument: "--"},
		{name: "pathspec", argument: "README.md"},
		{name: "revision range", argument: "HEAD~2..HEAD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			denied, blocked := deniedGitOption(tc.argument)
			if tc.denied == "" {
				assert.False(t, blocked, "expected %q to be allowed, denied by %q", tc.argument, denied)
				return
			}
			assert.True(t, blocked, "expected %q to be denied as %q", tc.argument, tc.denied)
			assert.Equal(t, tc.denied, denied)
		})
	}
}

// TestGitReadonlyArgListDenials pins the argument-pair rules: git reads a bare
// -c/-cc as a combined diff but "-c key=value" as a global config override, so
// the two forms cannot be told apart from one argument alone.
func TestGitReadonlyArgListDenials(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments []string
		denied    string // "" means the argument list must be allowed
	}{
		{name: "combined diff", arguments: []string{"-c"}},
		{name: "doubled combined diff", arguments: []string{"-cc"}},
		{name: "combined diff with stat", arguments: []string{"-c", "--stat"}},
		{name: "combined diff with revision", arguments: []string{"-cc", "HEAD~1"}},
		{name: "exclude filter", arguments: []string{"--exclude=*.tmp"}},
		{name: "exclude filter then path", arguments: []string{"--exclude=*.tmp", "README.md"}},
		{name: "config override", arguments: []string{"-c", "core.pager=evil"}, denied: "-c <key>=<value>"},
		{name: "attached config override", arguments: []string{"-cuser.email=x"}, denied: "-c"},
		{name: "exclude-from file", arguments: []string{"--exclude-from=x"}, denied: "--exclude-from"},
		{name: "abbreviated exclude-from file", arguments: []string{"--exclude-f=x"}, denied: "--exclude-from"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			denied, blocked := deniedGitArgs(tc.arguments)
			if tc.denied == "" {
				assert.False(t, blocked, "expected %v to be allowed, denied by %q", tc.arguments, denied)
				return
			}
			assert.True(t, blocked, "expected %v to be denied as %q", tc.arguments, tc.denied)
			assert.Equal(t, tc.denied, denied)
		})
	}
}

// TestGitReadonlyRunsCombinedDiffAndExcludeFilters proves the newly allowed
// flags reach git instead of being rejected before the spawn.
func TestGitReadonlyRunsCombinedDiffAndExcludeFilters(t *testing.T) {
	dir := initGitRepo(t, map[string]string{"README.md": "# repo\n", "notes.tmp": "tmp\n"})
	tool := &GitReadonlyTool{WorkingDir: dir}

	for _, tc := range []struct{ name, args string }{
		{name: "combined diff", args: `{"subcommand":"diff","args":["-c","--stat"]}`},
		{name: "combined diff of a merge", args: `{"subcommand":"log","args":["--cc","--oneline","-1"]}`},
		{name: "exclude filter", args: `{"subcommand":"ls-files","args":["--exclude=*.tmp"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "allowed", Name: "git_readonly", Arguments: []byte(tc.args)})
			require.NoError(t, err)
			assert.False(t, result.IsError, "%s should be allowed: %s", tc.args, result.Content)
		})
	}

	blocked, err := tool.Execute(context.Background(), domain.ToolCall{ID: "denied", Name: "git_readonly",
		Arguments: []byte(`{"subcommand":"log","args":["-c","core.pager=evil"]}`)})
	require.NoError(t, err)
	assert.True(t, blocked.IsError, "global -c config override must stay denied")
	assert.Contains(t, blocked.Content, "not allowed")
}

// TestGitReadonlyLongAbbreviationsNeverReachRepositoryPrograms proves the
// end-to-end path: an abbreviated dangerous flag is rejected before git runs,
// so the repository diff driver cannot execute.
func TestGitReadonlyLongAbbreviationsNeverReachRepositoryPrograms(t *testing.T) {
	dir := initGitRepo(t, map[string]string{"README.md": "old\n"})
	marker := filepath.Join(t.TempDir(), "executed")
	helper := filepath.Join(dir, "diff-helper.sh")
	require.NoError(t, os.WriteFile(helper, []byte("#!/bin/sh\nprintf executed > \""+marker+"\"\nprintf helper-output\n"), 0o700))
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("config", "diff.evil.command", helper)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.md diff=evil\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("new\n"), 0o600))

	tool := &GitReadonlyTool{WorkingDir: dir}
	for _, argument := range []string{"--ext-d", "--textcon", "--no-in"} {
		result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "abbrev", Name: "git_readonly",
			Arguments: []byte(`{"subcommand":"diff","args":["` + argument + `"]}`)})
		require.NoError(t, err)
		assert.True(t, result.IsError, "expected rejection for %s", argument)
		assert.NotContains(t, result.Content, "helper-output")
		_, statErr := os.Stat(marker)
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	}

	allowed, err := tool.Execute(context.Background(), domain.ToolCall{ID: "stat", Name: "git_readonly",
		Arguments: []byte(`{"subcommand":"diff","args":["--stat"]}`)})
	require.NoError(t, err)
	assert.False(t, allowed.IsError, "expected --stat to be allowed: %s", allowed.Content)
}
