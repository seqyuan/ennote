package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// resolveFileRoleForDelegation resolves a bare global Role handle to its latest
// published file revision (V2). Graph-local and inline Roles are never
// delegated by handle: they are frozen into the delegation item at Run start.
func (r *DelegationRepo) resolveFileRoleForDelegation(ctx context.Context, sessionID, handle string) (*DelegationRoleSnapshot, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" || r.RoleSources == nil || r.Models == nil {
		return nil, ErrDelegationRoleUnavailable
	}
	var projectID string
	if err := r.DB.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id=?`, sessionID).Scan(&projectID); err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	document, revision, err := r.RoleSources.LatestRoleRevision(handle)
	if err != nil {
		return nil, ErrDelegationRoleUnavailable
	}
	definition, diagnostics := (&RoleDiscovery{Models: r.Models}).ResolveDocument(ctx, document)
	if definition == nil {
		if len(diagnostics) != 0 {
			return nil, fmt.Errorf("resolve Role revision: %s", diagnostics[0].Message)
		}
		return nil, fmt.Errorf("resolve Role revision")
	}
	_ = projectID // file Roles are global; no project scoping in V2 delegation
	return &DelegationRoleSnapshot{
		RoleID:            handle,
		VersionID:         handle + "@" + revision.ID(),
		Scope:             domain.RoleScopeGlobal,
		Definition:        *definition,
		DelegationEnabled: true,
	}, nil
}
