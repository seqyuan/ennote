package globalsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writerTestGraph(id string) *graphsource.Document {
	return &graphsource.Document{
		SchemaVersion: 1, ID: id, Name: "Pack Graph",
		Tasks: map[string]graphsource.Task{
			"load": {Name: "Load", Role: "local/reviewer", Goal: "Load the input"},
		},
		Graph: map[string][]string{"load": {}},
	}
}

func writerTestRole(handle string) *rolesource.Document {
	return &rolesource.Document{
		SchemaVersion: 1, Handle: handle, Name: "Pack Role",
		Model:             rolesource.ModelBinding{Ref: "provider/model", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills:            []rolesource.SkillBinding{},
		Authority:         rolesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss,
		AllowedTools:      []string{"read"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxTotalTokens: 1000}},
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

func TestCreateGraphRoleNestedDirectories(t *testing.T) {
	home := t.TempDir()
	store := Store{HomeDir: home}
	// The Graph directory does not exist yet: an installer writes Graph-local
	// Skills and Roles before graph.yaml.
	_, err := store.CreateGraphFromDocument(writerTestGraph("pack-graph"))
	require.NoError(t, err)

	role := writerTestRole("reviewer")
	digest, err := store.CreateGraphRole("pack-graph", role)
	require.NoError(t, err)
	path := filepath.Join(home, "agents", "graphs", "pack-graph", "roles", "reviewer", "role.md")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	read, readDigest, err := store.ReadGraphRole("pack-graph", "reviewer")
	require.NoError(t, err)
	assert.Equal(t, digest, readDigest)
	assert.Equal(t, "reviewer", read.Handle)

	// The handle must match the directory that is created.
	mismatched := writerTestRole("reviewer")
	mismatched.Handle = " other"
	_, err = store.CreateGraphRole("pack-graph", mismatched)
	require.Error(t, err)

	// An existing Graph-local Role is never overwritten.
	_, err = store.CreateGraphRole("pack-graph", role)
	require.Error(t, err)
}

func TestCreateGraphRoleRejectsSymlinkedGraph(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	store := Store{HomeDir: home}
	require.NoError(t, os.MkdirAll(store.GraphsDir(), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(store.GraphsDir(), "pack-graph")))

	_, err := store.CreateGraphRole("pack-graph", writerTestRole("reviewer"))
	require.Error(t, err)
	entries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestDeleteGraphRoleAndRoleRemoveWholeDirectories(t *testing.T) {
	home := t.TempDir()
	store := Store{HomeDir: home}
	_, err := store.CreateGraphRole("pack-graph", writerTestRole("reviewer"))
	require.NoError(t, err)
	_, _, err = store.CreateRole(writerTestRole("report-writer"))
	require.NoError(t, err)
	_, err = store.PublishRoleRevision("report-writer")
	require.NoError(t, err)

	require.NoError(t, store.DeleteGraphRole("pack-graph", "reviewer"))
	_, _, err = store.ReadGraphRole("pack-graph", "reviewer")
	require.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, store.DeleteRole("report-writer"))
	_, _, err = store.ReadRole("report-writer")
	require.ErrorIs(t, err, os.ErrNotExist)
	// The revision directory went with the Role: a revision sequence must stay
	// contiguous.
	_, err = os.Lstat(filepath.Join(home, "agents", "roles", "report-writer"))
	require.ErrorIs(t, err, os.ErrNotExist)

	require.Error(t, store.DeleteRole("../escape"))
	// A missing object reports os.ErrNotExist instead of silently succeeding.
	require.ErrorIs(t, store.DeleteRole("missing-role"), os.ErrNotExist)
}

func TestDeleteGraphRemovesRevisionsRolesAndSkills(t *testing.T) {
	home := t.TempDir()
	store := Store{HomeDir: home}
	_, err := store.CreateGraphFromDocument(writerTestGraph("pack-graph"))
	require.NoError(t, err)
	_, err = store.CreateGraphRole("pack-graph", writerTestRole("reviewer"))
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
	require.NoError(t, os.MkdirAll(store.RolesDir(), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(store.RolesDir(), "linked-role")))

	err := store.DeleteRole("linked-role")
	require.Error(t, err)
	_, statErr := os.Stat(outside)
	require.NoError(t, statErr, "the symlink target must survive")
}
