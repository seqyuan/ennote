package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FrozenSkillSchemaVersion identifies the digest-pinned task Skill payload
// written into run_agent_flow_nodes.skill_digests_json and
// delegation_items.skills_json. Version 1 (implicit, never tagged) was the bare
// ID array ["skill-id"]; version 2 is
// {"schemaVersion":2,"skills":[{"id":"skill-id","digest":"<content hash>"}]}.
const FrozenSkillSchemaVersion = 2

// FrozenSkillRef pins one task-level additive Skill: the catalog ID plus the
// content digest observed when the Flow task was frozen.
//
// An empty Digest means "unpinned": the reference predates content pinning
// (a Flow Run frozen before this schema, or a model-authored delegate_tasks
// Skill list). Unpinned refs keep their historical behavior — the current
// on-disk Skill body is used — because there is no frozen content identity to
// verify against.
type FrozenSkillRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest,omitempty"`
}

// Pinned reports whether this ref carries a frozen content digest.
func (ref FrozenSkillRef) Pinned() bool { return ref.Digest != "" }

// FrozenSkillRefs is an ordered list of task Skill refs. Order is meaningful
// (it is the order the Flow task declared, which is the preload order) and is
// never sorted, so prompt composition stays byte-stable across recovery.
type FrozenSkillRefs []FrozenSkillRef

// IDs returns the bare Skill ID list in frozen order. It always returns a
// non-nil slice so "no task Skills" round trips as [] rather than null.
func (refs FrozenSkillRefs) IDs() []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	return ids
}

// Digest returns the frozen content digest for one Skill ID.
func (refs FrozenSkillRefs) Digest(id string) (string, bool) {
	for _, ref := range refs {
		if ref.ID == id {
			return ref.Digest, true
		}
	}
	return "", false
}

// NormalizeFrozenSkills trims IDs/digests, rejects blank IDs, and de-duplicates
// by Skill ID keeping the first occurrence. Duplicate handling is deterministic
// because it preserves input order instead of iterating a map.
func NormalizeFrozenSkills(refs FrozenSkillRefs) (FrozenSkillRefs, error) {
	if len(refs) == 0 {
		return FrozenSkillRefs{}, nil
	}
	normalized := make(FrozenSkillRefs, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			return nil, fmt.Errorf("task skill id is required")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, FrozenSkillRef{ID: id, Digest: strings.TrimSpace(ref.Digest)})
	}
	return normalized, nil
}

// frozenSkillEnvelope is the versioned on-disk representation.
type frozenSkillEnvelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Skills        FrozenSkillRefs `json:"skills"`
}

// EncodeFrozenSkills writes the versioned digest-pinned representation. It is
// used for every new write; DecodeFrozenSkills still accepts the legacy bare
// ID array so existing rows keep loading.
func EncodeFrozenSkills(refs FrozenSkillRefs) ([]byte, error) {
	normalized, err := NormalizeFrozenSkills(refs)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(frozenSkillEnvelope{
		SchemaVersion: FrozenSkillSchemaVersion,
		Skills:        normalized,
	})
	if err != nil {
		return nil, fmt.Errorf("encode frozen skills: %w", err)
	}
	return encoded, nil
}

// DecodeFrozenSkills accepts every representation that has ever been written:
//
//   - the versioned object {"schemaVersion":2,"skills":[{"id":...}]};
//   - the legacy bare ID array ["skill-id"];
//   - a mixed array of strings and {"id","digest"} objects (tolerant read of
//     partially migrated rows).
//
// Legacy string entries decode to unpinned refs (empty Digest) so pre-existing
// Flow Runs, retries, and crash recovery keep working; they simply cannot be
// digest-verified. A payload from a newer schema version fails closed rather
// than being guessed at.
func DecodeFrozenSkills(raw []byte) (FrozenSkillRefs, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return FrozenSkillRefs{}, nil
	}
	switch trimmed[0] {
	case '[':
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("decode frozen skills: %w", err)
		}
		refs := make(FrozenSkillRefs, 0, len(entries))
		for _, entry := range entries {
			ref, err := decodeFrozenSkillEntry(entry)
			if err != nil {
				return nil, err
			}
			refs = append(refs, ref)
		}
		return NormalizeFrozenSkills(refs)
	case '{':
		var envelope frozenSkillEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("decode frozen skills: %w", err)
		}
		if envelope.SchemaVersion > FrozenSkillSchemaVersion {
			return nil, fmt.Errorf("frozen skills payload schema version %d is newer than the supported version %d",
				envelope.SchemaVersion, FrozenSkillSchemaVersion)
		}
		return NormalizeFrozenSkills(envelope.Skills)
	default:
		return nil, fmt.Errorf("decode frozen skills: unrecognised payload %.32q", trimmed)
	}
}

func decodeFrozenSkillEntry(entry json.RawMessage) (FrozenSkillRef, error) {
	trimmed := strings.TrimSpace(string(entry))
	if strings.HasPrefix(trimmed, `"`) {
		var id string
		if err := json.Unmarshal(entry, &id); err != nil {
			return FrozenSkillRef{}, fmt.Errorf("decode frozen skill %s: %w", trimmed, err)
		}
		return FrozenSkillRef{ID: id}, nil
	}
	var ref FrozenSkillRef
	if err := json.Unmarshal(entry, &ref); err != nil {
		return FrozenSkillRef{}, fmt.Errorf("decode frozen skill %s: %w", trimmed, err)
	}
	return ref, nil
}

// SkillIDsFromJSON reduces either representation to the bare ID list. It is the
// compatibility path for read models that only ever exposed IDs (delegation
// items, activity views).
func SkillIDsFromJSON(raw []byte) ([]string, error) {
	refs, err := DecodeFrozenSkills(raw)
	if err != nil {
		return nil, err
	}
	return refs.IDs(), nil
}
