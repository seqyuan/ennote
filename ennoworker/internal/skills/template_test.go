package skills

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderTrustedTemplateSubstitutesWorkspaceAndSkillDir(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("Work in ${workspace}", vars)
	require.NoError(t, err)
	assert.Equal(t, "Work in /workspace", result)
}

func TestRenderTrustedTemplateSkillDir(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("Read ${skill_dir}/references/a.md", vars)
	require.NoError(t, err)
	assert.Equal(t, "Read /skills/my-skill/references/a.md", result)
}

func TestRenderTrustedTemplateMultiplePlaceholders(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("cd ${workspace} && ls ${skill_dir}", vars)
	require.NoError(t, err)
	assert.Equal(t, "cd /workspace && ls /skills/my-skill", result)
}

func TestRenderTrustedTemplateDollarEscaping(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("$${workspace} is literal", vars)
	require.NoError(t, err)
	assert.Equal(t, "${workspace} is literal", result)
}

func TestRenderTrustedTemplateLiteralEscapedDollarSkillDir(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("$${skill_dir} then ${skill_dir}", vars)
	require.NoError(t, err)
	assert.Equal(t, "${skill_dir} then /skills/my-skill", result)
}

func TestRenderTrustedTemplateUnknownPlaceholderLeftAsIs(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("Use ${HOME:-/tmp}", vars)
	require.NoError(t, err)
	assert.Equal(t, "Use ${HOME:-/tmp}", result)
}

func TestRenderTrustedTemplateShellDefaultSyntaxPreserved(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("Port: ${PORT:-8080}", vars)
	require.NoError(t, err)
	assert.Equal(t, "Port: ${PORT:-8080}", result)
}

func TestRenderTrustedTemplatePlainDollarPreserved(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("Price: $5 and ${workspace}", vars)
	require.NoError(t, err)
	assert.Equal(t, "Price: $5 and /workspace", result)
}

func TestRenderTrustedTemplateNestedNotRecursive(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/${workspace}"}
	result, err := RenderTrustedTemplate("Base: ${skill_dir}", vars)
	require.NoError(t, err)
	assert.Equal(t, "Base: /skills/${workspace}", result)
}

func TestRenderTrustedTemplateNoPlaceholdersUnchanged(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("Hello world, no templates here!", vars)
	require.NoError(t, err)
	assert.Equal(t, "Hello world, no templates here!", result)
}

func TestRenderTrustedTemplateEmptyVarsUnchanged(t *testing.T) {
	result, err := RenderTrustedTemplate("Look at ${workspace}", nil)
	require.NoError(t, err)
	assert.Equal(t, "Look at ${workspace}", result)
}

func TestRenderTrustedTemplateOnlyWorkspace(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace"}
	result, err := RenderTrustedTemplate("Workspace is ${workspace}", vars)
	require.NoError(t, err)
	assert.Equal(t, "Workspace is /workspace", result)
}

func TestRenderTrustedTemplateOnlySkillDir(t *testing.T) {
	vars := map[string]string{"skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("Skill: ${skill_dir}", vars)
	require.NoError(t, err)
	assert.Equal(t, "Skill: /skills/my-skill", result)
}

func TestRenderTrustedTemplateNoClosingBrace(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace"}
	result, err := RenderTrustedTemplate("Incomplete ${workspace", vars)
	require.NoError(t, err)
	assert.Equal(t, "Incomplete ${workspace", result)
}

func TestRenderTrustedTemplateSingleDollarAtEnd(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace"}
	result, err := RenderTrustedTemplate("Trailing dollar $", vars)
	require.NoError(t, err)
	assert.Equal(t, "Trailing dollar $", result)
}

func TestRenderTrustedTemplateMultipleOnSameLine(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/my-skill"}
	result, err := RenderTrustedTemplate("${workspace}/data/${skill_dir}/ref", vars)
	require.NoError(t, err)
	assert.Equal(t, "/workspace/data//skills/my-skill/ref", result)
}

func TestRenderTrustedTemplateBwrapMode(t *testing.T) {
	vars := map[string]string{"workspace": "/workspace", "skill_dir": "/skills/build"}
	result, err := RenderTrustedTemplate("Execute in ${workspace}, load ${skill_dir}", vars)
	require.NoError(t, err)
	assert.Equal(t, "Execute in /workspace, load /skills/build", result)
}

func TestRenderTrustedTemplateNoneMode(t *testing.T) {
	vars := map[string]string{"workspace": ".", "skill_dir": "/tmp/runs/r123/skills/build"}
	result, err := RenderTrustedTemplate("cd ${workspace} && Run ${skill_dir}", vars)
	require.NoError(t, err)
	assert.Equal(t, "cd . && Run /tmp/runs/r123/skills/build", result)
}
