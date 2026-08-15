package store

import (
	"context"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
)

// RoutedRunRepo preserves the Coordinator lifecycle contract while routing
// each operation to the session.db that owns the Run ID.
type RoutedRunRepo struct {
	Sessions *sessionstore.Manager
	Template *RunRepo
}

func (r *RoutedRunRepo) repo(ctx context.Context, runID string) (*RunRepo, error) {
	if r == nil || r.Sessions == nil || r.Template == nil {
		return nil, fmt.Errorf("routed run repository is incomplete")
	}
	db, sessionID, err := r.Sessions.OpenByResource(ctx, "run", runID)
	if err != nil {
		return nil, err
	}
	r.Sessions.RegisterOwner("run", runID, sessionID)
	clone := *r.Template
	clone.DB = db
	return &clone, nil
}

func (r *RoutedRunRepo) Claim(ctx context.Context, runID string) (*domain.AgentRun, error) {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return nil, err
	}
	return repo.Claim(ctx, runID)
}

func (r *RoutedRunRepo) Get(ctx context.Context, runID string) (*domain.AgentRun, error) {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return nil, err
	}
	return repo.Get(ctx, runID)
}

func (r *RoutedRunRepo) Succeed(ctx context.Context, runID string) error {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return err
	}
	return repo.Succeed(ctx, runID)
}

func (r *RoutedRunRepo) FinalizeSuccess(ctx context.Context, runID string, output domain.RunOutput) error {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return err
	}
	return repo.FinalizeSuccess(ctx, runID, output)
}

func (r *RoutedRunRepo) FinalizeChildSuccess(ctx context.Context, runID string, output domain.RunOutput) error {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return err
	}
	return repo.FinalizeChildSuccess(ctx, runID, output)
}

func (r *RoutedRunRepo) FinalizeChildFailure(ctx context.Context, runID, code, message string) (string, bool, error) {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return "", false, err
	}
	return repo.FinalizeChildFailure(ctx, runID, code, message)
}

func (r *RoutedRunRepo) Fail(ctx context.Context, runID, code, message string) error {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return err
	}
	return repo.Fail(ctx, runID, code, message)
}

func (r *RoutedRunRepo) Cancel(ctx context.Context, runID string) error {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return err
	}
	return repo.Cancel(ctx, runID)
}

func (r *RoutedRunRepo) ParentOfRun(ctx context.Context, runID string) (string, error) {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return "", err
	}
	return repo.ParentOfRun(ctx, runID)
}

func (r *RoutedRunRepo) OwnedChildIDs(ctx context.Context, runID string) ([]string, error) {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return nil, err
	}
	return repo.OwnedChildIDs(ctx, runID)
}

func (r *RoutedRunRepo) ReadySuccessorRuns(ctx context.Context, runID string) ([]string, error) {
	repo, err := r.repo(ctx, runID)
	if err != nil {
		return nil, err
	}
	return repo.ReadySuccessorRuns(ctx, runID)
}
