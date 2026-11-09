package store

import (
	"context"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
)

type resolvedFileHero struct {
	Document   *heroesource.Document
	Definition domain.HeroDefinition
	Revision   *globalsource.Revision
}

func (r *RunRepo) resolveFileHero(ctx context.Context, objectID, versionID string) (*resolvedFileHero, error) {
	if r.HeroSources == nil || r.Models == nil {
		return nil, fmt.Errorf("file-backed Hero resolver is unavailable")
	}
	document, revision, err := r.HeroSources.ReadHeroRevision(objectID, versionID)
	if err != nil {
		return nil, fmt.Errorf("read Hero revision: %w", err)
	}
	definition, diagnostics := (&HeroDiscovery{Models: r.Models}).ResolveDocument(ctx, document)
	if definition == nil {
		if len(diagnostics) != 0 {
			return nil, fmt.Errorf("resolve Hero revision: %s", diagnostics[0].Message)
		}
		return nil, fmt.Errorf("resolve Hero revision")
	}
	return &resolvedFileHero{Document: document, Definition: *definition, Revision: revision}, nil
}
