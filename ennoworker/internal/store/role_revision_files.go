package store

import (
	"context"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
)

type resolvedFileRole struct {
	Document   *rolesource.Document
	Definition domain.RoleDefinition
	Revision   *globalsource.Revision
}

func (r *RunRepo) resolveFileRole(ctx context.Context, objectID, versionID string) (*resolvedFileRole, error) {
	if r.RoleSources == nil || r.Models == nil {
		return nil, fmt.Errorf("file-backed Role resolver is unavailable")
	}
	document, revision, err := r.RoleSources.ReadRoleRevision(objectID, versionID)
	if err != nil {
		return nil, fmt.Errorf("read Role revision: %w", err)
	}
	definition, diagnostics := (&RoleDiscovery{Models: r.Models}).ResolveDocument(ctx, document)
	if definition == nil {
		if len(diagnostics) != 0 {
			return nil, fmt.Errorf("resolve Role revision: %s", diagnostics[0].Message)
		}
		return nil, fmt.Errorf("resolve Role revision")
	}
	return &resolvedFileRole{Document: document, Definition: *definition, Revision: revision}, nil
}
