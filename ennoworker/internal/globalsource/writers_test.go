package globalsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writerTestGraph(id string) *graphsource.Document {
	return &graphsource.Document{
		SchemaVersion: 1, ID: id, Name: "Pack Graph",
		Tasks: map[string]graphsource.Task{
			"load": {Name: "Load", Hero: "local/reviewer", Goal: "Load the input"},
		},
		Graph: map[string][]string{"load": {}},
	}
}

func writerTestHero(handle string) *heroesource.Document {
	return &heroesource.Document{
		SchemaVersion: 1, Handle: handle, Name: "Pack Hero",
		Model:             heroesource.ModelBinding{Ref: "provider/model", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills:            []heroesource.SkillBinding{},
		Authority:         heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss,
		AllowedTools:      []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxTotalTokens: 1000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Follow the instructions.",
	}
}

func TestCreateGraphFromDocumentWritesWholeDocument(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	document := writerTestGraph("pack-graph")
	digest, err := store.CreateGraphFromDocument(document)
	require.NoError(t, err)
	assert.NotEmpty(t, digest)

	read, readDigest, err := store.ReadGraph("pack-graph")
	require.NoError(t, err)
	assert.Equal(t, digest, readDigest)
	assert.Equal(t, "Pack Graph", read.Name)
	assert.Contains(t, read.Tasks, "load")

	// graph.yaml is 0600 inside a 0700 directory.
	info, err := os.Stat(filepath.Join(store.GraphsDir(), "pack-graph", "graph.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Join(store.GraphsDir(), "pack-graph"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	// An existing Graph is never overwritten.
	_, err = store.CreateGraphFromDocument(document)
	require.Error(t, err)
}

func TestCreateGraphHeroNestedDirectories(t *testing.T) {
	home := t.TempDir()
	store := Store{HomeDir: home}
	// The Graph directory does not exist yet: an installer writes Graph-local
	// Skills and Heroes before graph.yaml.
	_, err := store.CreateGraphFromDocument(writerTestGraph("pack-graph"))
	require.NoError(t, err)

	hero := writerTestHero("reviewer")
	digest, err := store.CreateGraphHero("pack-graph", hero)
	require.NoError(t, err)
	path := filepath.Join(home, "agents", "graphs", "pack-graph", "heroes", "reviewer", "hero.md")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	read, readDigest, err := store.ReadGraphHero("pack-graph", "reviewer")
	require.NoError(t, err)
	assert.Equal(t, digest, readDigest)
	assert.Equal(t, "reviewer", read.Handle)

	// The handle must match the directory that is created.
	mismatched := writerTestHero("reviewer")
	mismatched.Handle = " other"
	_, err = store.CreateGraphHero("pack-graph", mismatched)
	require.Error(t, err)

	// An existing Graph-local Hero is never overwritten.
	_, err = store.CreateGraphHero("pack-graph", hero)
	require.Error(t, err)
}

func TestCreateGraphHeroRejectsSymlinkedGraph(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	store := Store{HomeDir: home}
	require.NoError(t, os.MkdirAll(store.GraphsDir(), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(store.GraphsDir(), "pack-graph")))

	_, err := store.CreateGraphHero("pack-graph", writerTestHero("reviewer"))
	require.Error(t, err)
	entries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestDeleteGraphHeroAndHeroRemoveWholeDirectories(t *testing.T) {
	home := t.TempDir()
	store := Store{HomeDir: home}
	_, err := store.CreateGraphHero("pack-graph", writerTestHero("reviewer"))
	require.NoError(t, err)
	_, _, err = store.CreateHero(writerTestHero("report-writer"))
	require.NoError(t, err)
	_, err = store.PublishHeroRevision("report-writer")
	require.NoError(t, err)

	require.NoError(t, store.DeleteGraphHero("pack-graph", "reviewer"))
	_, _, err = store.ReadGraphHero("pack-graph", "reviewer")
	require.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, store.DeleteHero("report-writer"))
	_, _, err = store.ReadHero("report-writer")
	require.ErrorIs(t, err, os.ErrNotExist)
	// The revision directory went with the Hero: a revision sequence must stay
	// contiguous.
	_, err = os.Lstat(filepath.Join(home, "agents", "heroes", "report-writer"))
	require.ErrorIs(t, err, os.ErrNotExist)

	require.Error(t, store.DeleteHero("../escape"))
	// A missing object reports os.ErrNotExist instead of silently succeeding.
	require.ErrorIs(t, store.DeleteHero("missing-hero"), os.ErrNotExist)
}

func TestDeleteGraphRemovesRevisionsHeroesAndSkills(t *testing.T) {
	home := t.TempDir()
	store := Store{HomeDir: home}
	_, err := store.CreateGraphFromDocument(writerTestGraph("pack-graph"))
	require.NoError(t, err)
	_, err = store.CreateGraphHero("pack-graph", writerTestHero("reviewer"))
	require.NoError(t, err)
	skillDir := filepath.Join(home, "agents", "graphs", "pack-graph", "skills", "tables")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("prompt\n"), 0o600))
	_, err = store.PublishGraphRevision("pack-graph")
	require.NoError(t, err)

	require.NoError(t, store.DeleteGraph("pack-graph"))
	_, err = os.Lstat(filepath.Join(home, "agents", "graphs", "pack-graph"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestDeleteRejectsSymlinkedObject(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	store := Store{HomeDir: home}
	require.NoError(t, os.MkdirAll(store.HeroesDir(), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(store.HeroesDir(), "linked-hero")))

	err := store.DeleteHero("linked-hero")
	require.Error(t, err)
	_, statErr := os.Stat(outside)
	require.NoError(t, statErr, "the symlink target must survive")
}
