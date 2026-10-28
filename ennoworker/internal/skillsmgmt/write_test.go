package skillsmgmt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteThenInspectWriteReportsExistsIdentical(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	input := CreateInput{ID: "pack-skill", Name: "Pack Skill", Prompt: "Do the thing."}

	result, err := Write(root, input)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "pack-skill", result.ID)
	assert.Equal(t, filepath.Join(root, "pack-skill"), result.Path)

	// Writing the identical Skill again is a no-op, reported by a distinct
	// sentinel so an installer can tell it from a conflict.
	_, err = Write(root, input)
	require.ErrorIs(t, err, ErrSkillExistsIdentical)
	assert.NotErrorIs(t, err, ErrSkillExists)

	// InspectWrite plans without writing and reports the same content digest
	// ContentDigest recomputes from the installed directory.
	state, digest, err := InspectWrite(root, input)
	require.NoError(t, err)
	assert.Equal(t, WriteStateExistsIdentical, state)
	assert.True(t, strings.HasPrefix(digest, "sha256:"))
	installed, err := ContentDigest(root, "pack-skill")
	require.NoError(t, err)
	assert.Equal(t, digest, installed)

	// A new root reports created, and InspectWrite never creates it.
	freshRoot := filepath.Join(t.TempDir(), "absent")
	state, plannedDigest, err := InspectWrite(freshRoot, input)
	require.NoError(t, err)
	assert.Equal(t, WriteStateCreated, state)
	assert.Equal(t, digest, plannedDigest)
	_, statErr := os.Lstat(freshRoot)
	assert.True(t, os.IsNotExist(statErr))
}

func TestWriteReportsTypedConflict(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	_, err := Write(root, CreateInput{ID: "pack-skill", Prompt: "First prompt."})
	require.NoError(t, err)

	_, err = Write(root, CreateInput{ID: "pack-skill", Prompt: "Second prompt."})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSkillExists)
	assert.NotErrorIs(t, err, ErrSkillExistsIdentical)
	var conflict *ConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "pack-skill", conflict.ID)

	// The original Skill is untouched.
	loaded, loadErr := skills.Load(filepath.Join(root, "pack-skill"))
	require.NoError(t, loadErr)
	assert.Contains(t, loaded.PromptText, "First prompt.")

	// InspectWrite reports the same conflict without writing.
	_, _, err = InspectWrite(root, CreateInput{ID: "pack-skill", Prompt: "Second prompt."})
	require.ErrorIs(t, err, ErrSkillExists)
}

func TestWriteRejectsInvalidInputBeforeCreatingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	_, err := Write(root, CreateInput{ID: "../escape", Prompt: "body"})
	require.ErrorIs(t, err, ErrSkillIDInvalid)
	_, err = Write(root, CreateInput{ID: "valid-id", Prompt: "  "})
	require.ErrorIs(t, err, ErrSkillPromptRequired)
	_, statErr := os.Lstat(root)
	assert.True(t, os.IsNotExist(statErr), "validation must run before the root is created")
}

func TestCreateUsesWriteAndKeepsDuplicateAsConflict(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	service := &Service{UserRoot: root}
	result, err := service.Create(CreateInput{ID: "ui-skill", Prompt: "Prompt body."})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "ui-skill"), result.Path)
	// The create-skill API keeps refusing any existing target, including an
	// identical one, so clients still see 409.
	_, err = service.Create(CreateInput{ID: "ui-skill", Prompt: "Prompt body."})
	require.ErrorIs(t, err, ErrSkillExists)
	_, err = service.Create(CreateInput{ID: "ui-skill", Prompt: "Other body."})
	require.ErrorIs(t, err, ErrSkillExists)
}

func TestRenderedDigestCoversBothFiles(t *testing.T) {
	rendered, err := Render(CreateInput{ID: "digest-skill", Prompt: "Body."})
	require.NoError(t, err)
	digest := rendered.Digest()
	require.True(t, strings.HasPrefix(digest, "sha256:"))

	mutated := &Rendered{ID: rendered.ID, Files: map[string][]byte{
		"skill.json": rendered.Files["skill.json"],
		"SKILL.md":   append(append([]byte{}, rendered.Files["SKILL.md"]...), '\n'),
	}}
	assert.NotEqual(t, digest, mutated.Digest())
}
