package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuestionAPIRehydratesAndAnswersPendingQuestion(t *testing.T) {
	control := &fakeController{}
	server, handler := setupServer(t, control)
	ctx := context.Background()
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "question", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	_ = bindAPISession(t, server, ctx, session.ID)
	submission, err := server.Runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "question-turn", Text: "ask",
	})
	require.NoError(t, err)
	_, err = server.Runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	questions, err := domain.NormalizeQuestions([]domain.QuestionSpec{{
		ID: "scope", Prompt: "Which samples?", AllowOther: true,
		Options: []domain.QuestionOption{{Label: "All"}, {Label: "Tumor only"}},
	}})
	require.NoError(t, err)
	digest, err := domain.QuestionDigest(submission.Run.ID, "question-call", questions)
	require.NoError(t, err)
	requestRecord, err := server.Approvals.SuspendQuestion(ctx, submission.Run.ID, 1, 1, digest,
		json.RawMessage(`{"version":5}`), questions)
	require.NoError(t, err)

	activeResponse := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/active-run", nil, true)
	require.Equal(t, http.StatusOK, activeResponse.Code, activeResponse.Body.String())
	var active domain.ActiveRunState
	decodeData(t, activeResponse, &active)
	require.NotNil(t, active.PendingApproval)
	assert.Equal(t, domain.ApprovalKindQuestion, active.PendingApproval.Kind)
	require.Len(t, active.PendingApproval.Questions, 1)

	answer := request(t, handler, http.MethodPost,
		"/v1/approval-requests/"+requestRecord.ID+"/answer",
		map[string]any{
			"clientRequestId": "answer-1",
			"answers":         []map[string]any{{"questionId": "scope", "values": []string{"Tumor only"}}},
		}, true)
	require.Equal(t, http.StatusOK, answer.Code, answer.Body.String())
	control.mu.Lock()
	assert.Equal(t, []string{submission.Run.ID}, control.enqueued)
	control.mu.Unlock()

	conflict := request(t, handler, http.MethodPost,
		"/v1/approval-requests/"+requestRecord.ID+"/answer",
		map[string]any{
			"clientRequestId": "answer-2",
			"answers":         []map[string]any{{"questionId": "scope", "values": []string{"All"}}},
		}, true)
	assert.Equal(t, http.StatusConflict, conflict.Code)

	missing := request(t, handler, http.MethodPost,
		"/v1/approval-requests/missing/answer",
		map[string]any{"answers": []map[string]any{}}, true)
	assert.Equal(t, http.StatusNotFound, missing.Code)
}
