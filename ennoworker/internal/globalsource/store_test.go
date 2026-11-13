package globalsource

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
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

func TestConcurrentUpdateGraphSameDigestSingleWinner(t *testing.T) {
	const n = 16
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)

	var start sync.WaitGroup
	start.Add(n)
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			start.Done()
			start.Wait()
			_, _, err := store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
				document.Name = fmt.Sprintf("Winner %d", i)
				return nil
			})
			results <- err
		}()
	}

	successes := 0
	for i := 0; i < n; i++ {
		if err := <-results; err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, ErrConflict)
		}
	}
	require.Equal(t, 1, successes, "exactly one same-digest update must win")

	document, finalDigest, err := store.ReadGraph("rna-seq")
	require.NoError(t, err)
	require.NotEqual(t, digest, finalDigest)
	matched := false
	for i := 0; i < n; i++ {
		if document.Name == fmt.Sprintf("Winner %d", i) {
			matched = true
			break
		}
	}
	assert.True(t, matched, "final Graph must reflect exactly one winner's mutation")
}

func TestConcurrentUpdateHeroSameDigestSingleWinner(t *testing.T) {
	const n = 16
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateHero(writerTestHero("report-writer"))
	require.NoError(t, err)

	var start sync.WaitGroup
	start.Add(n)
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			start.Done()
			start.Wait()
			_, _, err := store.UpdateHero("report-writer", digest, func(document *heroesource.Document) error {
				document.Name = fmt.Sprintf("Winner %d", i)
				return nil
			})
			results <- err
		}()
	}

	successes := 0
	for i := 0; i < n; i++ {
		if err := <-results; err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, ErrConflict)
		}
	}
	require.Equal(t, 1, successes, "exactly one same-digest update must win")

	document, finalDigest, err := store.ReadHero("report-writer")
	require.NoError(t, err)
	require.NotEqual(t, digest, finalDigest)
	matched := false
	for i := 0; i < n; i++ {
		if document.Name == fmt.Sprintf("Winner %d", i) {
			matched = true
			break
		}
	}
	assert.True(t, matched, "final Hero must reflect exactly one winner's mutation")
}

func TestConcurrentUpdatesOnDistinctGraphsBothSucceed(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digestA, err := store.CreateGraph("graph-a", "A")
	require.NoError(t, err)
	_, digestB, err := store.CreateGraph("graph-b", "B")
	require.NoError(t, err)

	var start sync.WaitGroup
	start.Add(2)
	results := make(chan error, 2)
	go func() {
		start.Done()
		start.Wait()
		_, _, err := store.UpdateGraph("graph-a", digestA, func(document *graphsource.Document) error {
			document.Name = "A-renamed"
			return nil
		})
		results <- err
	}()
	go func() {
		start.Done()
		start.Wait()
		_, _, err := store.UpdateGraph("graph-b", digestB, func(document *graphsource.Document) error {
			document.Name = "B-renamed"
			return nil
		})
		results <- err
	}()
	for i := 0; i < 2; i++ {
		require.NoError(t, <-results)
	}

	a, _, err := store.ReadGraph("graph-a")
	require.NoError(t, err)
	require.Equal(t, "A-renamed", a.Name)
	b, _, err := store.ReadGraph("graph-b")
	require.NoError(t, err)
	require.Equal(t, "B-renamed", b.Name)
}

// Publishing is a read-then-write on one object: it reads the drafts and appends
// the next revision. Without per-object serialisation two publishes of the same
// unchanged draft both compute the same next version and the second fails on the
// rename. With it, concurrent publishes agree on one revision.
func TestConcurrentPublishAgreesOnOneRevision(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	_, _, err = store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Tasks["review"] = graphsource.Task{Name: "Review", Model: "openai-main/gpt-5", Goal: "Review"}
		document.Graph["review"] = []string{}
		return nil
	})
	require.NoError(t, err)

	const publishers = 8
	ids := make([]string, publishers)
	errs := make([]error, publishers)
	var wait sync.WaitGroup
	for index := 0; index < publishers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			revision, publishErr := store.PublishGraphRevision("rna-seq")
			if publishErr != nil {
				errs[index] = publishErr
				return
			}
			ids[index] = revision.ID()
		}(index)
	}
	wait.Wait()

	revisions, err := store.ListGraphRevisions("rna-seq")
	require.NoError(t, err)
	for index := 0; index < publishers; index++ {
		require.NoError(t, errs[index], "publisher %d", index)
		require.Len(t, revisions, 1, "publisher %d created an extra revision", index)
		assert.Equal(t, revisions[0].ID(), ids[index], "publisher %d", index)
	}
}
