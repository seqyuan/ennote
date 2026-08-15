// Package hooks implements ennote's config-driven lifecycle hook system.
//
// Phase 2: configuration types, JSON loader, hook set merge, matcher validation,
// and digest computation for run-set freezing. Process execution (runner) and
// lifecycle wiring (dispatcher) are delivered in Phase 3.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

// CommandType is the only hook type supported in v1.
const CommandType = "command"

// DefaultTimeoutSeconds is the per-hook execution timeout when Timeout is nil.
const DefaultTimeoutSeconds = 60

// HookConfig is a single hook command.
type HookConfig struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Command        string `json:"command"`
	TimeoutSeconds *int   `json:"timeoutSeconds,omitempty"`
}

// HookMatcherConfig binds a group of hooks to a matcher.
type HookMatcherConfig struct {
	ID      string       `json:"id"`
	Matcher string       `json:"matcher,omitempty"`
	Hooks   []HookConfig `json:"hooks"`
}

// EventHookSet is the hook configuration for a single event type.
type EventHookSet struct {
	Mode     string              `json:"mode,omitempty"` // "append" (default) | "replace"
	Matchers []HookMatcherConfig `json:"matchers"`
}

// HookSet maps an event type to its matcher list. This is the resolved,
// merged view of all configuration layers for a single run.
type HookSet map[string]EventHookSet

// HookLayer represents a single configuration source (global, env-override,
// or workspace).
type HookLayer struct {
	Source      string            `json:"-"`
	Hooks       map[string]string `json:"-"`
	RawHooks    json.RawMessage   `json:"hooks,omitempty"`
	HookPatches json.RawMessage   `json:"hookPatches,omitempty"` // per-matcher patches, applied after the append/replace merge
}

// LoadGlobalHookLayer reads the hooks section from $ENNOTE_HOME/config.json.
// Returns nil if the file does not exist.
func LoadGlobalHookLayer(homeDir string) (*HookLayer, error) {
	return loadHooksFile(filepath.Join(homeDir, "config.json"), "global")
}

// LoadEnvHookLayer reads the hooks section from the file named by ENNOTE_CONFIG_FILE.
// Returns nil if the env var is unset or the file does not exist.
func LoadEnvHookLayer(homeDir string) (*HookLayer, error) {
	path := os.Getenv("ENNOTE_CONFIG_FILE")
	if path == "" {
		return nil, nil
	}
	return loadHooksFile(path, "env")
}

// LoadWorkspaceHookLayer reads the hooks section from <canonicalRoot>/.ennote/config.json.
// Returns nil if the file does not exist. The caller must have already verified
// that the workspace is trusted before calling this function.
func LoadWorkspaceHookLayer(canonicalRoot string) (*HookLayer, error) {
	return loadHooksFile(filepath.Join(canonicalRoot, ".ennote", "config.json"), "workspace")
}

func loadHooksFile(path, source string) (*HookLayer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("hooks: read %s config %s: %w", source, path, err)
	}
	var layer HookLayer
	if err := json.Unmarshal(data, &layer); err != nil {
		return nil, fmt.Errorf("hooks: parse %s config %s: %w", source, path, err)
	}
	layer.Source = source
	return &layer, nil
}

// ResolveHookSet merges hook layers (global, env, workspace) into a single HookSet.
// Layers are processed in order; later layers with mode:"replace" replace the
// entire event's matcher list, otherwise they append. Workspace layers return an
// error (not loaded) when not passed (caller gates on trust).
func ResolveHookSet(layers ...*HookLayer) (HookSet, error) {
	set := HookSet{}
	for _, layer := range layers {
		if layer == nil {
			continue
		}
		var hooks map[string]EventHookSet
		if len(layer.RawHooks) > 0 {
			if err := json.Unmarshal(layer.RawHooks, &hooks); err != nil {
				return nil, fmt.Errorf("hooks: decode %s layer: %w", layer.Source, err)
			}
		}
		for event, eventSet := range hooks {
			event = strings.TrimSpace(event)
			if event == "" {
				continue
			}
			existing := set[event]
			if eventSet.Mode == "replace" {
				existing = EventHookSet{}
			}
			existing.Mode = "" // resolved set never carries mode; it's consumed at merge time.
			existing.Matchers = append(existing.Matchers, eventSet.Matchers...)
			if err := validateMatchers(event, existing.Matchers); err != nil {
				return nil, fmt.Errorf("hooks: %s layer %s: %w", layer.Source, event, err)
			}
			set[event] = existing
		}
		if len(layer.HookPatches) > 0 {
			var patches []fileconfig.PatchOp
			if err := json.Unmarshal(layer.HookPatches, &patches); err != nil {
				return nil, fmt.Errorf("hooks: decode %s patches: %w", layer.Source, err)
			}
			for i := range patches {
				if patches[i].Source == "" {
					patches[i].Source = layer.Source
				}
			}
			if err := applyHookPatches(set, patches); err != nil {
				return nil, fmt.Errorf("hooks: %s layer: %w", layer.Source, err)
			}
		}
	}
	return set, nil
}

// applyHookPatches applies per-matcher patches (design 六 P1) to the resolved
// set. Each patch replaces one matcher by id across all events (whole-row
// replacement) or deletes it when Set is empty. A patch targeting an unknown
// matcher id fails loud.
func applyHookPatches(set HookSet, patches []fileconfig.PatchOp) error {
	for _, op := range patches {
		if op.ID == "" {
			return fmt.Errorf("patch id is required")
		}
		found := false
		for event, eventSet := range set {
			for i, m := range eventSet.Matchers {
				if m.ID != op.ID {
					continue
				}
				found = true
				if len(op.Set) == 0 {
					eventSet.Matchers = append(eventSet.Matchers[:i], eventSet.Matchers[i+1:]...)
					set[event] = eventSet
					break
				}
				var replacement HookMatcherConfig
				if err := json.Unmarshal(op.Set, &replacement); err != nil {
					return fmt.Errorf("patch %q: decode matcher: %w", op.ID, err)
				}
				if strings.TrimSpace(replacement.ID) == "" {
					replacement.ID = op.ID // patch target keeps its id when omitted
				} else if replacement.ID != op.ID {
					return fmt.Errorf("patch %q must not change matcher id to %q", op.ID, replacement.ID)
				}
				eventSet.Matchers[i] = replacement
				set[event] = eventSet
				break
			}
			if found {
				break
			}
		}
		if !found {
			return fmt.Errorf("patch %q targets unknown matcher id", op.ID)
		}
	}
	return nil
}

func validateMatchers(event string, matchers []HookMatcherConfig) error {
	for _, m := range matchers {
		if strings.TrimSpace(m.ID) == "" {
			return fmt.Errorf("matcher must have a non-empty id")
		}
		if m.Matcher != "" && m.Matcher != "*" {
			// Validate matcher expression.
			if err := validateMatcherExpr(m.Matcher); err != nil {
				return fmt.Errorf("matcher %q: %w", m.ID, err)
			}
		}
		for _, h := range m.Hooks {
			if err := h.Validate(); err != nil {
				return fmt.Errorf("hook %q in matcher %q: %w", h.ID, m.ID, err)
			}
		}
	}
	// Check for duplicate IDs within the same event.
	ids := make(map[string]bool)
	for _, m := range matchers {
		if ids[m.ID] {
			return fmt.Errorf("duplicate matcher id %q", m.ID)
		}
		ids[m.ID] = true
	}
	return nil
}

func validateMatcherExpr(pattern string) error {
	// /.../ syntax is always regex.
	if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) > 2 {
		inner := pattern[1 : len(pattern)-1]
		_, err := regexp.Compile(inner)
		return err
	}
	// | without /.../ is a plain tool-name list — always valid.
	return nil
}

// Validate reports whether the hook is well-formed.
func (h HookConfig) Validate() error {
	if strings.TrimSpace(h.ID) == "" {
		return errors.New("hook id must not be empty")
	}
	if h.Type != "" && h.Type != CommandType {
		return errors.New("hook type must be \"command\"")
	}
	if strings.TrimSpace(h.Command) == "" {
		return errors.New("hook command must not be empty")
	}
	return nil
}

// Timeout returns the effective timeout in seconds.
func (h HookConfig) Timeout() int {
	if h.TimeoutSeconds != nil && *h.TimeoutSeconds > 0 {
		return *h.TimeoutSeconds
	}
	return DefaultTimeoutSeconds
}

// Digest computes a stable digest of a resolved HookSet for freezing into a run snapshot.
func (s HookSet) Digest() ([]byte, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	// Simple hash-based digest; precise algorithm is not critical as long as it's
	// deterministic and detects config changes.
	sum := [4]byte{}
	for i, b := range encoded {
		sum[i%4] ^= b
	}
	return encoded[:min(len(encoded), 64)], nil
}

// IsEmpty reports whether the set has no hooks configured.
func (s HookSet) IsEmpty() bool {
	return len(s) == 0
}

// MatchHooks returns every valid hook under eventType whose matcher matches toolName.
func (s HookSet) MatchHooks(eventType, toolName string, warnLog io.Writer) []HookConfig {
	eventSet, ok := s[eventType]
	if !ok {
		return nil
	}
	var out []HookConfig
	for _, m := range eventSet.Matchers {
		if !matcherApplies(m.Matcher, toolName, warnLog) {
			continue
		}
		for _, h := range m.Hooks {
			if err := h.Validate(); err != nil {
				warnf(warnLog, "hooks: skipping invalid hook %q: %v\n", h.ID, err)
				continue
			}
			out = append(out, h)
		}
	}
	return out
}

func matcherApplies(pattern, toolName string, warnLog io.Writer) bool {
	if toolName == "" {
		return true // non-tool events fire all hooks
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}
	// /.../ syntax is always regex.
	if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) > 2 {
		inner := pattern[1 : len(pattern)-1]
		re, err := regexp.Compile(inner)
		if err != nil {
			warnf(warnLog, "hooks: skipping matcher with invalid regexp %q: %v\n", pattern, err)
			return false
		}
		return re.MatchString(toolName)
	}
	// | without /.../ boundaries: exact tool-name list.
	if strings.ContainsRune(pattern, '|') {
		for _, p := range strings.Split(pattern, "|") {
			if strings.TrimSpace(p) == toolName {
				return true
			}
		}
		return false
	}
	// Plain literal match.
	return pattern == toolName
}

func warnf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format, args...)
}
