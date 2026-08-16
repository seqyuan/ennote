package store

import (
	"context"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

// ModelRepo resolves Model profiles from the file-native model catalog (V2).
// The legacy global model_profiles SQL table was removed.
type ModelRepo struct {
	Files *fileconfig.ModelStore
}

type CreateModelInput = fileconfig.CreateModelInput

type UpdateModelInput = fileconfig.UpdateModelInput

func (r *ModelRepo) Create(ctx context.Context, input CreateModelInput) (*domain.ModelProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.CreateModel(ctx, input)
}

func (r *ModelRepo) List(ctx context.Context) ([]domain.ModelProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.ListModels(ctx)
}

func (r *ModelRepo) FindByID(ctx context.Context, modelID string) (*domain.ModelProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.FindModel(ctx, modelID)
}

// ResolvePortableRef resolves an exact provider-name/model-name reference.
// Provider names are not yet unique in the managed catalog, so ambiguous
// references fail closed instead of selecting an arbitrary profile.
func (r *ModelRepo) ResolvePortableRef(ctx context.Context, ref string) (*domain.ModelProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	ref = strings.TrimSpace(ref)
	return r.Files.ResolvePortableRef(ctx, ref)
}

func (r *ModelRepo) FirstByProvider(ctx context.Context, providerID string) (*domain.ModelProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.FirstByProvider(ctx, providerID)
}

func (r *ModelRepo) SetDefault(ctx context.Context, modelID string) error {
	if r == nil || r.Files == nil {
		return ErrFileBackedStoreRequired
	}
	return r.Files.SetDefault(ctx, modelID)
}

func (r *ModelRepo) Update(ctx context.Context, modelID string, input UpdateModelInput) (*domain.ModelProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.UpdateModel(ctx, modelID, input)
}

func (r *ModelRepo) Delete(ctx context.Context, modelID string) error {
	if r == nil || r.Files == nil {
		return ErrFileBackedStoreRequired
	}
	return r.Files.DeleteModel(ctx, modelID)
}
