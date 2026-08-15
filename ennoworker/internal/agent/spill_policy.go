package agent

import (
	"context"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/spill"
)

// SpillPostHook is the post-execute consumer of the spill seam (design 二 P2).
// It runs on the post chain: results over maxInlineBytes are persisted in full
// and their model-facing projection becomes a head/tail preview plus the spill
// locator. Save failures keep the original inline result — best-effort, never
// turning a successful call into an error.
func SpillPostHook(store spill.SpillStore, maxInlineBytes int64, owner func(exec *ToolExecution) spill.Owner) PostToolHook {
	return func(ctx context.Context, exec *ToolExecution, result domain.ToolResult, next func(domain.ToolResult) (PostToolDecision, error)) (PostToolDecision, error) {
		d, err := next(result)
		if err != nil {
			return d, err
		}
		if store == nil || owner == nil || maxInlineBytes <= 0 || int64(len(d.Result.Content)) <= maxInlineBytes {
			return d, nil
		}
		ref, saveErr := store.SaveText(ctx, spill.SaveInput{
			Owner:         owner(exec),
			Source:        spill.Source{ToolName: d.Result.ToolName, CallID: d.Result.ToolCallID, Label: "result"},
			SuggestedName: d.Result.ToolName + ".txt",
			Content:       d.Result.Content,
		})
		if saveErr != nil {
			return d, nil // best-effort: keep the inline result
		}
		d.Result.Content = fmt.Sprintf("[spilled %d bytes to %s — %s]\n%s", ref.Bytes, ref.Locator, ref.RetrievalHint, spillPreview(d.Result.Content, 4000))
		return d, nil
	}
}

// spillPreview returns a rune-safe head/tail window of content.
func spillPreview(content string, maxRunes int) string {
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content
	}
	head := maxRunes * 3 / 4
	tail := maxRunes / 4
	return string(runes[:head]) + "\n...[content spilled, truncated preview]...\n" + string(runes[len(runes)-tail:])
}
