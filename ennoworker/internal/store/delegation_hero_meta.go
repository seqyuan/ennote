package store

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// DelegationHeroMeta is the frozen hero identity + definition captured at Run
// start for one delegated child. It is stored on the delegation item so child
// Run materialization and effective-config resolution never re-read mutable
// Role files or the removed global agent_profile_versions tables.
type DelegationHeroMeta struct {
	// ObjectID is the stable hero object id: "<heroID>" for global Heroes,
	// "graph:<graphID>/<heroID>" for graph-local Heroes, "inline:<taskID>" for
	// inline model-backed task Heroes.
	ObjectID string `json:"objectId"`
	// VersionID is the opaque frozen version ref stored in delegation_items.
	VersionID string `json:"versionId"`
	Handle    string `json:"handle"`
	// DisplayName is the human label used in speaker snapshots and UI.
	DisplayName string `json:"displayName"`
	// ConfigDigest identifies the exact frozen Role content.
	ConfigDigest string `json:"configDigest"`
	// Definition is the full resolved HeroDefinition.
	Definition domain.HeroDefinition `json:"definition"`
}

// NewDelegationHeroMeta builds the frozen Role meta from a hero version ref
// and its full definition JSON, deriving the object id and content digest
// from the ref. The digest is recomputed over the canonical definition so a
// tampered payload fails loudly. The caller sets Handle/DisplayName from the
// resolved Role identity before persisting the item.
func NewDelegationHeroMeta(versionID string, definitionJSON []byte) (*DelegationHeroMeta, error) {
	if strings.TrimSpace(versionID) == "" {
		return nil, fmt.Errorf("hero version ref is required")
	}
	var definition domain.HeroDefinition
	if err := json.Unmarshal(definitionJSON, &definition); err != nil {
		return nil, fmt.Errorf("decode frozen Role definition: %w", err)
	}
	objectID, _, _ := strings.Cut(versionID, "@")
	digest, err := digestJSON(definition)
	if err != nil {
		return nil, err
	}
	return &DelegationHeroMeta{
		ObjectID: objectID, VersionID: versionID,
		ConfigDigest: digest,
		Definition:   definition,
	}, nil
}

// delegationHeroMetaFromDefinition is the internal alias used by flow child
// materialization; production callers should use NewDelegationHeroMeta.
func delegationHeroMetaFromDefinition(versionID string, definitionJSON []byte) (*DelegationHeroMeta, error) {
	return NewDelegationHeroMeta(versionID, definitionJSON)
}

// validateFrozenDelegationMeta verifies a frozen delegation Role meta is
// structurally complete before it is used to freeze a child effective config.
func validateFrozenDelegationMeta(meta DelegationHeroMeta) error {
	if strings.TrimSpace(meta.ObjectID) == "" || strings.TrimSpace(meta.VersionID) == "" ||
		strings.TrimSpace(meta.Handle) == "" || strings.TrimSpace(meta.ConfigDigest) == "" {
		return fmt.Errorf("frozen delegation Role meta is incomplete")
	}
	if meta.Definition.HeroPrompt == "" || meta.Definition.ModelBinding.ModelProfileID == "" {
		return fmt.Errorf("frozen delegation Role definition is incomplete")
	}
	return nil
}

// frozenHeroVersion parses the numeric version from a "<id>@vNNNNNN" ref;
// graph-local and inline refs (non-revision ids) report 0.
func frozenHeroVersion(versionID string) int {
	_, revision, ok := strings.Cut(versionID, "@")
	if !ok {
		return 0
	}
	var version int
	if _, err := fmt.Sscanf(strings.TrimPrefix(revision, "v"), "%d", &version); err != nil {
		return 0
	}
	return version
}
