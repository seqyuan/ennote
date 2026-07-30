package api

import (
	"context"

	"github.com/seqyuan/ennote/ennoworker/internal/runs"
)

type CoordinatorController struct{ Coordinator *runs.Coordinator }

func (c CoordinatorController) Enqueue(ctx context.Context, runID string) error {
	return c.Coordinator.Enqueue(ctx, runID)
}
func (c CoordinatorController) Cancel(ctx context.Context, runID string) error {
	return c.Coordinator.Cancel(ctx, runID)
}
