package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateSkillToolDelegatesToInjectedCreator(t *testing.T) {
	var captured CreateSkillInput
	tool := &CreateSkillTool{Create: func(input CreateSkillInput) (CreateSkillResult, error) {
		captured = input
		return CreateSkillResult{ID: input.ID, Path: "/home/u/.ennote/skills/" + input.ID,
			Description: "draft", AllowedTools: input.AllowedTools}, nil
	}}
	args, err := json.Marshal(map[string]any{
		"id": "pdf-report", "name": "PDF Report", "description": "draft",
		"prompt": "Write a report.", "allowedTools": []string{"read", "bash"},
	})
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "c1", Name: "create_skill", Arguments: args})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, "pdf-report", captured.ID)
	assert.Equal(t, "PDF Report", captured.Name)
	assert.Equal(t, []string{"read", "bash"}, captured.AllowedTools)
	assert.Contains(t, result.Content, "Created skill \"pdf-report\"")
	assert.Contains(t, result.Content, "no Worker restart")
}

func TestCreateSkillToolReportsCreatorErrors(t *testing.T) {
	tool := &CreateSkillTool{Create: func(CreateSkillInput) (CreateSkillResult, error) {
		return CreateSkillResult{}, assert.AnError
	}}
	args, err := json.Marshal(map[string]any{"id": "x", "prompt": "body"})
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "c1", Name: "create_skill", Arguments: args})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, assert.AnError.Error())
}

func TestCreateSkillToolRiskAndSchema(t *testing.T) {
	tool := &CreateSkillTool{}
	def := tool.Definition()
	assert.Equal(t, "create_skill", def.Name)
	assert.Equal(t, domain.RiskLocalWrite, def.RiskClass)
	assert.Contains(t, string(def.Parameters), `"required":["id","prompt"]`)
}
