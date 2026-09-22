// Package systemprompt owns the composition vocabulary of the Agent system
// prompt: the ordered segments that produce the exact model-visible bytes, and
// the identity recorded for each of them.
//
// Composition is deliberately a pure, order-preserving concatenation. It never
// rewrites, trims, normalizes, or re-separates segment text — every segment owns
// its own headers and blank lines. That is what lets the composition be split
// into auditable sections without changing a single byte of the prompt the model
// receives, so prompt caches and previously frozen digests stay valid.
//
// This package is a leaf: it depends only on domain, so projectcontext, agent,
// and the executor can all speak the same prompt vocabulary without importing
// each other.
package systemprompt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// Segment is one ordered unit of a composed system prompt.
type Segment struct {
	// ID is the stable section identifier readers see, e.g. "project.agents"
	// or "role.definition".
	ID string
	// Kind classifies the contributing layer.
	Kind domain.PromptSectionKind
	// Source names the producer under the platform's
	// (source, version, digest, risk) identity convention, e.g.
	// "role:security-reviewer@3", "skills:catalog", "project:MEMORY.md".
	Source string
	// Text is the exact byte contribution, including its own separators.
	Text string
	// SkillID is set when this segment inlines one Skill body.
	SkillID string
}

// Compose concatenates segments in order and records one PromptSection per
// non-empty segment. Empty segments contribute no bytes and are therefore
// omitted: a reader must be able to trust that every recorded section is really
// part of the prompt.
func Compose(segments []Segment) (string, []domain.PromptSection) {
	total := 0
	for _, segment := range segments {
		total += len(segment.Text)
	}
	var builder []byte
	if total > 0 {
		builder = make([]byte, 0, total)
	}
	sections := make([]domain.PromptSection, 0, len(segments))
	for _, segment := range segments {
		if segment.Text == "" {
			continue
		}
		builder = append(builder, segment.Text...)
		sections = append(sections, domain.PromptSection{
			ID:      segment.ID,
			Kind:    segment.Kind,
			Source:  segment.Source,
			Bytes:   len(segment.Text),
			Digest:  TextDigest(segment.Text),
			SkillID: segment.SkillID,
		})
	}
	return string(builder), sections
}

// SkillSegment builds the inlined preloaded-Skill segment. The wrapper format
// lives here so every writer of a preloaded Skill agrees on it.
func SkillSegment(skillID, promptText, source string) Segment {
	return Segment{
		ID:      "skill.preload." + skillID,
		Kind:    domain.PromptSectionSkillPreload,
		Source:  source,
		Text:    fmt.Sprintf("\n\n<preloaded_skill id=\"%s\">\n%s\n</preloaded_skill>", skillID, promptText),
		SkillID: skillID,
	}
}

// TextDigest is the hex SHA-256 of one segment's exact contribution.
func TextDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// SectionsDigest is the canonical digest of an ordered section list, or "" when
// there are no sections. It covers every identity field — id, kind, source, byte
// count, per-section digest, and skill id — in order, so any change to what
// produced the prompt, or to the order it was composed in, is detectable.
func SectionsDigest(sections []domain.PromptSection) (string, error) {
	if len(sections) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(sections)
	if err != nil {
		return "", fmt.Errorf("encode prompt sections: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
