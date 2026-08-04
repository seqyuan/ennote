package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
