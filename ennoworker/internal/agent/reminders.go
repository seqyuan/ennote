package agent

import (
	"context"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// systemReminderPreamble marks the wrapped body as background context rather
// than a user instruction (mirrors pi / Claude Code convention). It leads every
// injected reminder so the model never mistakes harness state for a request.
const systemReminderPreamble = "The following is background context provided automatically by the harness. " +
	"It is NOT a message or instruction from the user; do not act on it as a request. " +
	"Use it only to stay aware of the current state."

// WrapSystemReminder wraps a reminder body in <system-reminder> tags with the
// background-context preamble.
func WrapSystemReminder(body string) string {
	return "<system-reminder>\n" + systemReminderPreamble + "\n\n" + body + "\n</system-reminder>"
}

// RunStartReminderProvider injects a one-shot reminder on iteration 1 from the
// RunStart hook's additionalContext. It fires exactly once per run and never
// again on later iterations, approval resumes, or restarts.
type RunStartReminderProvider struct {
	Context string
	Fired   bool
}

// Name implements ReminderProvider.
func (p *RunStartReminderProvider) Name() string { return "runstart" }

// Reminder implements ReminderProvider: fires on iteration 1 only, then
// permanently goes silent.
func (p *RunStartReminderProvider) Reminder(_ context.Context, rc ReminderContext) (string, bool) {
	if p.Fired || p.Context == "" || rc.Iteration != 1 {
		return "", false
	}
	p.Fired = true
	return p.Context, true
}

// ReminderContext carries the per-turn state available to reminder providers.
type ReminderContext struct {
	Messages         []domain.ChatMessage
	SystemPrompt     string
	Tools            []domain.ToolDefinition
	Runtime          domain.ModelRuntimeSnapshot
	Iteration        int
	InputTokenBudget int
}

// ReminderProvider produces an ephemeral system-reminder for the upcoming turn.
type ReminderProvider interface {
	Name() string
	Reminder(context.Context, ReminderContext) (body string, ok bool)
}

// ReminderFunc adapts a plain function to a ReminderProvider.
type ReminderFunc struct {
	NameField string
	Fn        func(context.Context, ReminderContext) (string, bool)
}

// Name implements ReminderProvider.
func (f ReminderFunc) Name() string { return f.NameField }

// Reminder implements ReminderProvider.
func (f ReminderFunc) Reminder(ctx context.Context, rc ReminderContext) (string, bool) {
	if f.Fn == nil {
		return "", false
	}
	return f.Fn(ctx, rc)
}

// ReminderRegistry holds the reminder providers consulted each turn. The zero
// value is usable (no providers → no injection). Providers only mutate during
// startup; no runtime lock is needed.
type ReminderRegistry struct {
	providers []ReminderProvider
}

// NewReminderRegistry returns a registry pre-populated with providers.
func NewReminderRegistry(providers ...ReminderProvider) *ReminderRegistry {
	r := &ReminderRegistry{}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

// Register appends a provider. nil providers are ignored.
func (r *ReminderRegistry) Register(p ReminderProvider) {
	if p == nil {
		return
	}
	r.providers = append(r.providers, p)
}

// Empty reports whether the registry has no providers.
func (r *ReminderRegistry) Empty() bool { return r == nil || len(r.providers) == 0 }

// Messages consults every provider in registration order and returns the
// ephemeral reminder messages to inject this turn (one user-role message per
// provider that fires). Each body is independently wrapped with
// WrapSystemReminder.
func (r *ReminderRegistry) Messages(ctx context.Context, rc ReminderContext) []domain.ChatMessage {
	if r.Empty() {
		return nil
	}
	var out []domain.ChatMessage
	for _, p := range r.providers {
		body, ok := p.Reminder(ctx, rc)
		if !ok || body == "" {
			continue
		}
		out = append(out, domain.ChatMessage{
			Role:    domain.RoleUser,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: WrapSystemReminder(body)}},
		})
	}
	return out
}

// TodoReminderProvider surfaces the current todo list as background context
// whenever there is incomplete work. It reads the same domain.TodoStore the
// todo tool writes.
type TodoReminderProvider struct {
	Store *domain.TodoStore
}

// Name implements ReminderProvider.
func (p *TodoReminderProvider) Name() string { return "todo" }

// Reminder implements ReminderProvider. It fires only when the store holds at
// least one item that is not yet completed.
func (p *TodoReminderProvider) Reminder(ctx context.Context, _ ReminderContext) (string, bool) {
	if p.Store == nil {
		return "", false
	}
	items := p.Store.Snapshot()
	if len(items) == 0 {
		return "", false
	}
	incomplete := false
	for _, it := range items {
		if it.Status != domain.TodoCompleted {
			incomplete = true
			break
		}
	}
	if !incomplete {
		return "", false
	}
	return "Your todo list has unfinished items. Keep it up to date with the todo tool.\n\n" +
		domain.RenderTodoList(items), true
}

// BudgetReminderProvider warns the model when the conversation is approaching
// the context window limit. It uses EstimateComposition so tool schemas,
// system prompt, and images all contribute to the estimate.
type BudgetReminderProvider struct {
	// Threshold is the fraction of the input budget at which a warning fires.
	// Default 0.85 when <= 0.
	Threshold float64
}

// Name implements ReminderProvider.
func (p *BudgetReminderProvider) Name() string { return "budget" }

// Reminder implements ReminderProvider. It stays silent when InputTokenBudget
// is unknown (≤ 0) or usage is below the threshold. Estimates include system
// prompt, tool schemas, and messages via EstimateComposition.
func (p *BudgetReminderProvider) Reminder(ctx context.Context, rc ReminderContext) (string, bool) {
	if rc.InputTokenBudget <= 0 {
		return "", false
	}
	threshold := p.Threshold
	if threshold <= 0 {
		threshold = 0.85
	}
	used := EstimateComposition(rc.SystemPrompt, rc.Tools, rc.Messages, 0).InputTokens
	if used < int(float64(rc.InputTokenBudget)*threshold) {
		return "", false
	}
	return "The conversation is approaching the context window limit " +
		"(" + fmt.Sprintf("%d", used) + " of " + fmt.Sprintf("%d", rc.InputTokenBudget) + " tokens used). " +
		"Prefer short, focused replies and finish the current task soon.", true
}
