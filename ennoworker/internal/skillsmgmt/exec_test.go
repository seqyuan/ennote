package skillsmgmt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchUsesAPIAndSortsByInstalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/search", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"skills":[
			{"id":"org/low","name":"low","source":"org","installs":5},
			{"id":"org/hot","name":"hot","source":"org","installs":12500}
		]}`))
	}))
	defer server.Close()

	results, err := Search(context.Background(), "search", 10, server.URL)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "org@hot", results[0].Package)
	assert.Equal(t, "12.5K installs", results[0].Installs)
	assert.Equal(t, server.URL+"/org/hot", results[0].URL)
	assert.Equal(t, "org@low", results[1].Package)
}

func TestSearchFallsBackToNpxFind(t *testing.T) {
	fakeNpx := filepath.Join(t.TempDir(), "npx")
	script := "#!/bin/sh\necho 'foo/bar 1.2K installs'\necho 'https://skills.sh/foo/bar'"
	require.NoError(t, os.WriteFile(fakeNpx, []byte(script), 0o755))
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", filepath.Dir(fakeNpx)+":"+oldPath)

	// API base that always fails (unreachable port).
	results, err := Search(context.Background(), "bar", 10, "http://127.0.0.1:1")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "foo/bar", results[0].Package)
}

func TestInstallRunsNpxWithAgentFlag(t *testing.T) {
	var got []string
	var gotCwd string
	runNpx = func(_ context.Context, args []string, _ time.Duration, cwd string, _ []string) (string, error) {
		got = args
		gotCwd = cwd
		return "Installation complete", nil
	}
	defer func() { runNpx = realRunNpx }()

	out, err := Install(context.Background(), "acme/skills@web", "global", "")
	require.NoError(t, err)
	assert.Equal(t, "acme/skills@web", got[2])
	assert.Contains(t, got, "-y")
	assert.Contains(t, got, "--agent")
	assert.Contains(t, got, "-g")
	_ = out

	_, err = Install(context.Background(), "acme/skills@web", "project", "/tmp/proj")
	require.NoError(t, err)
	assert.NotContains(t, got, "-g")
	assert.Equal(t, "/tmp/proj", gotCwd)
}

func TestInstallFailsOnUnrecognizedOutput(t *testing.T) {
	runNpx = func(_ context.Context, _ []string, _ time.Duration, _ string, _ []string) (string, error) {
		return "ERROR Missing repository", nil
	}
	defer func() { runNpx = realRunNpx }()
	_, err := Install(context.Background(), "acme/skills@web", "global", "")
	assert.ErrorContains(t, err, "install failed")
}

func TestCheckUpdatesGlobalUpToDate(t *testing.T) {
	_ = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sha":"rootsha","tree":[]}`))
	}))

	install := InstallInfo{
		Package: "acme/skills@web", Scope: "global", Source: "acme/skills",
		SourceType: "github", SkillPath: "web/SKILL.md", VersionHash: "abc123", CanCheckForUpdates: true,
	}
	orig := fetchGitHubTreeHash
	defer func() { fetchGitHubTreeHash = orig }()
	fetchGitHubTreeHash = func(_ context.Context, apiURL, token, folder string) (string, error) {
		assert.Contains(t, apiURL, "acme/skills")
		assert.Equal(t, "web", folder)
		return "abc123", nil
	}
	results := CheckUpdates(context.Background(), []InstallInfo{install}, "", "http://unused")
	require.Len(t, results, 1)
	assert.Equal(t, UpdateUpToDate, results[0].State)
	assert.Equal(t, "abc123", results[0].LatestVersion)
}

func TestCheckUpdatesSkipsUnsupported(t *testing.T) {
	results := CheckUpdates(context.Background(), []InstallInfo{
		{Package: "x@y", Scope: "global", CanCheckForUpdates: false},
	}, "", "")
	require.Len(t, results, 1)
	assert.Equal(t, UpdateUnsupported, results[0].State)
}

func TestUpdateBuildsArgs(t *testing.T) {
	var got []string
	runNpx = func(_ context.Context, args []string, _ time.Duration, _ string, _ []string) (string, error) {
		got = args
		return "Installed 1 skill", nil
	}
	defer func() { runNpx = realRunNpx }()

	_, err := Update(context.Background(), InstallInfo{
		Package: "acme/skills@web", Scope: "global", Source: "acme/skills",
		SkillPath: "skills/web/SKILL.md", VersionHash: "abc", CanCheckForUpdates: true,
	}, "")
	require.NoError(t, err)
	joined := strings.Join(got, " ")
	assert.Contains(t, joined, "acme/skills/skills/web")
	assert.Contains(t, joined, "--skill")
	assert.Contains(t, joined, "web")
	assert.Contains(t, joined, "-g")
}

func TestSkillHelpers(t *testing.T) {
	assert.Equal(t, "web", skillNameFromPackage("acme/skills@web"))
	assert.Equal(t, "web-search", skillSlug("Web Search"))
	assert.Equal(t, "skills/web", skillFolder("skills/web/SKILL.md"))
	assert.Equal(t, "", skillFolder(""))
}

func TestParseFindOutput(t *testing.T) {
	raw := "foo/bar 1.2K installs\n└ https://skills.sh/foo/bar\nbaz/qux 3 installs\n"
	results := parseFindOutput(raw)
	require.Len(t, results, 2)
	assert.Equal(t, "https://skills.sh/foo/bar", results[0].URL)
	assert.Equal(t, "baz/qux", results[1].Package)
}
