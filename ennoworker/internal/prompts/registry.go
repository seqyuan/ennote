package prompts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Registry holds the resolved effective templates plus diagnostics.
type Registry struct {
	templates   map[string]Template
	shadowed    []shadowedEntry
	diagnostics []Diagnostic
}

type shadowedEntry struct {
	winner, loser string
	tier          Tier
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{templates: make(map[string]Template)}
}

// Add inserts a template with tier-based conflict resolution. Higher-tier
// wins; same-tier last-write-wins. Losers are recorded as shadowed.
func (r *Registry) Add(tmpl Template) {
	existing, ok := r.templates[tmpl.Name]
	if !ok {
		r.templates[tmpl.Name] = tmpl
		return
	}
	if tmpl.Tier > existing.Tier {
		r.shadowed = append(r.shadowed, shadowedEntry{
			winner: tmpl.Name, loser: existing.Name, tier: existing.Tier,
		})
		msg := fmt.Sprintf("template %q (%s) shadowed by higher-tier %s",
			existing.Name, existing.Tier, tmpl.Tier)
		r.trackDiag("info", "shadowed_template", tmpl.Tier.String(), "", existing.Name, msg)
		r.templates[tmpl.Name] = tmpl
	} else if tmpl.Tier < existing.Tier {
		r.shadowed = append(r.shadowed, shadowedEntry{
			winner: existing.Name, loser: tmpl.Name, tier: tmpl.Tier,
		})
		msg := fmt.Sprintf("template %q (%s) shadowed by higher-tier %s",
			tmpl.Name, tmpl.Tier, existing.Tier)
		r.trackDiag("info", "shadowed_template", tmpl.Tier.String(), "", tmpl.Name, msg)
	} else {
		// Same tier: last-write-wins.
		r.templates[tmpl.Name] = tmpl
	}
}

// trackDiag records a diagnostic without leaking host paths into the
// user-facing message: the Path field carries the internal path and is not
// rendered by the UI; messages never embed absolute paths.
func (r *Registry) trackDiag(level, code, source, path, name, message string) {
	if path != "" {
		path = filepath.Base(path)
	}
	r.diagnostics = append(r.diagnostics, Diagnostic{
		Level: level, Code: code, Source: source, Path: path, Name: name, Message: message,
	})
}

// List returns all effective templates sorted by name, with diagnostics
// capped at the standard limit.
func (r *Registry) List() []TemplateSummary {
	out := make([]TemplateSummary, 0, len(r.templates))
	for _, t := range r.templates {
		out = append(out, TemplateSummary{
			Name:         t.Name,
			Description:  t.Description,
			ArgumentHint: t.ArgumentHint,
			Source:       t.Tier.String(),
			Editable:     t.Tier == TierGlobal,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ExpandParsedInvocation expands a template by name with raw arguments.
func (r *Registry) ExpandParsedInvocation(name, rawArguments string) (ExpandResult, error) {
	tmpl, ok := r.templates[name]
	if !ok {
		return NewNotFoundExpand(name, sanitizeDiagnostics(r.diagnostics))
	}

	args, splitErr := SplitArgs(rawArguments)
	var diags []Diagnostic
	if splitErr != nil {
		// Fall back to single raw argument.
		args = []string{rawArguments}
		diags = append(diags, Diagnostic{
			Level:   "warning",
			Code:    "arguments_fallback",
			Source:  tmpl.Tier.String(),
			Name:    name,
			Message: "cannot split arguments; using raw input as single argument",
		})
	}

	expanded, expandErr := ExpandTemplate(tmpl.Body, args)
	if expandErr != nil {
		return ExpandResult{}, expandErr
	}

	return NewMatchedExpand(name, expanded, diags)
}

// Diagnostics returns all collected diagnostics, capped at the standard limit.
func (r *Registry) Diagnostics() []Diagnostic { return sanitizeDiagnostics(r.diagnostics) }

// ——— Resolve ———

// Resolve builds a Registry from all four tiers: builtin → settings → global → project.
// Hard budget exhaustion or a non-recoverable storage failure returns an error
// (never a partial Registry); per-source directory-entry rollback returns a
// diagnostic only.
func Resolve(ctx ResolveContext, builtins []Template, globalStore *GlobalStore, budget *ResolveBudget) (*Registry, error) {
	if budget == nil {
		budget = NewResolveBudget()
	}
	reg := NewRegistry()

	for _, t := range builtins {
		reg.Add(t)
	}

	// Settings tier.
	if len(ctx.SettingsPaths) > 0 {
		if err := loadSettingsTier(reg, ctx.SettingsPaths, budget); err != nil {
			return nil, err
		}
	}

	// Global tier.
	if globalStore != nil {
		if err := loadGlobalTier(reg, globalStore, budget); err != nil {
			return nil, err
		}
	}

	// Project tier (highest).
	switch {
	case ctx.Trusted && ctx.CanonicalRoot != "":
		if err := loadProjectTier(reg, ctx.CanonicalRoot, budget); err != nil {
			return nil, err
		}
	case !ctx.Trusted && ctx.CanonicalRoot != "":
		reg.trackDiag("info", "project_templates_untrusted", "project", "", "",
			"workspace not trusted; project prompt templates not loaded")
	}

	return reg, nil
}

// loadSettingsTier processes each settings path (a directory or a single
// .md file). Returns ErrPromptResourceLimit on hard budget exhaustion.
func loadSettingsTier(reg *Registry, paths []string, budget *ResolveBudget) error {
	for _, p := range paths {
		if budget.TemplateFilesRemaining <= 0 || budget.TemplateBytesRemaining <= 0 {
			return fmt.Errorf("%w: settings tier", ErrPromptResourceLimit)
		}

		// Try as a directory first (no-follow on final component).
		if fd, err := openDirPathNoFollow(p); err == nil {
			scanDirSource(reg, fd, p, TierSettings, budget)
			closeFD(fd)
			continue
		}

		// Try as a single file (no-follow on final component).
		fd, err := openFilePathNoFollow(p)
		if err != nil {
			reg.trackDiag("warning", "source_skipped", "settings", p, "",
				"path is not an accessible file or directory")
			continue
		}
		if err := readSingleFileSource(reg, fd, p, TierSettings, budget); err != nil {
			closeFD(fd)
			return err
		}
		closeFD(fd)
	}
	return nil
}

// loadGlobalTier loads the global store into the registry. In the project
// resolve context the global store must be fully usable: recovery mode
// (over 2,000 entries) is a 500, and budget exhaustion is a 500.
func loadGlobalTier(reg *Registry, store *GlobalStore, budget *ResolveBudget) error {
	result := store.List()
	if result.RecoveryMode {
		return fmt.Errorf("%w: global prompts over limit", ErrPromptStorageUnavailable)
	}
	if budget.TemplateFilesRemaining <= 0 || budget.TemplateBytesRemaining <= 0 {
		return fmt.Errorf("%w: global tier", ErrPromptResourceLimit)
	}

	// Consume budget for global tier.
	for _, t := range result.Templates {
		if budget.TemplateFilesRemaining <= 0 || budget.TemplateBytesRemaining <= 0 {
			return fmt.Errorf("%w: global tier", ErrPromptResourceLimit)
		}
		full, err := store.Get(t.Name)
		if err != nil {
			continue
		}
		budget.TemplateFilesRemaining--
		budget.TemplateBytesRemaining -= int64(len(full.Body) + 512)
		reg.Add(full)
	}
	for _, d := range result.Diagnostics {
		reg.diagnostics = append(reg.diagnostics, d)
	}
	return nil
}

// loadProjectTier opens <canonicalRoot>/.ennote/prompts with dir-FD anchoring:
// each component is opened O_DIRECTORY|O_NOFOLLOW relative to the previous
// FD, so a symlinked .ennote or prompts cannot escape the workspace root.
func loadProjectTier(reg *Registry, canonicalRoot string, budget *ResolveBudget) error {
	rootFD, err := openDirAt(posixATFDCWD, canonicalRoot)
	if err != nil {
		// canonicalRoot was already canonicalised; failure means it is gone.
		reg.trackDiag("warning", "source_skipped", "project", "", "",
			"cannot open workspace root")
		return nil
	}
	defer closeFD(rootFD)

	ennoteFD, err := openDirAt(rootFD, ".ennote")
	if err != nil {
		// Missing or symlinked .ennote — both are fine (skip).
		return nil
	}
	defer closeFD(ennoteFD)

	promptsFD, err := openDirAt(ennoteFD, "prompts")
	if err != nil {
		// Missing or symlinked prompts — skip.
		return nil
	}
	defer closeFD(promptsFD)

	scanDirSource(reg, promptsFD, filepath.Join(canonicalRoot, ".ennote", "prompts"), TierProject, budget)
	return nil
}

// scanDirSource enumerates a directory FD bounded (2,001 entries max),
// sorts eligible *.md entries, and parses up to 500 of them. Symlinked
// entries and non-regular files are skipped with diagnostics. Hard budget
// exhaustion is recorded on the registry; directory-entry exhaustion rolls
// back this source only (no error).
func scanDirSource(reg *Registry, dirFD int, displayPath string, tier Tier, budget *ResolveBudget) {
	entries, atLimit, err := readDirAt(dirFD, 2000)
	if err != nil {
		reg.trackDiag("warning", "source_skipped", tier.String(), displayPath, "",
			"cannot enumerate directory")
		return
	}
	if atLimit || len(entries) > 2000 {
		reg.trackDiag("warning", "source_skipped", tier.String(), displayPath, "",
			"directory has more than 2,000 entries; skipping source")
		return
	}

	// Deterministic order.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	parseCount := 0
	for _, e := range entries {
		// Consume the per-source directory-entry budget; if exhausted, roll
		// back this source only (does not fail the request).
		if budget.DirectoryEntriesRemaining <= 0 {
			reg.trackDiag("warning", "source_skipped", tier.String(), displayPath, "",
				"directory-entry budget exhausted; source skipped")
			return
		}
		budget.DirectoryEntriesRemaining--

		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		// Reject symlinks via the entry type (lstat semantics).
		if e.Type()&os.ModeSymlink != 0 {
			reg.trackDiag("warning", "template_parse_error", tier.String(), displayPath, e.Name(),
				"symlink entries are not allowed")
			continue
		}

		parseCount++
		if parseCount > 500 {
			reg.trackDiag("warning", "template_scan_limit", tier.String(), displayPath, "",
				"more than 500 markdown files in source; remaining not parsed")
			break
		}

		if budget.TemplateFilesRemaining <= 0 || budget.TemplateBytesRemaining <= 0 {
			reg.trackDiag("warning", "prompt_resource_limit", tier.String(), displayPath, "",
				"request budget exhausted")
			return
		}

		fd, err := openFileAt(dirFD, e.Name())
		if err != nil {
			reg.trackDiag("warning", "template_parse_error", tier.String(), displayPath, e.Name(),
				"cannot open file")
			continue
		}

		ok, err := fstatIsRegular(fd)
		if err != nil || !ok {
			closeFD(fd)
			reg.trackDiag("warning", "template_parse_error", tier.String(), displayPath, e.Name(),
				"not a regular file")
			continue
		}

		data, err := readBounded(fd, maxTemplateBytes)
		closeFD(fd)
		if err != nil {
			reg.trackDiag("warning", "template_size_exceeded", tier.String(), displayPath, e.Name(),
				"file exceeds 64 KiB limit")
			continue
		}

		budget.TemplateFilesRemaining--
		budget.TemplateBytesRemaining -= int64(len(data))

		tmpl, err := ParseTemplate(data, e.Name())
		if err != nil {
			reg.trackDiag("warning", "template_parse_error", tier.String(), displayPath, e.Name(),
				"parse error: "+parseErrorMessage(err))
			continue
		}
		tmpl.Tier = tier
		tmpl.Source = tier.String()
		tmpl.Path = filepath.Join(displayPath, e.Name())
		reg.Add(tmpl)
	}
}

// readSingleFileSource reads a settings path that points at a single file.
func readSingleFileSource(reg *Registry, fd int, path string, tier Tier, budget *ResolveBudget) error {
	ok, err := fstatIsRegular(fd)
	if err != nil || !ok {
		reg.trackDiag("warning", "source_skipped", tier.String(), path, "",
			"not a regular file")
		return nil
	}
	if budget.TemplateFilesRemaining <= 0 || budget.TemplateBytesRemaining <= 0 {
		return fmt.Errorf("%w: settings single file", ErrPromptResourceLimit)
	}

	data, err := readBounded(fd, maxTemplateBytes)
	if err != nil {
		reg.trackDiag("warning", "template_size_exceeded", tier.String(), path, "",
			"file exceeds 64 KiB limit")
		return nil
	}

	budget.TemplateFilesRemaining--
	budget.TemplateBytesRemaining -= int64(len(data))

	tmpl, err := ParseTemplate(data, filepath.Base(path))
	if err != nil {
		reg.trackDiag("warning", "template_parse_error", tier.String(), path, filepath.Base(path),
			"parse error: "+parseErrorMessage(err))
		return nil
	}
	tmpl.Tier = tier
	tmpl.Source = tier.String()
	tmpl.Path = path
	reg.Add(tmpl)
	return nil
}

// parseErrorMessage returns a path-free parse error description.
func parseErrorMessage(err error) string {
	// Errors from ParseTemplate embed only the template's own field issues,
	// not host paths. Trim any wrapped path-looking prefixes defensively.
	msg := err.Error()
	if i := strings.Index(msg, ": "); i >= 0 && strings.Contains(msg[:i], "/") {
		// Drop a leading path segment if present.
		return msg[i+2:]
	}
	return msg
}

// IsNotFoundErr reports whether err is an ENOENT from a no-follow open.
func isNoSuchFile(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR)
}
