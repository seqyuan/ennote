package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Agent-facing create_skill tool must write through skillsmgmt into the
// Worker user skills root, and the result must load from the live catalog.
func TestCreateSkillToolWritesThroughWorkerService(t *testing.T) {
	skillsDir := t.TempDir()
	executor := &agentExecutor{skillsDir: skillsDir, homeDir: t.TempDir()}
	registry, err := tools.NewRegistry()
	require.NoError(t, err)
	require.NoError(t, executor.registerCreateSkill(registry))

	args, err := json.Marshal(map[string]any{
		"id": "agent-authored", "description": "Created by the agent",
		"prompt": "Inspect the input and report.", "allowedTools": []string{"read"},
	})
	require.NoError(t, err)
	result, err := registry.Execute(context.Background(),
		domain.ToolCall{ID: "call", Name: "create_skill", Arguments: args})
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)

	skill, err := skills.Load(filepath.Join(skillsDir, "agent-authored"))
	require.NoError(t, err)
	assert.Equal(t, "agent-authored", skill.Manifest.ID)
	assert.Contains(t, skill.PromptText, "Inspect the input")
	assert.Equal(t, []string{"read"}, skill.Manifest.AllowedTools)

	_, statErr := os.Stat(filepath.Join(skillsDir, "agent-authored", "skill.json"))
	require.NoError(t, statErr)
}
