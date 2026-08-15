package prompts

import "fmt"

// Sentinel errors for request-level failures that map to HTTP error codes.
var (
	// ErrPromptResourceLimit is returned when request-wide file/byte budget
	// is exhausted during a project catalog/expand resolve. Maps to
	// 500 prompt_resource_limit.
	ErrPromptResourceLimit = fmt.Errorf("prompt resource limit exceeded")

	// ErrPromptStorageUnavailable is returned when the global store cannot be
	// fully scanned (e.g. over 2,000 entries) during a project resolve.
	// Maps to 500 prompt_storage_unavailable.
	ErrPromptStorageUnavailable = fmt.Errorf("prompt storage unavailable")

	// ErrPromptConfigInvalid is returned when config.json's prompts.paths is
	// unreadable or invalid during a project resolve. Maps to
	// 500 prompt_config_invalid.
	ErrPromptConfigInvalid = fmt.Errorf("prompt configuration invalid")

	// ErrWorkspaceTrustUnavailable is returned when the trust store cannot be
	// read. Never degrade to trusted. Maps to 500 workspace_trust_unavailable.
	ErrWorkspaceTrustUnavailable = fmt.Errorf("workspace trust unavailable")

	// ErrInvocationTooLarge is returned when an expand invocation exceeds
	// 16 KiB. Maps to 413 payload_too_large.
	ErrInvocationTooLarge = fmt.Errorf("invocation exceeds 16 KiB")

	// ErrPromptTemplateTooLarge is returned when a canonical serialized
	// global template exceeds 64 KiB. Maps to 413 prompt_template_too_large.
	ErrPromptTemplateTooLarge = fmt.Errorf("serialized template exceeds 64 KiB")

	// ErrProjectNotFound is returned when a project has no active workspace.
	// Maps to 404.
	ErrProjectNotFound = fmt.Errorf("project or workspace not found")
)

// Tier is the priority tier of a template source. Higher values win in
// same-name conflicts. Declared in ascending priority order so natural >
// comparison matches "higher priority wins".
type Tier int

const (
	TierBuiltin Tier = iota // lowest priority
	TierSettings
	TierGlobal
	TierProject // highest priority
)

func (t Tier) String() string {
	switch t {
	case TierBuiltin:
		return "builtin"
	case TierSettings:
		return "settings"
	case TierGlobal:
		return "global"
	case TierProject:
		return "project"
	default:
		return "unknown"
	}
}

// TemplateSummary is the public-facing catalog entry (no body).
type TemplateSummary struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argumentHint"`
	Source       string `json:"source"`
	Editable     bool   `json:"editable"`
}

// GlobalPromptTemplateEntry represents a file in the global store directory.
// Valid entries carry description/argumentHint; invalid entries have
// Valid:false and a Diagnostic. Only editable:true entries can be PUT
// repaired or DELETEd.
type GlobalPromptTemplateEntry struct {
	Name         string      `json:"name"`
	Description  string      `json:"description,omitempty"`
	ArgumentHint string      `json:"argumentHint,omitempty"`
	Valid        bool        `json:"valid"`
	Editable     bool        `json:"editable"`
	Diagnostic   *Diagnostic `json:"diagnostic,omitempty"`
}

// Diagnostic carries a non-fatal message about template loading.
type Diagnostic struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Source  string `json:"source"`
	Path    string `json:"path,omitempty"`
	Name    string `json:"name,omitempty"`
	Message string `json:"message"`
}

// ResolveContext carries the per-request parameters for registry resolution.
type ResolveContext struct {
	HomeDir       string
	WorkspaceID   string
	CanonicalRoot string
	Trusted       bool
	SettingsPaths []string
}

// ResolveBudget tracks request-wide resource counters.
// DirectoryEntriesRemaining is a soft per-source budget (exhaustion in one
// source rolls back that source only). TemplateFilesRemaining and
// TemplateBytesRemaining are hard budgets (exhaustion triggers 500).
// Diagnostics are capped at 512 at response time via sanitizeDiagnostics,
// with a diagnostics_truncated warning appended when the limit is exceeded.
type ResolveBudget struct {
	DirectoryEntriesRemaining int
	TemplateFilesRemaining    int
	TemplateBytesRemaining    int64
}

// NewResolveBudget creates a full request-local budget.
func NewResolveBudget() *ResolveBudget {
	return &ResolveBudget{
		DirectoryEntriesRemaining: 16384,
		TemplateFilesRemaining:    4096,
		TemplateBytesRemaining:    32 * 1024 * 1024, // 32 MiB
	}
}

// ExpandCase is the JSON/Go/OpenAPI discriminant for expand results. Zero
// value is illegal; always construct via the constructor functions.
type ExpandCase string

const (
	ExpandCaseMatched           ExpandCase = "matched"
	ExpandCaseNotFound          ExpandCase = "not_found"
	ExpandCaseInvalidInvocation ExpandCase = "invalid_invocation"
)

// ExpandResult is the structured result of expanding a slash invocation.
// Use the constructor functions; direct struct literals are invalid.
type ExpandResult struct {
	Case        ExpandCase
	Name        string
	Text        string
	Diagnostics []Diagnostic
}

// NewMatchedExpand creates a successful expansion result.
func NewMatchedExpand(name, text string, diagnostics []Diagnostic) (ExpandResult, error) {
	if name == "" {
		return ExpandResult{}, fmt.Errorf("NewMatchedExpand: name must not be empty")
	}
	if text == "" || isBlank(text) {
		return ExpandResult{}, fmt.Errorf("NewMatchedExpand: text must not be empty or whitespace-only")
	}
	return ExpandResult{
		Case:        ExpandCaseMatched,
		Name:        name,
		Text:        text,
		Diagnostics: sanitizeDiagnostics(diagnostics),
	}, nil
}

// NewNotFoundExpand creates a "template not found" result.
func NewNotFoundExpand(name string, diagnostics []Diagnostic) (ExpandResult, error) {
	if name == "" {
		return ExpandResult{}, fmt.Errorf("NewNotFoundExpand: name must not be empty")
	}
	return ExpandResult{
		Case:        ExpandCaseNotFound,
		Name:        name,
		Diagnostics: sanitizeDiagnostics(diagnostics),
	}, nil
}

// NewInvalidInvocationExpand creates an "invalid invocation" result (parser
// fast path — no infrastructure was read).
func NewInvalidInvocationExpand() ExpandResult {
	return ExpandResult{
		Case:        ExpandCaseInvalidInvocation,
		Diagnostics: []Diagnostic{},
	}
}

// Validate checks the internal invariants of the result. Call before
// serializing to JSON.
func (r ExpandResult) Validate() error {
	switch r.Case {
	case ExpandCaseMatched:
		if r.Name == "" {
			return fmt.Errorf("matched result must have non-empty name")
		}
		if r.Text == "" || isBlank(r.Text) {
			return fmt.Errorf("matched result must have non-blank text")
		}
		return nil
	case ExpandCaseNotFound:
		if r.Name == "" {
			return fmt.Errorf("not_found result must have non-empty name")
		}
		if r.Text != "" {
			return fmt.Errorf("not_found result must have empty text")
		}
		return nil
	case ExpandCaseInvalidInvocation:
		if r.Name != "" {
			return fmt.Errorf("invalid_invocation result must have empty name")
		}
		if r.Text != "" {
			return fmt.Errorf("invalid_invocation result must have empty text")
		}
		if len(r.Diagnostics) != 0 {
			return fmt.Errorf("invalid_invocation result must have empty diagnostics")
		}
		return nil
	default:
		return fmt.Errorf("unknown ExpandCase %q", r.Case)
	}
}

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// sanitizeDiagnostics caps diagnostics at the standard limit and appends a
// diagnostics_truncated warning when the cap is exceeded.
func sanitizeDiagnostics(diags []Diagnostic) []Diagnostic {
	if len(diags) == 0 {
		return nil
	}
	if len(diags) <= 511 {
		return diags
	}
	out := make([]Diagnostic, 0, 512)
	out = append(out, diags[:511]...)
	out = append(out, Diagnostic{
		Level:   "warning",
		Code:    "diagnostics_truncated",
		Message: fmt.Sprintf("diagnostics truncated: %d collected, returning first 511", len(diags)),
	})
	return out
}
