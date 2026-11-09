package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeroRunFreezesPublishedExecutionDefinitionAndPrompt(t *testing.T) {
	runs, _, session, hero, version, sources := setupPublishedHeroInvocation(t)
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, heroInvocation(session.ID, "hero-config", hero, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	// A later publish (identity + prompt change) cannot alter the frozen
	// speaker or the published definition selected by the Turn: the run stays
	// pinned to the originally selected revision.
	_, _, err = sources.UpdateHero(hero.ID, version.ConfigDigest, func(document *heroesource.Document) error {
		document.Name = "Renamed Live Hero"
		document.Prompt = "This draft must not be executed."
		return nil
	})
	require.NoError(t, err)
	_, err = sources.PublishHeroRevision(hero.ID)
	require.NoError(t, err)

	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Hero)
	assert.Equal(t, hero.ID, resolved.Effective.Hero.ObjectID)
	assert.Equal(t, version.ID, resolved.Effective.Hero.VersionID)
	assert.Equal(t, version.ConfigDigest, resolved.Effective.Hero.ConfigDigest)
	assert.Equal(t, "security-reviewer", resolved.Effective.Hero.Handle)
	assert.Equal(t, "Security Reviewer", resolved.Effective.Hero.DisplayName)
	assert.Equal(t, version.Definition.AllowedTools, resolved.Effective.Hero.AllowedTools)
	assert.Equal(t, version.Definition.ModelBinding.ModelProfileID, resolved.Effective.ModelProfileID)
	assert.Equal(t, version.Definition.ModelBinding.ThinkingEffort, resolved.Effective.ThinkingEffort)
	assert.Equal(t, version.Definition.MaxLoopIterations, resolved.Effective.MaxIterations)
	assert.True(t, resolved.Effective.Routing.Pinned)
	assert.False(t, resolved.Effective.Routing.AllowAutoRoute)
	assert.Equal(t, "builtin-tool-discuss-v3", resolved.Effective.ToolPolicy.ID)
	assert.Equal(t, version.Definition.HeroPrompt, resolved.SystemPrompt.AgentPrompt)
	assert.Equal(t, hero.ID, resolved.SystemPrompt.AgentProfileID)
	assert.NotContains(t, string(claimed.EffectiveConfig), version.Definition.HeroPrompt)

	stored, err := runs.Get(ctx, claimed.ID)
	require.NoError(t, err)
	assert.Equal(t, resolved.SystemPrompt.Digest, stored.SystemPrompt.Digest)
	assert.NotContains(t, string(stored.EffectiveConfig), version.Definition.HeroPrompt)
}

func TestHeroRunRejectsRuntimeModelAndPermissionOverrides(t *testing.T) {
	runs, heroes, session, hero, version, _ := setupPublishedHeroInvocation(t)
	ctx := context.Background()
	input := heroInvocation(session.ID, "hero-override", hero, version)
	input.RequestedConfig = json.RawMessage(`{"modelProfileId":"other","toolPolicyProfileId":"builtin-tool-auto-v1"}`)
	// The override is rejected before a Run is created; the ResolveAndFreezeConfig
	// guard remains as defense-in-depth for crafted rows.
	_, err := runs.SubmitInvocation(ctx, input)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorInvocationTargetInvalid, domain.ErrorCodeOf(err))
	var runCount int
	require.NoError(t, heroes.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE session_id=?`, session.ID).Scan(&runCount))
	assert.Zero(t, runCount)
}
