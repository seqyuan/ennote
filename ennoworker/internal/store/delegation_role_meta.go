package store

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// DelegationRoleMeta is the frozen role identity + definition captured at Run
// start for one delegated child. It is stored on the delegation item so child
// Run materialization and effective-config resolution never re-read mutable
// Role files or the removed global agent_profile_versions tables.
type DelegationRoleMeta struct {
	// ObjectID is the stable role object id: "<roleID>" for global Roles,
	// "graph:<graphID>/<roleID>" for graph-local Roles, "inline:<taskID>" for
	// inline model-backed task Roles.
	ObjectID string `json:"objectId"`
	// VersionID is the opaque frozen version ref stored in delegation_items.
	VersionID string `json:"versionId"`
	Handle    string `json:"handle"`
	// DisplayName is the human label used in speaker snapshots and UI.
	DisplayName string `json:"displayName"`
	// ConfigDigest identifies the exact frozen Role content.
	ConfigDigest string `json:"configDigest"`
	// Definition is the full resolved RoleDefinition.
	Definition domain.RoleDefinition `json:"definition"`
}

// NewDelegationRoleMeta builds the frozen Role meta from a role version ref
// and its full definition JSON, deriving the object id and content digest
// from the ref. The digest is recomputed over the canonical definition so a
// tampered payload fails loudly. The caller sets Handle/DisplayName from the
// resolved Role identity before persisting the item.
func NewDelegationRoleMeta(versionID string, definitionJSON []byte) (*DelegationRoleMeta, error) {
	if strings.TrimSpace(versionID) == "" {
		return nil, fmt.Errorf("role version ref is required")
	}
	var definition domain.RoleDefinition
	if err := json.Unmarshal(definitionJSON, &definition); err != nil {
		return nil, fmt.Errorf("decode frozen Role definition: %w", err)
	}
	objectID, _, _ := strings.Cut(versionID, "@")
	digest, err := digestJSON(definition)
	if err != nil {
		return nil, err
	}
	return &DelegationRoleMeta{
		ObjectID: objectID, VersionID: versionID,
		ConfigDigest: digest,
		Definition:   definition,
	}, nil
}

// delegationRoleMetaFromDefinition is the internal alias used by flow child
// materialization; production callers should use NewDelegationRoleMeta.
func delegationRoleMetaFromDefinition(versionID string, definitionJSON []byte) (*DelegationRoleMeta, error) {
	return NewDelegationRoleMeta(versionID, definitionJSON)
}

// validateFrozenDelegationMeta verifies a frozen delegation Role meta is
// structurally complete before it is used to freeze a child effective config.
func validateFrozenDelegationMeta(meta DelegationRoleMeta) error {
	if strings.TrimSpace(meta.ObjectID) == "" || strings.TrimSpace(meta.VersionID) == "" ||
		strings.TrimSpace(meta.Handle) == "" || strings.TrimSpace(meta.ConfigDigest) == "" {
		return fmt.Errorf("frozen delegation Role meta is incomplete")
	}
	if meta.Definition.RolePrompt == "" || meta.Definition.ModelBinding.ModelProfileID == "" {
		return fmt.Errorf("frozen delegation Role definition is incomplete")
	}
	return nil
}

// frozenRoleVersion parses the numeric version from a "<id>@vNNNNNN" ref;
// graph-local and inline refs (non-revision ids) report 0.
func frozenRoleVersion(versionID string) int {
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
