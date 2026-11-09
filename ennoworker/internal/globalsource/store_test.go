package globalsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGraphCreateListUpdateAndConflict(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	document, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	assert.Empty(t, document.Tasks)
	assert.NotEmpty(t, digest)

	entries, err := store.ListGraphs()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "RNA-seq", entries[0].Name)
	assert.Empty(t, entries[0].Error)

	updated, nextDigest, err := store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Tasks["align"] = graphsource.Task{Name: "Align", Model: "anthropic/claude-sonnet-4", Goal: "Align reads"}
		document.Graph["align"] = []string{}
		return nil
	})
	require.NoError(t, err)
	assert.Contains(t, updated.Tasks, "align")
	assert.NotEqual(t, digest, nextDigest)

	_, currentDigest, err := store.UpdateGraph("rna-seq", digest, func(*graphsource.Document) error { return nil })
	assert.ErrorIs(t, err, ErrConflict)
	assert.Equal(t, nextDigest, currentDigest)
}

func TestGlobalCatalogDoesNotReadGraphPrivateResources(t *testing.T) {
	home := t.TempDir()
	store := Store{HomeDir: home}
	_, _, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	privateHero := filepath.Join(home, "agents", "graphs", "rna-seq", "heroes", "private-hero")
	require.NoError(t, os.MkdirAll(privateHero, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(privateHero, "hero.md"), []byte("private secret that must not be parsed"), 0o600))

	heroes, err := store.ListHeroes()
	require.NoError(t, err)
	assert.Empty(t, heroes)
	graphs, err := store.ListGraphs()
	require.NoError(t, err)
	require.Len(t, graphs, 1)
	assert.Empty(t, graphs[0].Error)
}

func TestCatalogRejectsSymlinkedObjectAndSource(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "agents", "graphs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "graph.yaml"), []byte("outside"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(home, "agents", "graphs", "escaped")))

	store := Store{HomeDir: home}
	entries, err := store.ListGraphs()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.NotEmpty(t, entries[0].Error)
	assert.Nil(t, entries[0].Document)
}

func TestStoreRejectsTraversalAndMismatchedDirectoryIdentity(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, _, err := store.CreateGraph("../escape", "Escape")
	require.Error(t, err)

	_, _, err = store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	path := filepath.Join(store.GraphsDir(), "rna-seq", "graph.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	data = []byte("schema_version: 1\nid: other-graph\nname: Other\ntasks: {}\ngraph: {}\n")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	_, _, err = store.ReadGraph("rna-seq")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match directory")
}

func TestStoreReportsCorruptHeroAndGraphAsEntryErrors(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	heroDir := filepath.Join(store.HeroesDir(), "broken-hero")
	require.NoError(t, os.MkdirAll(heroDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(heroDir, "hero.md"), []byte("---\nnot: valid: yaml\n"), 0o600))
	graphDir := filepath.Join(store.GraphsDir(), "broken-graph")
	require.NoError(t, os.MkdirAll(graphDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(graphDir, "graph.yaml"), []byte("schemaVersion: 1\n"), 0o600))

	heroes, err := store.ListHeroes()
	require.NoError(t, err)
	found := false
	for _, entry := range heroes {
		if entry.ID == "broken-hero" {
			found = true
			assert.NotEmpty(t, entry.Error)
		}
	}
	assert.True(t, found, "corrupt Hero must be reported with a parse error, not dropped")

	graphs, err := store.ListGraphs()
	require.NoError(t, err)
	found = false
	for _, entry := range graphs {
		if entry.ID == "broken-graph" {
			found = true
			assert.NotEmpty(t, entry.Error)
		}
	}
	assert.True(t, found, "corrupt Graph must be reported with a parse error, not dropped")
}
