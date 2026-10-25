package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancelResolvesQuestionAttention(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	questions, err := domain.NormalizeQuestions(questionSpecs())
	require.NoError(t, err)
	digest, err := domain.QuestionDigest(submission.Run.ID, "question-call", questions)
	require.NoError(t, err)
	request, err := approvals.SuspendQuestion(context.Background(), submission.Run.ID, 1, 1, digest,
		json.RawMessage(`{"version":5}`), questions)
	require.NoError(t, err)

	require.NoError(t, runs.Cancel(context.Background(), submission.Run.ID))
	var approvalStatus, attentionStatus string
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM tool_approval_requests WHERE id=?`, request.ID).Scan(&approvalStatus))
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM attention_items WHERE source_id=?`, request.ID).Scan(&attentionStatus))
	assert.Equal(t, string(domain.ApprovalCancelled), approvalStatus)
	assert.Equal(t, "resolved", attentionStatus)
}
