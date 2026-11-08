package globalsource

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveHeroReturnsLatestRevisionWithSource pins the design 六 P1 resolve
// point: a bare handle resolves to the latest published revision with an audit
// source ("global" today).
func TestResolveHeroReturnsLatestRevisionWithSource(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	hero := &heroesource.Document{
		SchemaVersion: 1, Handle: "reviewer", Name: "Reviewer",
		Model:        heroesource.ModelBinding{Ref: "openai-main/gpt-5", ThinkingEffort: domain.ThinkingDefault},
		Skills:       []heroesource.SkillBinding{},
		AllowedTools: []string{"read"},
		Context:      heroesource.ContextPolicy{AllowedModes: []domain.HeroContextMode{}},
		Delegation:   heroesource.DelegationPolicy{AllowedCallerKinds: []string{}, AllowedStrategies: []string{}},
		Prompt:       "Review the supplied work.",
	}
	_, _, err := store.CreateHero(hero)
	require.NoError(t, err)
	published, err := store.PublishHeroRevision("reviewer")
	require.NoError(t, err)

	resolved, err := store.ResolveHero("reviewer")
	require.NoError(t, err)
	assert.Equal(t, "reviewer", resolved.Document.Handle)
	assert.Equal(t, published.ID(), resolved.Revision.ID())
	assert.Equal(t, published.Digest, resolved.Revision.Digest)
	assert.Equal(t, "global", resolved.Source)
}

// TestResolveHeroUnpublishedFails pins that resolution only sees published
// revisions, never the mutable draft.
func TestResolveHeroUnpublishedFails(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	hero := &heroesource.Document{
		SchemaVersion: 1, Handle: "draft", Name: "Draft",
		Model:        heroesource.ModelBinding{Ref: "openai-main/gpt-5"},
		Skills:       []heroesource.SkillBinding{},
		AllowedTools: []string{},
		Context:      heroesource.ContextPolicy{},
		Delegation:   heroesource.DelegationPolicy{},
		Prompt:       "unpublished",
	}
	_, _, err := store.CreateHero(hero)
	require.NoError(t, err)

	_, err = store.ResolveHero("draft")
	require.Error(t, err)
}
