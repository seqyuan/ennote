package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ApprovalKind distinguishes a tool-approval checkpoint from a structured
// question checkpoint. Both share the approval checkpoint table and Run status
// so there is exactly one pause/resume mechanism.
type ApprovalKind string

const (
	ApprovalKindTool     ApprovalKind = "tool_approval"
	ApprovalKindQuestion ApprovalKind = "question"
)

type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type QuestionSpec struct {
	ID          string           `json:"id"`
	Prompt      string           `json:"prompt"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multiSelect,omitempty"`
	AllowOther  bool             `json:"allowOther,omitempty"`
}

type QuestionAnswer struct {
	QuestionID string   `json:"questionId"`
	Values     []string `json:"values,omitempty"`
	OtherText  string   `json:"otherText,omitempty"`
}

// AskUserArguments is the raw tool payload. The dispatcher parses it before
// any tool executes; the tool itself is a marker.
type AskUserArguments struct {
	Questions []QuestionSpec `json:"questions"`
}

func ParseAskUserQuestions(raw []byte) ([]QuestionSpec, error) {
	var args AskUserArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("decode ask_user arguments: %w", err)
	}
	return NormalizeQuestions(args.Questions)
}

var questionIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

const (
	maxQuestions        = 4
	maxQuestionOptions  = 8
	maxQuestionIDBytes  = 256
	maxPromptBytes      = 500
	maxOptionLabelBytes = 120
	maxOtherTextBytes   = 2000
	maxQuestionsJSON    = 16 * 1024
	maxAnswersJSON      = 16 * 1024
)

// NormalizeQuestions trims model-provided text, defaults option IDs to labels,
// and rejects unknown structure before it can reach a checkpoint.
func NormalizeQuestions(input []QuestionSpec) ([]QuestionSpec, error) {
	if len(input) == 0 || len(input) > maxQuestions {
		return nil, fmt.Errorf("ask_user requires 1-%d questions", maxQuestions)
	}
	normalized := make([]QuestionSpec, len(input))
	seenIDs := make(map[string]bool, len(input))
	for index, question := range input {
		question.ID = strings.TrimSpace(question.ID)
		question.Prompt = strings.TrimSpace(question.Prompt)
		if !questionIDPattern.MatchString(question.ID) {
			return nil, fmt.Errorf("question %d has an invalid id", index)
		}
		if seenIDs[question.ID] {
			return nil, fmt.Errorf("duplicate question id %q", question.ID)
		}
		seenIDs[question.ID] = true
		if question.Prompt == "" || len(question.Prompt) > maxPromptBytes {
			return nil, fmt.Errorf("question %q has an invalid prompt", question.ID)
		}
		if len(question.Options) < 2 || len(question.Options) > maxQuestionOptions {
			return nil, fmt.Errorf("question %q requires 2-%d options", question.ID, maxQuestionOptions)
		}
		seenOptions := make(map[string]bool, len(question.Options))
		options := make([]QuestionOption, len(question.Options))
		for optionIndex, option := range question.Options {
			option.Label = strings.TrimSpace(option.Label)
			option.ID = strings.TrimSpace(option.ID)
			option.Description = strings.TrimSpace(option.Description)
			if option.Label == "" || len(option.Label) > maxOptionLabelBytes {
				return nil, fmt.Errorf("question %q option %d has an invalid label", question.ID, optionIndex)
			}
			if option.ID == "" {
				option.ID = option.Label
			}
			if seenOptions[option.ID] {
				return nil, fmt.Errorf("question %q has duplicate option id %q", question.ID, option.ID)
			}
			seenOptions[option.ID] = true
			options[optionIndex] = option
		}
		question.Options = options
		normalized[index] = question
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxQuestionsJSON {
		return nil, fmt.Errorf("ask_user payload exceeds %d bytes", maxQuestionsJSON)
	}
	return normalized, nil
}

// CanonicalQuestions is the stable serialization used by the checkpoint digest.
func CanonicalQuestions(questions []QuestionSpec) ([]byte, error) {
	normalized, err := NormalizeQuestions(questions)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

// QuestionDigest binds a question checkpoint to the exact Run, tool call, and
// question payload. It never uses the approval batch digest algorithm.
func QuestionDigest(runID, toolCallID string, questions []QuestionSpec) (string, error) {
	canonical, err := CanonicalQuestions(questions)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "question\x00%d\x00%s\x00%s\x00", 1, runID, toolCallID)
	hash.Write(canonical)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// CanonicalAnswers normalizes answer order and multi-select value order so
// idempotent retries compare equal.
func CanonicalAnswers(questions []QuestionSpec, answers []QuestionAnswer) ([]QuestionAnswer, error) {
	normalizedQuestions, err := NormalizeQuestions(questions)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]QuestionAnswer, len(answers))
	for _, answer := range answers {
		if _, exists := byID[answer.QuestionID]; exists {
			return nil, fmt.Errorf("duplicate answer for question %q", answer.QuestionID)
		}
		byID[answer.QuestionID] = answer
	}
	result := make([]QuestionAnswer, 0, len(normalizedQuestions))
	for _, question := range normalizedQuestions {
		answer, ok := byID[question.ID]
		if !ok {
			return nil, fmt.Errorf("missing answer for question %q", question.ID)
		}
		allowed := make(map[string]bool, len(question.Options))
		for _, option := range question.Options {
			allowed[option.ID] = true
		}
		values := make([]string, 0, len(answer.Values))
		seen := make(map[string]bool, len(answer.Values))
		for _, value := range answer.Values {
			if !allowed[value] {
				return nil, fmt.Errorf("unknown option %q for question %q", value, question.ID)
			}
			if seen[value] {
				continue
			}
			seen[value] = true
			values = append(values, value)
		}
		other := strings.TrimSpace(answer.OtherText)
		if len(other) > maxOtherTextBytes {
			return nil, fmt.Errorf("other text for question %q is too long", question.ID)
		}
		if other != "" && !question.AllowOther {
			return nil, fmt.Errorf("question %q does not allow other text", question.ID)
		}
		if question.MultiSelect {
			if len(values) == 0 && other == "" {
				return nil, fmt.Errorf("question %q requires at least one selection", question.ID)
			}
		} else if len(values) > 1 {
			return nil, fmt.Errorf("question %q allows one selection", question.ID)
		} else if len(values) == 0 && other == "" {
			return nil, fmt.Errorf("question %q requires an answer", question.ID)
		}
		result = append(result, QuestionAnswer{QuestionID: question.ID, Values: values, OtherText: other})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxAnswersJSON {
		return nil, fmt.Errorf("answers exceed %d bytes", maxAnswersJSON)
	}
	return result, nil
}
