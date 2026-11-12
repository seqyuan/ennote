package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type PublishArtifactTool struct {
	Jail *workspace.Jail
	Sink *ArtifactSink
}

func (t *PublishArtifactTool) ExecutionClass() domain.ExecutionClass {
	return domain.ExecutionWorkspaceWrite
}

func (t *PublishArtifactTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name: "publish_artifact",
		Description: "Publish one completed Workspace file as an immutable conversation artifact. " +
			"Files are never published automatically. " +
			"kind: \"widget\" publishes a self-contained interactive HTML page (.html, UTF-8, at most 128 KiB) " +
			"that the reader sees in an isolated frame: no network, no cookies, no Workspace access, no eval. " +
			"Prefer inline SVG/CSS and inline data over external libraries; the only allowed script hosts are " +
			"cdnjs.cloudflare.com, cdn.jsdelivr.net, unpkg.com and esm.sh. Read/fetch the data yourself and inline a bounded " +
			"snapshot; a widget cannot fetch anything. Style with var(--color-text-primary), var(--color-background-primary), " +
			"var(--color-border-secondary), var(--font-sans), var(--font-mono); the frame is 320-900 px wide, so never fix " +
			"a pixel width. Omit kind for ordinary files. Guide: /skills/generative-canvas/SKILL.md",
		Parameters: schema(`{"type":"object","properties":{"path":{"type":"string","description":"Workspace path of the completed file"},"name":{"type":"string","description":"Optional download filename"},"kind":{"type":"string","enum":["auto","widget"],"description":"auto infers from content and never yields a widget; widget renders an interactive HTML page in an isolated frame"}},"required":["path"],"additionalProperties":false}`),
		RiskClass:  domain.RiskLocalWrite,
	}
}

func (t *PublishArtifactTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var args struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid publish_artifact arguments: %w", err)), nil
	}
	if strings.TrimSpace(args.Path) == "" {
		return errorResult(call, fmt.Errorf("path must not be empty")), nil
	}
	// A widget is only ever produced on request. `auto` means "classify by content",
	// and that classifier turns .html into static_html — the script-free kind.
	// Case and surrounding space are normalised, which cannot weaken anything: the
	// only way to reach the widget path is still to name it.
	kind := strings.ToLower(strings.TrimSpace(args.Kind))
	switch kind {
	case "", "auto":
		kind = ""
	case domain.ArtifactKindWidget:
	default:
		return errorResult(call, fmt.Errorf("kind must be \"auto\" or \"widget\"")), nil
	}
	resolved, err := t.Jail.ResolveWorkspaceExisting(args.Path)
	if err != nil {
		return errorResult(call, err), nil
	}
	if kind == domain.ArtifactKindWidget {
		// Checked against the Workspace path as well as the artifact name, because the
		// name is the model's to choose and the error message should point at the file
		// the reader can actually see.
		switch strings.ToLower(filepath.Ext(resolved)) {
		case ".html", ".htm":
		default:
			return errorResult(call, fmt.Errorf("a widget must be published from an .html file")), nil
		}
	}
	file, err := os.Open(resolved)
	if err != nil {
		return errorResult(call, err), nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return errorResult(call, err), nil
	}
	if !info.Mode().IsRegular() {
		return errorResult(call, fmt.Errorf("artifact source must be a regular file")), nil
	}
	displayPath, err := openedWorkspacePath(t.Jail, file)
	if err != nil {
		return errorResult(call, err), nil
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		name = filepath.Base(resolved)
	}
	artifact, err := t.Sink.PublishAs(ctx, call.ID, name, "workspace_publish", displayPath, kind, file)
	if err != nil {
		return errorResult(call, err), nil
	}
	content := describeArtifacts([]domain.ArtifactReference{artifact.Reference()})
	if note := previewDropWarning(artifact.PreviewDrops, artifact.PreviewDropsTotal); note != "" {
		content += "\n" + note
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
		Content:   content,
		Artifacts: []domain.ArtifactReference{artifact.Reference()}}, nil
}

// previewDropWarning is what turns a silent loss into a decision the model can
// still reverse: the same bytes published as a widget keep the SVG, the script
// and the interactivity, because a widget is served verbatim in a sandboxed
// frame. Empty drops produce no line at all, so a clean publish reads exactly as
// before.
func previewDropWarning(drops []string, total int) string {
	if len(drops) == 0 {
		return ""
	}
	listed := strings.Join(drops, ", ")
	if total > len(drops) {
		// The list is capped, and the model must not read the cap as the whole
		// answer: fixing only the shown kinds and republishing drops the rest again.
		listed = fmt.Sprintf("%s (and %d more)", listed, total-len(drops))
	}
	pronoun := "them"
	if total == 1 {
		pronoun = "it"
	}
	return fmt.Sprintf("warning: the preview drops %s; publish with kind %q to keep %s interactive",
		listed, domain.ArtifactKindWidget, pronoun)
}

func openedWorkspacePath(jail *workspace.Jail, file *os.File) (string, error) {
	for _, descriptorRoot := range []string{"/proc/self/fd", "/dev/fd"} {
		descriptorPath := filepath.Join(descriptorRoot, fmt.Sprint(file.Fd()))
		canonical, err := filepath.EvalSymlinks(descriptorPath)
		if err != nil {
			continue
		}
		return jail.DisplayWorkspacePath(canonical)
	}
	return "", fmt.Errorf("secure artifact source verification is unavailable on this platform")
}
