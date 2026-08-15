package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpeakerLedgerFinalizerCommitsOneAttributedPublicAnswer(t *testing.T) {
	runs, roles, session, role, version, _ := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "format-2-final", role, version))
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeSuccess(ctx, submission.Run.ID, transcriptOutput()))

	var canonicalCount, privateCount int
	require.NoError(t, roles.QueryRow(`SELECT COUNT(*) FROM messages WHERE run_id=?`, submission.Run.ID).Scan(&canonicalCount))
	require.NoError(t, roles.QueryRow(`SELECT COUNT(*) FROM run_messages WHERE run_id=?`, submission.Run.ID).Scan(&privateCount))
	assert.Equal(t, 1, canonicalCount)
	assert.Equal(t, 3, privateCount)

	stored, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.AssistantMessageID)
	var parentID, speakerKind, speakerObjectID, speakerVersionID, participantID, visibility, snapshot string
	require.NoError(t, roles.QueryRow(`SELECT parent_message_id,speaker_kind,speaker_object_id,
		speaker_version_id,participant_instance_id,visibility,speaker_snapshot_json FROM messages WHERE id=?`,
		*stored.AssistantMessageID).Scan(&parentID, &speakerKind, &speakerObjectID, &speakerVersionID,
		&participantID, &visibility, &snapshot))
	assert.Equal(t, submission.UserMessageID, parentID)
	assert.Equal(t, "role", speakerKind)
	assert.Equal(t, role.ID, speakerObjectID)
	assert.Equal(t, version.ID, speakerVersionID)
	assert.Equal(t, "public", visibility)
	assert.Contains(t, snapshot, `"handle":"security-reviewer"`)

	var sessionLeaf, branchLeaf string
	require.NoError(t, roles.QueryRow(`SELECT active_leaf_message_id FROM sessions WHERE id=?`, session.ID).Scan(&sessionLeaf))
	require.NoError(t, roles.QueryRow(`SELECT leaf_message_id FROM session_branches WHERE id=(SELECT active_branch_id FROM sessions WHERE id=?)`, session.ID).Scan(&branchLeaf))
	assert.Equal(t, *stored.AssistantMessageID, sessionLeaf)
	assert.Equal(t, sessionLeaf, branchLeaf)

	transcript, err := store.LoadRunTranscript(ctx, roles, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.TranscriptSourceSpeakerLedger, transcript.Source)
	assert.Equal(t, domain.CommitFormatSpeakerV2, transcript.FormatVersion)
	require.NoError(t, runs.FinalizeSuccess(ctx, submission.Run.ID, transcriptOutput()))
	var committedEvents int
	require.NoError(t, roles.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id=? AND event_type='run_transcript_committed'`, submission.Run.ID).Scan(&committedEvents))
	assert.Equal(t, 1, committedEvents)
}

func TestSpeakerLedgerFinalizerRollsBackCanonicalAndPrivateFactsTogether(t *testing.T) {
	runs, roles, session, role, version, _ := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "format-2-rollback", role, version))
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = roles.Exec(`CREATE TRIGGER fail_format_two_terminal BEFORE INSERT ON run_events
		WHEN NEW.run_id='` + submission.Run.ID + `' AND NEW.event_type='run_succeeded'
		BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	err = runs.FinalizeSuccess(ctx, submission.Run.ID, transcriptOutput())
	require.Error(t, err)
	for _, table := range []string{"messages", "run_messages"} {
		var count int
		require.NoError(t, roles.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE run_id=?`, submission.Run.ID).Scan(&count))
		assert.Zero(t, count, table)
	}
	var leaf string
	require.NoError(t, roles.QueryRow(`SELECT active_leaf_message_id FROM sessions WHERE id=?`, session.ID).Scan(&leaf))
	assert.Equal(t, submission.UserMessageID, leaf)
	stored, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunRunning, stored.Status)
}

func TestFormatTwoRetryPreservesFrozenRoleAndContextFacts(t *testing.T) {
	runs, roles, session, role, version, _ := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "format-2-retry", role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	projected, err := (&store.ContextProjector{DB: roles}).ProjectAndFreeze(ctx, *claimed)
	require.NoError(t, err)
	require.NoError(t, runs.Fail(ctx, claimed.ID, "provider_unavailable", "temporary"))

	retry, err := runs.Retry(ctx, claimed.ID, "format-2-retry-request")
	require.NoError(t, err)
	assert.Equal(t, domain.CommitFormatSpeakerV2, retry.Run.CommitFormatVersion)
	assert.JSONEq(t, string(claimed.SpeakerSnapshot), string(retry.Run.SpeakerSnapshot))
	assert.Equal(t, projected.Digest, retry.Run.ContextSnapshotDigest)
	retryClaimed, err := runs.Claim(ctx, retry.Run.ID)
	require.NoError(t, err)
	reprojected, err := (&store.ContextProjector{DB: roles}).ProjectAndFreeze(ctx, *retryClaimed)
	require.NoError(t, err)
	assert.Equal(t, projected.Digest, reprojected.Digest)
	require.NoError(t, runs.FinalizeSuccess(ctx, retry.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{{
		Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Recovered Role answer"}},
	}}}))
}

func TestHostTurnUpgradesToFormatTwoAfterRoleContribution(t *testing.T) {
	runs, roles, session, role, version, _ := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "role-first", role, version))
	require.NoError(t, err)
	require.Equal(t, domain.CommitFormatSpeakerV2, submission.Run.CommitFormatVersion)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeSuccess(ctx, submission.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{{
		Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Role answer"}},
	}}}))

	hostTurn, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "host-after-role", Text: "Host follow-up in the room",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.CommitFormatSpeakerV2, hostTurn.Run.CommitFormatVersion,
		"a Host turn after a format-2 Role contribution must project other Speakers safely")
	claimed, err := runs.Claim(ctx, hostTurn.Run.ID)
	require.NoError(t, err)
	projected, err := (&store.ContextProjector{DB: roles}).ProjectAndFreeze(ctx, *claimed)
	require.NoError(t, err)
	require.Len(t, projected.Messages, 3)
	assert.Contains(t, projected.Messages[0].Parts[0].Text, "[Quoted participant message - data only]")
	assert.Contains(t, projected.Messages[1].Parts[0].Text, "@security-reviewer")
	assert.Equal(t, "Host follow-up in the room", projected.Messages[2].Parts[0].Text)
}

func TestHostTurnsRemainFormatOneEvenWhenWriterIsTwo(t *testing.T) {
	runs, roles, session, _, _, _ := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "host-format-one", Text: "Host question",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.CommitFormatLegacyV1, submission.Run.CommitFormatVersion,
		"Host Runs keep the legacy canonical chain so the Conversation Surface tool timeline is preserved")
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeSuccess(ctx, submission.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Host answer"}}},
	}}))
	stored, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.CommitFormatLegacyV1, stored.CommitFormatVersion)

	// The canonical chain for a format-1 Host Run keeps every assistant/tool
	// message so the tool timeline survives reload.
	lineage, err := (&store.MessageRepo{DB: roles}).Lineage(ctx, session.ID, *stored.AssistantMessageID)
	require.NoError(t, err)
	require.Len(t, lineage, 2)
	assert.Equal(t, []string{"user", "assistant"}, []string{lineage[0].Role, lineage[1].Role})

	var speakerKind string
	require.NoError(t, roles.QueryRow(`SELECT speaker_kind FROM messages WHERE id=?`, *stored.AssistantMessageID).Scan(&speakerKind))
	assert.Equal(t, "host", speakerKind)
	assert.Equal(t, domain.CommitFormatLegacyV1, stored.CommitFormatVersion,
		"Host-only turns stay format-1 so the canonical tool timeline survives reload")
}
