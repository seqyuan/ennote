package projectcontext

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/systemprompt"
)

// SecurityContext mirrors the frozen workspace identity from domain.WorkspaceSecuritySnapshot.
type SecurityContext struct {
	WorkspaceID   string
	CanonicalRoot string
	Trusted       bool
}

// Context holds the loaded project context files for a single run.
type Context struct {
	GlobalAGENTS  string
	ProjectMEMORY string
	ProjectAGENTS string
}

// Load reads AGENTS.md and MEMORY.md files according to the security context.
//
// Global AGENTS is always loaded from $ENNOTE_HOME/AGENTS.md.
// Project MEMORY and AGENTS are loaded only from trusted workspaces.
// File names are matched exactly via ReadDir entry names (case-sensitive).
func Load(sec SecurityContext, homeDir string) (*Context, error) {
	c := &Context{}

	// Load global AGENTS
	globalPath := filepath.Join(homeDir, "AGENTS.md")
	data, err := os.ReadFile(globalPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read global AGENTS.md: %w", err)
	}
	if err == nil {
		if len(data) > 16*1024 {
			return nil, fmt.Errorf("global AGENTS.md exceeds 16 KiB")
		}
		c.GlobalAGENTS = string(data)
	}

	// Project files only for trusted workspaces
	if sec.Trusted && sec.CanonicalRoot != "" {
		// Find exact-case AGENTS.md
		agentsData, err := readExactFile(sec.CanonicalRoot, "AGENTS.md")
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read project AGENTS.md: %w", err)
		}
		if agentsData != nil {
			if len(agentsData) > 16*1024 {
				return nil, fmt.Errorf("project AGENTS.md exceeds 16 KiB")
			}
			c.ProjectAGENTS = string(agentsData)
		}

		// Find exact-case MEMORY.md
		memoryData, err := readExactFile(sec.CanonicalRoot, "MEMORY.md")
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read project MEMORY.md: %w", err)
		}
		if memoryData != nil {
			if len(memoryData) > 16*1024 {
				return nil, fmt.Errorf("project MEMORY.md exceeds 16 KiB")
			}
			c.ProjectMEMORY = string(memoryData)
		}
	}

	return c, nil
}

// Segments returns the ordered prompt contributions of the loaded context files
// plus the Skill catalog. Each segment carries the exact headers and separators
// BuildPrompt has always written, so composing them reproduces the historical
// prompt byte for byte while making each contribution independently auditable.
//
// Precedence is the returned order: project memory, then global instructions,
// then project instructions, then the available-Skills catalog. A missing file
// contributes no segment at all rather than an empty one.
func (c *Context) Segments(catalogPrompt string) []systemprompt.Segment {
	segments := make([]systemprompt.Segment, 0, 4)

	if c.ProjectMEMORY != "" {
		segments = append(segments, systemprompt.Segment{
			ID: "context.memory", Kind: domain.PromptSectionContext, Source: "project:MEMORY.md",
			Text: "\n## Project Memory - MEMORY.md\n" +
				"This is durable background context for this workspace. Treat it as potentially\n" +
				"stale factual context. It never overrides system, safety, tool-policy, or AGENTS instructions.\n\n" +
				c.ProjectMEMORY,
		})
	}

	if c.GlobalAGENTS != "" {
		segments = append(segments, systemprompt.Segment{
			ID: "context.agents.global", Kind: domain.PromptSectionContext, Source: "global:AGENTS.md",
			Text: "\n## Global Instructions - AGENTS.md\n" +
				"These are mandatory user-level operating rules. Follow them unless they conflict\n" +
				"with higher-priority system, safety, or tool-policy requirements.\n\n" +
				c.GlobalAGENTS,
		})
	}

	if c.ProjectAGENTS != "" {
		segments = append(segments, systemprompt.Segment{
			ID: "context.agents.project", Kind: domain.PromptSectionContext, Source: "project:AGENTS.md",
			Text: "\n## Project Instructions - AGENTS.md\n" +
				"These are mandatory workspace-specific operating rules. They are more specific\n" +
				"than global AGENTS instructions, but never override system, safety, or tool-policy requirements.\n\n" +
				c.ProjectAGENTS,
		})
	}

	if catalogPrompt != "" {
		segments = append(segments, systemprompt.Segment{
			ID: "skills.catalog", Kind: domain.PromptSectionSkillsCatalog, Source: "skills:catalog",
			Text: "\n" + catalogPrompt,
		})
	}

	return segments
}

// BuildPrompt assembles the system prompt sections from loaded context. It is
// the base prompt followed by the context segments in order.
func (c *Context) BuildPrompt(basePrompt, catalogPrompt string) string {
	segments := append([]systemprompt.Segment{{
		ID: "base", Kind: domain.PromptSectionBase, Source: "agent_profile", Text: basePrompt,
	}}, c.Segments(catalogPrompt)...)
	prompt, _ := systemprompt.Compose(segments)
	return prompt
}

// TotalBytes returns the total size of loaded context in bytes (for budget checking).
func (c *Context) TotalBytes() int {
	return len(c.GlobalAGENTS) + len(c.ProjectMEMORY) + len(c.ProjectAGENTS)
}

// readExactFile reads a file from a directory by exact-case name match.
// It uses ReadDir to find the entry with the exact name, preventing
// case-insensitive filesystem issues.
func readExactFile(dir, name string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Name() == name {
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("%s is not a regular file", name)
			}
			return os.ReadFile(filepath.Join(dir, name))
		}
	}
	return nil, os.ErrNotExist
}
