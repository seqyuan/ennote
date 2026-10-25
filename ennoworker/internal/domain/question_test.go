package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleQuestions() []QuestionSpec {
	return []QuestionSpec{{
		ID: "scope", Prompt: "Which samples?", AllowOther: true,
		Options: []QuestionOption{{Label: "All"}, {Label: "Tumor only"}},
	}}
}

func TestNormalizeQuestionsDefaultsOptionIDToLabel(t *testing.T) {
	questions, err := NormalizeQuestions(sampleQuestions())
	require.NoError(t, err)
	assert.Equal(t, "All", questions[0].Options[0].ID)
	assert.Equal(t, "Tumor only", questions[0].Options[1].ID)
}

func TestQuestionDigestBindsRunAndCall(t *testing.T) {
	first, err := QuestionDigest("run-1", "call-1", sampleQuestions())
	require.NoError(t, err)
	second, err := QuestionDigest("run-1", "call-1", sampleQuestions())
	require.NoError(t, err)
	assert.Equal(t, first, second)

	otherRun, err := QuestionDigest("run-2", "call-1", sampleQuestions())
	require.NoError(t, err)
	assert.NotEqual(t, first, otherRun)
}

func TestCanonicalAnswersValidatesSelections(t *testing.T) {
	questions, err := NormalizeQuestions(sampleQuestions())
	require.NoError(t, err)

	answers, err := CanonicalAnswers(questions, []QuestionAnswer{{
		QuestionID: "scope", Values: []string{"Tumor only"},
	}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Tumor only"}, answers[0].Values)

	_, err = CanonicalAnswers(questions, []QuestionAnswer{{QuestionID: "scope", Values: []string{"Missing"}}})
	assert.Error(t, err)
	_, err = CanonicalAnswers(questions, []QuestionAnswer{{QuestionID: "scope"}})
	assert.Error(t, err)
}

func TestCanonicalAnswersAllowsOtherText(t *testing.T) {
	questions, err := NormalizeQuestions(sampleQuestions())
	require.NoError(t, err)
	answers, err := CanonicalAnswers(questions, []QuestionAnswer{{
		QuestionID: "scope", OtherText: "Only the treated arm",
	}})
	require.NoError(t, err)
	assert.Equal(t, "Only the treated arm", answers[0].OtherText)
}

func TestNormalizeQuestionsRejectsDuplicatesAndTooManyOptions(t *testing.T) {
	duplicate := []QuestionSpec{
		{ID: "a", Prompt: "A", Options: []QuestionOption{{Label: "1"}, {Label: "2"}}},
		{ID: "a", Prompt: "B", Options: []QuestionOption{{Label: "1"}, {Label: "2"}}},
	}
	_, err := NormalizeQuestions(duplicate)
	assert.Error(t, err)

	tooMany := []QuestionSpec{{ID: "a", Prompt: "A", Options: make([]QuestionOption, 9)}}
	for i := range tooMany[0].Options {
		tooMany[0].Options[i] = QuestionOption{Label: string(rune('a' + i))}
	}
	_, err = NormalizeQuestions(tooMany)
	assert.Error(t, err)
}
