package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsReadOnlySchedulingTool pins the conservative allowlist that every
// scheduling path (flow publish fan_out, writer-scope accounting, Graph Run
// freeze) shares. Unknown tools and all MCP tools must fail closed.
func TestIsReadOnlySchedulingTool(t *testing.T) {
	readOnly := []string{
		"read", "ls", "grep", "find", "git_readonly",
		"search_compacted_history", "todo", "ask_user",
	}
	for _, tool := range readOnly {
		assert.True(t, IsReadOnlySchedulingTool(tool), "%q must be read-only", tool)
	}

	mutating := []string{
		"bash", "exec", "write", "edit", "publish_artifact",
		"web_fetch", "http", "delegate_tasks",
		// MCP tools are never inferred read-only from their name, even when the
		// name suggests a read ("__read" / no "__write__" substring).
		"mcp__server__read", "mcp__server__write", "mcp__server__search",
		// Legacy / historical names must not leak in by accident.
		"bash_read", "sql_read",
		// Unknown and empty tools fail closed.
		"totally_unknown_tool", "",
	}
	for _, tool := range mutating {
		assert.False(t, IsReadOnlySchedulingTool(tool), "%q must not be read-only", tool)
	}
}

func TestHeroToolRequiresMutation(t *testing.T) {
	mutating := []string{"write", "edit", "exec", "bash", "publish_artifact"}
	for _, tool := range mutating {
		assert.True(t, HeroToolRequiresMutation(tool), "%q must require mutation", tool)
	}
	readOnly := []string{"read", "ls", "grep", "find", "git_readonly", "web_fetch", "http", "delegate_tasks", "", "mcp__server__write"}
	for _, tool := range readOnly {
		assert.False(t, HeroToolRequiresMutation(tool), "%q must not require mutation", tool)
	}
}

func TestHeroDefinitionIsReadOnly(t *testing.T) {
	cases := []struct {
		name string
		hero HeroDefinition
		want bool
	}{
		{
			name: "read_only authority with read-only tools",
			hero: HeroDefinition{Authority: HeroAuthorityReadOnly,
				AllowedTools: []string{"read", "ls", "grep", "find", "git_readonly", "ask_user"}},
			want: true,
		},
		{
			name: "read_only authority with no tools",
			hero: HeroDefinition{Authority: HeroAuthorityReadOnly},
			want: true,
		},
		{
			name: "read_only authority with an MCP tool",
			hero: HeroDefinition{Authority: HeroAuthorityReadOnly,
				AllowedTools: []string{"read", "mcp__server__read"}},
			want: false,
		},
		{
			name: "read_only authority with an unknown tool",
			hero: HeroDefinition{Authority: HeroAuthorityReadOnly,
				AllowedTools: []string{"read", "mystery_tool"}},
			want: false,
		},
		{
			name: "read_only authority with a mutating tool",
			hero: HeroDefinition{Authority: HeroAuthorityReadOnly,
				AllowedTools: []string{"read", "write"}},
			want: false,
		},
		{
			name: "mutation authority with read-only tools",
			hero: HeroDefinition{Authority: HeroAuthorityMutation,
				AllowedTools: []string{"read", "grep"}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, HeroDefinitionIsReadOnly(tc.hero))
		})
	}
}
