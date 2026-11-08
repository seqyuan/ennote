package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupPublishedHeroInvocation(t *testing.T) (*store.RunRepo, *sql.DB, domain.Session, domain.HeroIdentity, domain.HeroVersion, *globalsource.Store) {
	t.Helper()
	return setupPublishedHeroInvocationWithSkills(t, []heroesource.SkillBinding{})
}

// setupPublishedHeroInvocationWithSkills is setupPublishedHeroInvocation with
// explicit Role Skill bindings so preload-pinning tests can publish a Role that
// carries them.
func setupPublishedHeroInvocationWithSkills(t *testing.T,
	skillBindings []heroesource.SkillBinding) (*store.RunRepo, *sql.DB, domain.Session, domain.HeroIdentity, domain.HeroVersion, *globalsource.Store) {
	t.Helper()
	stack := newFileConfigStack(t)
	ctx := context.Background()
	db, _, session := newSessionDB(t)
	// V2: the invoked Role is a published file revision (globalsource), never a
	// global hero SQL row.
	document := &heroesource.Document{
		SchemaVersion: 1, Handle: "security-reviewer", Name: "Security Reviewer",
		Description: "Inspect trust boundaries.", Positioning: "Inspect trust boundaries.",
		Icon: "shield-check", Color: "red",
		Model:  heroesource.ModelBinding{Ref: stack.DefaultRef, ThinkingEffort: domain.ThinkingMedium, Fallbacks: []string{}},
		Skills: skillBindings, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextRoom,
			AllowedModes:           []domain.HeroContextMode{domain.HeroContextRoom, domain.HeroContextReply, domain.HeroContextFresh},
			OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationApprovalRequired,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Review the supplied evidence independently.",
	}
	_, _, err := stack.Sources.CreateHero(document)
	require.NoError(t, err)
	revision, err := stack.Sources.PublishHeroRevision(document.Handle)
	require.NoError(t, err)
	// Re-read the parsed document so the frozen definition matches the on-disk
	// revision exactly (normalization sorts allowed tools, caller kinds, etc.).
	parsed, _, err := stack.Sources.ReadHero(document.Handle)
	require.NoError(t, err)
	definition, diagnostics := (&store.HeroDiscovery{Models: stack.ModelRepo}).ResolveDocument(ctx, parsed)
	require.NotNil(t, definition, diagnostics)
	revisionID := revision.ID()
	hero := domain.HeroIdentity{
		ID: document.Handle, Handle: document.Handle, Name: document.Name,
		Description: document.Description, Positioning: document.Positioning,
		Icon: document.Icon, Color: document.Color,
		Status: "active", SourceKind: "file", SourceLocator: document.Handle, SourceDigest: revision.Digest,
		CurrentVersionID: &revisionID, CurrentVersion: revision.Version, DelegationEnabled: true,
	}
	version := domain.HeroVersion{ID: revisionID, HeroID: document.Handle, Version: revision.Version,
		Definition: *definition, ConfigDigest: revision.Digest, Status: "published"}
	return &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo,
			Policies: stack.Policies, HeroSources: stack.Sources},
		db, session, hero, version, stack.Sources
}

func heroInvocation(sessionID, requestID string, hero domain.HeroIdentity, version domain.HeroVersion) domain.SubmitInvocationInput {
	return domain.SubmitInvocationInput{
		SessionID: sessionID, ClientRequestID: requestID, Text: "Review the authorization boundary.",
		Target: domain.HeroInvocationTarget{Kind: domain.InvocationTargetHero, ObjectID: hero.ID,
			VersionID: version.ID, ContextMode: domain.InvocationContextRoom},
		RequestedConfig: json.RawMessage(`{}`),
	}
}

func TestSubmitHeroInvocationCreatesAtomicImplicitInviteAndFrozenTarget(t *testing.T) {
	runs, heroes, session, hero, version, _ := setupPublishedHeroInvocation(t)
	ctx := context.Background()
	// V2 file-native invocation is always format-2 (SpeakerV2); the legacy
	// hosted_commit_format_version writer gate does not apply.
	submission, err := runs.SubmitInvocation(ctx, heroInvocation(session.ID, "request-1", hero, version))
	require.NoError(t, err)
	assert.Equal(t, domain.CommitFormatSpeakerV2, submission.Run.CommitFormatVersion)
	assert.Contains(t, string(submission.Run.SpeakerSnapshot), `"handle":"security-reviewer"`)
	assert.Contains(t, string(submission.Run.SpeakerSnapshot), version.ConfigDigest)

	var participantID, heroVersionID string
	require.NoError(t, heroes.QueryRow(`SELECT id,hero_version_id FROM room_member_instances
		WHERE session_id=? AND hero_id=?`, session.ID, hero.ID).Scan(&participantID, &heroVersionID))
	assert.Equal(t, version.ID, heroVersionID)

	var controlID string
	require.NoError(t, heroes.QueryRow(`SELECT id FROM messages WHERE session_id=? AND visibility='room_control'`, session.ID).Scan(&controlID))
	var controlKind, controlPayload string
	require.NoError(t, heroes.QueryRow(`SELECT block_kind,payload_json FROM message_parts WHERE message_id=?`, controlID).
		Scan(&controlKind, &controlPayload))
	assert.Equal(t, "room_control", controlKind)
	assert.Contains(t, controlPayload, `"action":"participant_invited"`)
	assert.Contains(t, controlPayload, participantID)

	var parentID, addresseeKind, addresseeObjectID, addresseeVersionID string
	require.NoError(t, heroes.QueryRow(`SELECT parent_message_id,addressee_kind,addressee_object_id,addressee_version_id
		FROM messages WHERE id=?`, submission.UserMessageID).Scan(&parentID, &addresseeKind, &addresseeObjectID, &addresseeVersionID))
	assert.Equal(t, controlID, parentID)
	assert.Equal(t, "hero", addresseeKind)
	assert.Equal(t, hero.ID, addresseeObjectID)
	assert.Equal(t, version.ID, addresseeVersionID)

	var targetKind, targetObjectID, targetVersionID, targetParticipantID, contextMode string
	require.NoError(t, heroes.QueryRow(`SELECT target_kind,target_object_id,target_version_id,
		target_participant_instance_id,context_mode FROM turns WHERE id=?`, submission.TurnID).
		Scan(&targetKind, &targetObjectID, &targetVersionID, &targetParticipantID, &contextMode))
	assert.Equal(t, []string{"hero", hero.ID, version.ID, participantID, "room"},
		[]string{targetKind, targetObjectID, targetVersionID, targetParticipantID, contextMode})

	replayed, err := runs.SubmitInvocation(ctx, heroInvocation(session.ID, "request-1", hero, version))
	require.NoError(t, err)
	assert.True(t, replayed.Existing)
	assert.Equal(t, submission.Run.ID, replayed.Run.ID)
	var messageCount, participantCount int
	require.NoError(t, heroes.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=?`, session.ID).Scan(&messageCount))
	require.NoError(t, heroes.QueryRow(`SELECT COUNT(*) FROM room_member_instances WHERE session_id=?`, session.ID).Scan(&participantCount))
	assert.Equal(t, 2, messageCount)
	assert.Equal(t, 1, participantCount)
}

func TestSubmitHeroInvocationReusesParticipantButInvitesOnEachBranchLineage(t *testing.T) {
	runs, heroes, session, hero, version, _ := setupPublishedHeroInvocation(t)
	ctx := context.Background()
	_, branchPoint := seedAttributedHistory(t, heroes, session)
	firstInput := heroInvocation(session.ID, "branch-invite-1", hero, version)
	firstInput.BaseMessageID = branchPoint
	first, err := runs.SubmitInvocation(ctx, firstInput)
	require.NoError(t, err)
	_, err = runs.Claim(ctx, first.Run.ID)
	require.NoError(t, err)
	require.NoError(t, runs.Fail(ctx, first.Run.ID, "qualification", "create branch"))
	_, err = (&store.BranchRepo{DB: heroes}).Create(ctx, session.ID, branchPoint, "Alternative")
	require.NoError(t, err)

	secondInput := heroInvocation(session.ID, "branch-invite-2", hero, version)
	secondInput.BaseMessageID = branchPoint
	_, err = runs.SubmitInvocation(ctx, secondInput)
	require.NoError(t, err)
	var participants, controls int
	require.NoError(t, heroes.QueryRow(`SELECT COUNT(*) FROM room_member_instances WHERE session_id=?`, session.ID).Scan(&participants))
	require.NoError(t, heroes.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=? AND visibility='room_control'`, session.ID).Scan(&controls))
	assert.Equal(t, 1, participants, "the stable Session participant identity is reused")
	assert.Equal(t, 2, controls, "each branch lineage records its own invitation fact")
}

func TestSubmitHeroInvocationRejectsRuntimeConfigOverridesEarly(t *testing.T) {
	runs, heroes, session, hero, version, _ := setupPublishedHeroInvocation(t)
	ctx := context.Background()
	input := heroInvocation(session.ID, "hero-config-override", hero, version)
	input.RequestedConfig = json.RawMessage(`{"modelProfileId":"other","toolPolicyProfileId":"builtin-tool-auto-v1"}`)
	_, err := runs.SubmitInvocation(ctx, input)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorInvocationTargetInvalid, domain.ErrorCodeOf(err))
	var counts int
	require.NoError(t, heroes.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE session_id=?`, session.ID).Scan(&counts))
	assert.Zero(t, counts, "rejected overrides must not create a Run")
}

func TestSubmitHeroInvocationRollbackAndParticipantReuse(t *testing.T) {
	runs, heroes, session, hero, version, _ := setupPublishedHeroInvocation(t)
	ctx := context.Background()
	_, err := heroes.Exec(`CREATE TRIGGER fail_hero_run BEFORE INSERT ON agent_runs
		WHEN NEW.commit_format_version=2 BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	_, err = runs.SubmitInvocation(ctx, heroInvocation(session.ID, "rollback", hero, version))
	require.Error(t, err)
	for _, table := range []string{"messages", "turns", "agent_runs", "room_member_instances"} {
		var count int
		require.NoError(t, heroes.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		assert.Equal(t, 0, count, table)
	}
	_, err = heroes.Exec(`DROP TRIGGER fail_hero_run`)
	require.NoError(t, err)
	first, err := runs.SubmitInvocation(ctx, heroInvocation(session.ID, "request-1", hero, version))
	require.NoError(t, err)
	_, err = heroes.Exec(`UPDATE agent_runs SET status='succeeded',finished_at='2026-08-03T00:00:00Z' WHERE id=?`, first.Run.ID)
	require.NoError(t, err)
	_, err = heroes.Exec(`UPDATE turns SET status='succeeded',updated_at='2026-08-03T00:00:00Z' WHERE id=?`, first.TurnID)
	require.NoError(t, err)

	secondInput := heroInvocation(session.ID, "request-2", hero, version)
	secondInput.BaseMessageID = first.UserMessageID
	_, err = runs.SubmitInvocation(ctx, secondInput)
	require.NoError(t, err)
	var controls, participants int
	require.NoError(t, heroes.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=? AND visibility='room_control'`, session.ID).Scan(&controls))
	require.NoError(t, heroes.QueryRow(`SELECT COUNT(*) FROM room_member_instances WHERE session_id=?`, session.ID).Scan(&participants))
	assert.Equal(t, 1, controls)
	assert.Equal(t, 1, participants)
}
