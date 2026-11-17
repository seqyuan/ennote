package globalsource

import (
	"os"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addTaskWithHero is the mutation the editor's "Add Task (new local Hero)" sends:
// one task, referencing the Hero created in the same call.
func addTaskWithHero(taskID string, hero *heroesource.Document) func(*domain.FlowDefinition) error {
	return func(definition *domain.FlowDefinition) error {
		definition.Tasks[taskID] = domain.FlowTask{Hero: "local/" + hero.Handle, Goal: "Do the work"}
		return nil
	}
}

func TestMutateGraphWithLocalHeroCreatesBoth(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)

	hero := graphHeroDocument("reviewer", "Review the QC report.")
	created, definition, nextDigest, err := store.MutateGraphWithLocalHero("qc", digest, hero, addTaskWithHero("review", hero))
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "local/reviewer", definition.Tasks["review"].Hero)
	assert.NotEqual(t, digest, nextDigest)

	// The Hero draft is on disk at the path the reference resolves to…
	draft, _, err := store.ReadGraphHero("qc", "reviewer")
	require.NoError(t, err)
	assert.Equal(t, "Review the QC report.", draft.Prompt)
	// …and the definition was written with it.
	onDisk, onDiskDigest, err := store.ReadGraphDefinition("qc")
	require.NoError(t, err)
	assert.Equal(t, nextDigest, onDiskDigest)
	assert.Contains(t, onDisk.Tasks, "review")
}

func TestMutateGraphWithLocalHeroRollsBackHeroOnDefinitionFailure(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)

	hero := graphHeroDocument("reviewer", "Review.")
	_, _, _, err = store.MutateGraphWithLocalHero("qc", digest, hero, func(*domain.FlowDefinition) error {
		return assert.AnError
	})
	require.Error(t, err)

	// Neither half survives: no Task, and no Hero the Task could have referenced.
	_, _, readErr := store.ReadGraphHero("qc", "reviewer")
	assert.ErrorIs(t, readErr, os.ErrNotExist)
	onDisk, onDiskDigest, err := store.ReadGraphDefinition("qc")
	require.NoError(t, err)
	assert.Equal(t, digest, onDiskDigest)
	assert.Empty(t, onDisk.Tasks)
}

func TestMutateGraphWithLocalHeroRefusesConflict(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)

	hero := graphHeroDocument("reviewer", "Review.")
	_, _, current, err := store.MutateGraphWithLocalHero("qc", "sha256:"+string(make([]byte, 0)), hero, addTaskWithHero("review", hero))
	assert.ErrorIs(t, err, ErrConflict)
	assert.Equal(t, digest, current)

	// A stale digest wrote nothing at all.
	_, _, readErr := store.ReadGraphHero("qc", "reviewer")
	assert.ErrorIs(t, readErr, os.ErrNotExist)
	onDisk, _, err := store.ReadGraphDefinition("qc")
	require.NoError(t, err)
	assert.Empty(t, onDisk.Tasks)
}

// A lost response makes the client retry the same request. The retry must succeed
// rather than fail on "the Hero already exists", or the reader sees a save error
// for a save that worked.
func TestMutateGraphWithLocalHeroIsIdempotentOnReplay(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)

	hero := graphHeroDocument("reviewer", "Review.")
	_, _, first, err := store.MutateGraphWithLocalHero("qc", digest, hero, addTaskWithHero("review", hero))
	require.NoError(t, err)

	_, _, second, err := store.MutateGraphWithLocalHero("qc", first, hero, addTaskWithHero("review", hero))
	require.NoError(t, err, "replaying the same request must not fail")
	assert.Equal(t, first, second, "an identical replay leaves the definition digest unchanged")
}

// Creating never overwrites: a second document under the same handle is refused,
// because the caller asked to create rather than to replace.
func TestMutateGraphWithLocalHeroRefusesDifferentHero(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)

	_, err = store.CreateGraphHero("qc", graphHeroDocument("reviewer", "Original prompt."))
	require.NoError(t, err)

	replacement := graphHeroDocument("reviewer", "A different prompt.")
	_, _, _, err = store.MutateGraphWithLocalHero("qc", digest, replacement, addTaskWithHero("review", replacement))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	draft, _, err := store.ReadGraphHero("qc", "reviewer")
	require.NoError(t, err)
	assert.Equal(t, "Original prompt.", draft.Prompt)
	onDisk, _, err := store.ReadGraphDefinition("qc")
	require.NoError(t, err)
	assert.Empty(t, onDisk.Tasks)
}

func TestUpdateGraphHeroRoundTrip(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)
	hero := graphHeroDocument("reviewer", "First prompt.")
	_, _, _, err = store.MutateGraphWithLocalHero("qc", digest, hero, addTaskWithHero("review", hero))
	require.NoError(t, err)
	// The Hero digest comes from the file, not from the mutation result: the third
	// return value is the definition digest.
	created, createdDigest, err := store.ReadGraphHero("qc", "reviewer")
	require.NoError(t, err)
	require.NotEmpty(t, createdDigest)

	updated, nextDigest, err := store.UpdateGraphHero("qc", "reviewer", createdDigest, func(document *heroesource.Document) error {
		document.Prompt = "Second prompt."
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "Second prompt.", updated.Prompt)
	assert.NotEqual(t, createdDigest, nextDigest)

	// A stale digest is a conflict, and the handle is the directory identity.
	_, _, err = store.UpdateGraphHero("qc", "reviewer", createdDigest, func(*heroesource.Document) error { return nil })
	assert.ErrorIs(t, err, ErrConflict)
	_, _, err = store.UpdateGraphHero("qc", "reviewer", nextDigest, func(document *heroesource.Document) error {
		document.Handle = "someone-else"
		return nil
	})
	assert.Error(t, err)
	assert.Equal(t, created.Prompt, "First prompt.")

	reloaded, _, err := store.ReadGraphHero("qc", "reviewer")
	require.NoError(t, err)
	assert.Equal(t, "Second prompt.", reloaded.Prompt)
}
