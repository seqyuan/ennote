package store

import (
	"context"
	"errors"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

// ErrFileBackedStoreRequired is returned when a repo is used without its
// file-native store. The legacy global provider/model/policy SQL tables were
// removed (V2): every store is file-backed now.
var ErrFileBackedStoreRequired = errors.New("file-backed store is required")

// ProviderRepo resolves Provider profiles from the file-native model catalog
// (V2). The legacy global provider_profiles SQL table was removed.
type ProviderRepo struct {
	Files *fileconfig.ModelStore
}

type CreateProviderInput = fileconfig.CreateProviderInput

func (r *ProviderRepo) Create(ctx context.Context, input CreateProviderInput) (*domain.ProviderProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.CreateProvider(ctx, input)
}

func (r *ProviderRepo) FindByID(ctx context.Context, id string) (*domain.ProviderProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.FindProvider(ctx, id)
}

func (r *ProviderRepo) List(ctx context.Context) ([]domain.ProviderProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.ListProviders(ctx)
}

// Delete soft-deletes a provider profile and hides its models.
func (r *ProviderRepo) Delete(ctx context.Context, id string) error {
	if r == nil || r.Files == nil {
		return ErrFileBackedStoreRequired
	}
	return r.Files.DeleteProvider(ctx, id)
}
