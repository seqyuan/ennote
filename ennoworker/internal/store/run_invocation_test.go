package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupPublishedRoleInvocation(t *testing.T) (*store.RunRepo, *store.RoleRepo, domain.Session, domain.RoleIdentity, domain.RoleVersion) {
	t.Helper()
	roles, projectID, modelID := setupRoleRepo(t)
	ctx := context.Background()
	role, err := roles.Create(ctx, store.CreateRoleInput{
		Handle: "security-reviewer", Name: "Security Reviewer", Positioning: "Inspect trust boundaries.",
		Icon: "shield-check", Color: "red", Scope: domain.RoleScopeProject, ProjectID: &projectID,
		Definition: validRoleDefinition(modelID),
	})
	require.NoError(t, err)
	version, err := roles.Publish(ctx, role.ID, role.DraftRevision)
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: roles.DB}).Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "Role invocation"})
	require.NoError(t, err)
	return &store.RunRepo{DB: roles.DB}, roles, *session, *role, *version
}

func roleInvocation(sessionID, requestID string, role domain.RoleIdentity, version domain.RoleVersion) domain.SubmitInvocationInput {
	return domain.SubmitInvocationInput{
		SessionID: sessionID, ClientRequestID: requestID, Text: "Review the authorization boundary.",
		Target: domain.RoleInvocationTarget{Kind: domain.InvocationTargetRole, ObjectID: role.ID,
			VersionID: version.ID, ContextMode: domain.InvocationContextRoom},
		RequestedConfig: json.RawMessage(`{}`),
	}
}

func TestSubmitRoleInvocationCreatesAtomicImplicitInviteAndFrozenTarget(t *testing.T) {
	runs, roles, session, role, version := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	_, err := roles.DB.Exec(`UPDATE settings SET value='1' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	_, err = runs.SubmitInvocation(ctx, roleInvocation(session.ID, "request-disabled", role, version))
	require.Error(t, err)
	assert.Equal(t, domain.ErrorCommitFormatNotEnabled, domain.ErrorCodeOf(err))

	_, err = roles.DB.Exec(`UPDATE settings SET value='2' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "request-1", role, version))
	require.NoError(t, err)
	assert.Equal(t, domain.CommitFormatSpeakerV2, submission.Run.CommitFormatVersion)
	assert.Contains(t, string(submission.Run.SpeakerSnapshot), `"handle":"security-reviewer"`)
	assert.Contains(t, string(submission.Run.SpeakerSnapshot), version.ConfigDigest)

	var participantID, roleVersionID string
	require.NoError(t, roles.DB.QueryRow(`SELECT id,role_version_id FROM room_member_instances
		WHERE session_id=? AND role_id=?`, session.ID, role.ID).Scan(&participantID, &roleVersionID))
	assert.Equal(t, version.ID, roleVersionID)

	var controlID string
	require.NoError(t, roles.DB.QueryRow(`SELECT id FROM messages WHERE session_id=? AND visibility='room_control'`, session.ID).Scan(&controlID))
	var controlKind, controlPayload string
	require.NoError(t, roles.DB.QueryRow(`SELECT block_kind,payload_json FROM message_parts WHERE message_id=?`, controlID).
		Scan(&controlKind, &controlPayload))
	assert.Equal(t, "room_control", controlKind)
	assert.Contains(t, controlPayload, `"action":"participant_invited"`)
	assert.Contains(t, controlPayload, participantID)

	var parentID, addresseeKind, addresseeObjectID, addresseeVersionID string
	require.NoError(t, roles.DB.QueryRow(`SELECT parent_message_id,addressee_kind,addressee_object_id,addressee_version_id
		FROM messages WHERE id=?`, submission.UserMessageID).Scan(&parentID, &addresseeKind, &addresseeObjectID, &addresseeVersionID))
	assert.Equal(t, controlID, parentID)
	assert.Equal(t, "role", addresseeKind)
	assert.Equal(t, role.ID, addresseeObjectID)
	assert.Equal(t, version.ID, addresseeVersionID)

	var targetKind, targetObjectID, targetVersionID, targetParticipantID, contextMode string
	require.NoError(t, roles.DB.QueryRow(`SELECT target_kind,target_object_id,target_version_id,
		target_participant_instance_id,context_mode FROM turns WHERE id=?`, submission.TurnID).
		Scan(&targetKind, &targetObjectID, &targetVersionID, &targetParticipantID, &contextMode))
	assert.Equal(t, []string{"role", role.ID, version.ID, participantID, "room"},
		[]string{targetKind, targetObjectID, targetVersionID, targetParticipantID, contextMode})

	replayed, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "request-1", role, version))
	require.NoError(t, err)
	assert.True(t, replayed.Existing)
	assert.Equal(t, submission.Run.ID, replayed.Run.ID)
	var messageCount, participantCount int
	require.NoError(t, roles.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=?`, session.ID).Scan(&messageCount))
	require.NoError(t, roles.DB.QueryRow(`SELECT COUNT(*) FROM room_member_instances WHERE session_id=?`, session.ID).Scan(&participantCount))
	assert.Equal(t, 2, messageCount)
	assert.Equal(t, 1, participantCount)
}

func TestSubmitRoleInvocationReusesParticipantButInvitesOnEachBranchLineage(t *testing.T) {
	runs, roles, session, role, version := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	_, branchPoint := seedAttributedHistory(t, roles, session)
	firstInput := roleInvocation(session.ID, "branch-invite-1", role, version)
	firstInput.BaseMessageID = branchPoint
	first, err := runs.SubmitInvocation(ctx, firstInput)
	require.NoError(t, err)
	_, err = runs.Claim(ctx, first.Run.ID)
	require.NoError(t, err)
	require.NoError(t, runs.Fail(ctx, first.Run.ID, "qualification", "create branch"))
	_, err = (&store.BranchRepo{DB: roles.DB}).Create(ctx, session.ID, branchPoint, "Alternative")
	require.NoError(t, err)

	secondInput := roleInvocation(session.ID, "branch-invite-2", role, version)
	secondInput.BaseMessageID = branchPoint
	_, err = runs.SubmitInvocation(ctx, secondInput)
	require.NoError(t, err)
	var participants, controls int
	require.NoError(t, roles.DB.QueryRow(`SELECT COUNT(*) FROM room_member_instances WHERE session_id=?`, session.ID).Scan(&participants))
	require.NoError(t, roles.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=? AND visibility='room_control'`, session.ID).Scan(&controls))
	assert.Equal(t, 1, participants, "the stable Session participant identity is reused")
	assert.Equal(t, 2, controls, "each branch lineage records its own invitation fact")
}

func TestSubmitRoleInvocationRejectsRuntimeConfigOverridesEarly(t *testing.T) {
	runs, roles, session, role, version := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	input := roleInvocation(session.ID, "role-config-override", role, version)
	input.RequestedConfig = json.RawMessage(`{"modelProfileId":"other","toolPolicyProfileId":"builtin-tool-auto-v1"}`)
	_, err := runs.SubmitInvocation(ctx, input)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorInvocationTargetInvalid, domain.ErrorCodeOf(err))
	var counts int
	require.NoError(t, roles.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE session_id=?`, session.ID).Scan(&counts))
	assert.Zero(t, counts, "rejected overrides must not create a Run")
}

func TestSubmitRoleInvocationRollbackAndParticipantReuse(t *testing.T) {
	runs, roles, session, role, version := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	_, err := roles.DB.Exec(`UPDATE settings SET value='2' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	_, err = roles.DB.Exec(`CREATE TRIGGER fail_role_run BEFORE INSERT ON agent_runs
		WHEN NEW.commit_format_version=2 BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	_, err = runs.SubmitInvocation(ctx, roleInvocation(session.ID, "rollback", role, version))
	require.Error(t, err)
	for _, table := range []string{"messages", "turns", "agent_runs", "room_member_instances"} {
		var count int
		require.NoError(t, roles.DB.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		assert.Equal(t, 0, count, table)
	}
	_, err = roles.DB.Exec(`DROP TRIGGER fail_role_run`)
	require.NoError(t, err)
	first, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "request-1", role, version))
	require.NoError(t, err)
	_, err = roles.DB.Exec(`UPDATE agent_runs SET status='succeeded',finished_at='2026-08-03T00:00:00Z' WHERE id=?`, first.Run.ID)
	require.NoError(t, err)
	_, err = roles.DB.Exec(`UPDATE turns SET status='succeeded',updated_at='2026-08-03T00:00:00Z' WHERE id=?`, first.TurnID)
	require.NoError(t, err)

	secondInput := roleInvocation(session.ID, "request-2", role, version)
	secondInput.BaseMessageID = first.UserMessageID
	_, err = runs.SubmitInvocation(ctx, secondInput)
	require.NoError(t, err)
	var controls, participants int
	require.NoError(t, roles.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=? AND visibility='room_control'`, session.ID).Scan(&controls))
	require.NoError(t, roles.DB.QueryRow(`SELECT COUNT(*) FROM room_member_instances WHERE session_id=?`, session.ID).Scan(&participants))
	assert.Equal(t, 1, controls)
	assert.Equal(t, 1, participants)
}
