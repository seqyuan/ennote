package fileconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ApplyToolPolicyPatch applies a whole-row patch to a ToolPolicyConfig
// (design 一 Stage 2a). op.Set is the COMPLETE replacement ToolPolicyConfig —
// whole-row replacement, never a field-level merge (D6). The returned source
// records which layer won for audit; it defaults to "project".
func ApplyToolPolicyPatch(op PatchOp) (domain.ToolPolicyConfig, string, error) {
	if op.ID == "" {
		return domain.ToolPolicyConfig{}, "", fmt.Errorf("policy patch id is required")
	}
	if len(op.Set) == 0 {
		return domain.ToolPolicyConfig{}, "", fmt.Errorf("policy patch set is required (whole-row replacement)")
	}
	if !json.Valid(op.Set) {
		return domain.ToolPolicyConfig{}, "", fmt.Errorf("policy patch %q is not valid JSON", op.ID)
	}
	var replacement domain.ToolPolicyConfig
	if err := json.Unmarshal(op.Set, &replacement); err != nil {
		return domain.ToolPolicyConfig{}, "", fmt.Errorf("decode policy patch %q: %w", op.ID, err)
	}
	source := op.Source
	if source == "" {
		source = "project"
	}
	return replacement, source, nil
}

// LoadWorkspaceToolPolicyPatch reads the `toolPolicyPatch` section from a
// trusted workspace's <canonicalRoot>/.ennote/config.json, or returns nil when
// the file or the section is absent. The caller owns the trust gate: this
// function MUST only be called after the workspace has been verified trusted.
func LoadWorkspaceToolPolicyPatch(canonicalRoot string) (*PatchOp, error) {
	path := filepath.Join(canonicalRoot, ".ennote", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspace tool policy patch %s: %w", path, err)
	}
	var doc struct {
		ToolPolicyPatch json.RawMessage `json:"toolPolicyPatch,omitempty"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse workspace tool policy patch %s: %w", path, err)
	}
	if len(doc.ToolPolicyPatch) == 0 {
		return nil, nil
	}
	var op PatchOp
	if err := json.Unmarshal(doc.ToolPolicyPatch, &op); err != nil {
		return nil, fmt.Errorf("decode workspace tool policy patch: %w", err)
	}
	return &op, nil
}
