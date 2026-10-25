package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func questionSpecs() []domain.QuestionSpec {
	return []domain.QuestionSpec{{
		ID: "scope", Prompt: "Which samples?", AllowOther: true,
		Options: []domain.QuestionOption{{Label: "All"}, {Label: "Tumor only"}},
	}}
}

func TestSuspendQuestionSharesTheApprovalCheckpoint(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	questions, err := domain.NormalizeQuestions(questionSpecs())
	require.NoError(t, err)
	digest, err := domain.QuestionDigest(submission.Run.ID, "question-call", questions)
	require.NoError(t, err)

	request, err := approvals.SuspendQuestion(context.Background(), submission.Run.ID, 1, 1, digest,
		json.RawMessage(`{"version":5}`), questions)
	require.NoError(t, err)
	assert.Equal(t, domain.ApprovalKindQuestion, request.Kind)
	require.Len(t, request.Questions, 1)
	assert.Empty(t, request.Items)

	run, err := runs.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunWaitingForApproval, run.Status)

	pending, err := approvals.FindPendingBySession(context.Background(), submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, domain.ApprovalKindQuestion, pending.Kind)
	require.Len(t, pending.Questions, 1)

	var attentionKind string
	require.NoError(t, runs.DB.QueryRow(`SELECT kind FROM attention_items WHERE source_id=?`, request.ID).Scan(&attentionKind))
	assert.Equal(t, string(domain.AttentionNeedsInput), attentionKind)
}

func TestAnswerQuestionIsIdempotentAndRejectsTheApprovalEndpoint(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	questions, err := domain.NormalizeQuestions(questionSpecs())
	require.NoError(t, err)
	digest, err := domain.QuestionDigest(submission.Run.ID, "question-call", questions)
	require.NoError(t, err)
	request, err := approvals.SuspendQuestion(context.Background(), submission.Run.ID, 1, 1, digest,
		json.RawMessage(`{"version":5}`), questions)
	require.NoError(t, err)

	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "approve-question", nil)
	assert.ErrorIs(t, err, store.ErrApprovalConflict, "the approval endpoint must not resolve a question")

	answers := []domain.QuestionAnswer{{QuestionID: "scope", Values: []string{"Tumor only"}}}
	resolved, err := approvals.Answer(context.Background(), request.ID, answers, "answer-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ApprovalApproved, resolved.Status)
	require.Len(t, resolved.Answers, 1)

	_, err = approvals.Answer(context.Background(), request.ID, answers, "answer-2")
	require.NoError(t, err, "same answers are idempotent across request ids")
	_, err = approvals.Answer(context.Background(), request.ID,
		[]domain.QuestionAnswer{{QuestionID: "scope", Values: []string{"All"}}}, "answer-3")
	assert.ErrorIs(t, err, store.ErrApprovalConflict)

	run, err := runs.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, run.Status)

	_, err = runs.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	resume, err := approvals.BeginResume(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	require.NotNil(t, resume)
	assert.Equal(t, domain.ApprovalKindQuestion, resume.Approval.Kind)
	require.Len(t, resume.Approval.Answers, 1)
}

func TestAnswerRejectsAnApprovalRowAndInvalidOptions(t *testing.T) {
	_, approvals, submission := setupApprovalRun(t)
	approval, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":5}`), approvalItems(), nil)
	require.NoError(t, err)
	_, err = approvals.Answer(context.Background(), approval.ID,
		[]domain.QuestionAnswer{{QuestionID: "scope", Values: []string{"All"}}}, "answer")
	assert.ErrorIs(t, err, store.ErrApprovalConflict)

	questions, err := domain.NormalizeQuestions(questionSpecs())
	require.NoError(t, err)
	_, err = domain.CanonicalAnswers(questions, []domain.QuestionAnswer{{
		QuestionID: "scope", Values: []string{"missing"},
	}})
	assert.Error(t, err)
}

func TestAnswerRejectsStaleQuestion(t *testing.T) {
	_, approvals, submission := setupApprovalRun(t)
	questions, err := domain.NormalizeQuestions(questionSpecs())
	require.NoError(t, err)
	digest, err := domain.QuestionDigest(submission.Run.ID, "question-call", questions)
	require.NoError(t, err)
	request, err := approvals.SuspendQuestion(context.Background(), submission.Run.ID, 1, 1, digest,
		json.RawMessage(`{"version":5}`), questions)
	require.NoError(t, err)
	_, err = approvals.Answer(context.Background(), request.ID,
		[]domain.QuestionAnswer{{QuestionID: "scope"}}, "empty")
	var invalid *domain.QuestionSpec
	_ = invalid
	assert.True(t, errors.Is(err, store.ErrQuestionInvalidAnswer) || errors.Is(err, store.ErrApprovalConflict))
}
