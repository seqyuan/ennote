package globalsource

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveRoleReturnsLatestRevisionWithSource pins the design 六 P1 resolve
// point: a bare handle resolves to the latest published revision with an audit
// source ("global" today).
func TestResolveRoleReturnsLatestRevisionWithSource(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	role := &rolesource.Document{
		SchemaVersion: 1, Handle: "reviewer", Name: "Reviewer",
		Model:        rolesource.ModelBinding{Ref: "openai-main/gpt-5", ThinkingEffort: domain.ThinkingDefault},
		Skills:       []rolesource.SkillBinding{},
		AllowedTools: []string{"read"},
		Context:      rolesource.ContextPolicy{AllowedModes: []domain.RoleContextMode{}},
		Delegation:   rolesource.DelegationPolicy{AllowedCallerKinds: []string{}, AllowedStrategies: []string{}},
		Prompt:       "Review the supplied work.",
	}
	_, _, err := store.CreateRole(role)
	require.NoError(t, err)
	published, err := store.PublishRoleRevision("reviewer")
	require.NoError(t, err)

	resolved, err := store.ResolveRole("reviewer")
	require.NoError(t, err)
	assert.Equal(t, "reviewer", resolved.Document.Handle)
	assert.Equal(t, published.ID(), resolved.Revision.ID())
	assert.Equal(t, published.Digest, resolved.Revision.Digest)
	assert.Equal(t, "global", resolved.Source)
}

// TestResolveRoleUnpublishedFails pins that resolution only sees published
// revisions, never the mutable draft.
func TestResolveRoleUnpublishedFails(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	role := &rolesource.Document{
		SchemaVersion: 1, Handle: "draft", Name: "Draft",
		Model:        rolesource.ModelBinding{Ref: "openai-main/gpt-5"},
		Skills:       []rolesource.SkillBinding{},
		AllowedTools: []string{},
		Context:      rolesource.ContextPolicy{},
		Delegation:   rolesource.DelegationPolicy{},
		Prompt:       "unpublished",
	}
	_, _, err := store.CreateRole(role)
	require.NoError(t, err)

	_, err = store.ResolveRole("draft")
	require.Error(t, err)
}
