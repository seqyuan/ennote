package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// resolveFileHeroForDelegation resolves a bare global Role handle to its latest
// published file revision (V2). Graph-local and inline Heroes are never
// delegated by handle: they are frozen into the delegation item at Run start.
func (r *DelegationRepo) resolveFileHeroForDelegation(ctx context.Context, sessionID, handle string) (*DelegationHeroSnapshot, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" || r.HeroSources == nil || r.Models == nil {
		return nil, ErrDelegationHeroUnavailable
	}
	var projectID string
	if err := r.DB.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id=?`, sessionID).Scan(&projectID); err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	document, revision, err := r.HeroSources.LatestHeroRevision(handle)
	if err != nil {
		return nil, ErrDelegationHeroUnavailable
	}
	definition, diagnostics := (&HeroDiscovery{Models: r.Models}).ResolveDocument(ctx, document)
	if definition == nil {
		if len(diagnostics) != 0 {
			return nil, fmt.Errorf("resolve Role revision: %s", diagnostics[0].Message)
		}
		return nil, fmt.Errorf("resolve Role revision")
	}
	_ = projectID // file Heroes are global; no project scoping in V2 delegation
	return &DelegationHeroSnapshot{
		HeroID:            handle,
		VersionID:         handle + "@" + revision.ID(),
		Scope:             domain.HeroScopeGlobal,
		Definition:        *definition,
		DelegationEnabled: true,
	}, nil
}
