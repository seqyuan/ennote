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

func TestAgentSourcesUseSharedAgentsRoot(t *testing.T) {
	home := t.TempDir()
	store := Store{HomeDir: home}
	assert.Equal(t, filepath.Join(home, "agents"), store.AgentsDir())
	assert.Equal(t, filepath.Join(home, "agents", "roles"), store.RolesDir())
	assert.Equal(t, filepath.Join(home, "agents", "graphs"), store.GraphsDir())
}

func TestGraphRevisionsAreImmutableAndDigestIdempotent(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)

	first, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, "v000001", first.ID())
	assert.Equal(t, digest, first.Digest)
	again, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, first, again)

	revisionSource := filepath.Join(store.GraphsDir(), "rna-seq", "revisions", "v000001", "graph.yaml")
	original, err := os.ReadFile(revisionSource)
	require.NoError(t, err)
	_, nextDigest, err := store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Description = "Updated source"
		return nil
	})
	require.NoError(t, err)
	second, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, "v000002", second.ID())
	assert.Equal(t, nextDigest, second.Digest)
	unchanged, err := os.ReadFile(revisionSource)
	require.NoError(t, err)
	assert.Equal(t, original, unchanged)

	revisions, err := store.ListGraphRevisions("rna-seq")
	require.NoError(t, err)
	require.Len(t, revisions, 2)
	assert.Equal(t, []int{1, 2}, []int{revisions[0].Version, revisions[1].Version})
}

func TestRoleRevisionPublishesCanonicalRoleMarkdown(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	role := &rolesource.Document{
		SchemaVersion: 1, Handle: "reviewer", Name: "Reviewer",
		Model:  rolesource.ModelBinding{Ref: "openai-main/gpt-5", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, AllowedTools: []string{"read"},
		Context:    rolesource.ContextPolicy{AllowedModes: []domain.RoleContextMode{}},
		Delegation: rolesource.DelegationPolicy{AllowedCallerKinds: []string{}, AllowedStrategies: []string{}},
		Prompt:     "Review the supplied work.",
	}
	_, digest, err := store.CreateRole(role)
	require.NoError(t, err)
	revision, err := store.PublishRoleRevision("reviewer")
	require.NoError(t, err)
	assert.Equal(t, digest, revision.Digest)
	contents, err := os.ReadFile(filepath.Join(store.RolesDir(), "reviewer", "revisions", "v000001", "role.md"))
	require.NoError(t, err)
	parsed, err := rolesource.Parse(contents)
	require.NoError(t, err)
	assert.Equal(t, role.Handle, parsed.Handle)
	assert.Equal(t, role.Prompt, parsed.Prompt)
}

func TestRevisionMetadataRejectsTampering(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, _, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	_, err = store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	metadata := filepath.Join(store.GraphsDir(), "rna-seq", "revisions", "v000001", "revision.json")
	require.NoError(t, os.WriteFile(metadata, []byte(`{"schemaVersion":1,"resourceId":"other","version":1,"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-08-10T00:00:00Z"}`), 0o600))

	_, err = store.ListGraphRevisions("rna-seq")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata is invalid")
}
