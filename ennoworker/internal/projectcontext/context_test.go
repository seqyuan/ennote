package projectcontext

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_GlobalAGENTS(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("global rules"), 0o644))

	ctx, err := Load(SecurityContext{Trusted: false}, home)
	require.NoError(t, err)
	assert.Equal(t, "global rules", ctx.GlobalAGENTS)
	assert.Empty(t, ctx.ProjectAGENTS)
	assert.Empty(t, ctx.ProjectMEMORY)
}

func TestLoad_TrustedProjectFiles(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("project rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "MEMORY.md"), []byte("project background"), 0o644))

	ctx, err := Load(SecurityContext{CanonicalRoot: ws, Trusted: true}, home)
	require.NoError(t, err)
	assert.Equal(t, "project rules", ctx.ProjectAGENTS)
	assert.Equal(t, "project background", ctx.ProjectMEMORY)
}

func TestLoad_UntrustedSkipsProjectFiles(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("sensitive"), 0o644))

	ctx, err := Load(SecurityContext{CanonicalRoot: ws, Trusted: false}, home)
	require.NoError(t, err)
	assert.Empty(t, ctx.ProjectAGENTS)
	assert.Empty(t, ctx.ProjectMEMORY)
}

func TestLoad_CaseSensitiveMatch(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "project")
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(ws, 0o755))
	// Write lowercase variant
	require.NoError(t, os.WriteFile(filepath.Join(ws, "agents.md"), []byte("lowercase"), 0o644))

	ctx, err := Load(SecurityContext{CanonicalRoot: ws, Trusted: true}, home)
	require.NoError(t, err)
	// Must NOT match lowercase agents.md as AGENTS.md
	assert.Empty(t, ctx.ProjectAGENTS)
}

func TestBuildPrompt_Order(t *testing.T) {
	ctx := &Context{
		ProjectMEMORY: "memory content",
		GlobalAGENTS:  "global rules",
		ProjectAGENTS: "project rules",
	}
	prompt := ctx.BuildPrompt("You are helpful.", "<available_skills>\n</available_skills>")

	// MEMORY before global AGENTS before project AGENTS before catalog
	memIdx := indexOf(prompt, "Project Memory")
	globalIdx := indexOf(prompt, "Global Instructions")
	projectIdx := indexOf(prompt, "Project Instructions")
	catalogIdx := indexOf(prompt, "<available_skills>")

	assert.True(t, memIdx < globalIdx, "MEMORY before global AGENTS")
	assert.True(t, globalIdx < projectIdx, "global AGENTS before project AGENTS")
	assert.True(t, projectIdx < catalogIdx, "project AGENTS before catalog")
}

func TestBuildPrompt_BaseOnly(t *testing.T) {
	ctx := &Context{}
	prompt := ctx.BuildPrompt("Base prompt.", "")
	assert.Equal(t, "Base prompt.", prompt)
}

func TestBuildPrompt_EmptyContext(t *testing.T) {
	ctx := &Context{}
	prompt := ctx.BuildPrompt("Base.", "catalog")
	assert.Contains(t, prompt, "Base.")
	assert.Contains(t, prompt, "catalog")
}

func TestTotalBytes(t *testing.T) {
	ctx := &Context{
		GlobalAGENTS:  "1234567890",      // 10 bytes
		ProjectMEMORY: "12345",           // 5 bytes
		ProjectAGENTS: "123456789012345", // 15 bytes
	}
	assert.Equal(t, 30, ctx.TotalBytes())
}

func TestReadExactFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Test.md"), []byte("content"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.md"), []byte("lowercase"), 0o644))

	data, err := readExactFile(dir, "Test.md")
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))

	data, err = readExactFile(dir, "TEST.md")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
