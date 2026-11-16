package api

import (
	"context"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
)

// ClassedRunController is the optional half of RunController: a controller that
// can schedule a Run that is not a person's own turn. A controller without it
// keeps working — enqueueChildRuns falls back to Enqueue and simply does not
// separate child work from interactive work.
type ClassedRunController interface {
	EnqueueWithClass(context.Context, string, runs.RunClass) error
}

// enqueueResumedRun schedules the Run an approval decision or a question answer
// resumed. It is not necessarily a person's own turn: delegated and Graph
// children suspend for approval too, and a wave of child approvals must not
// occupy every permit. The class comes from the Run's own facts, like every
// other enqueue site that did not create the Run.
func enqueueResumedRun(ctx context.Context, control RunController, run *domain.AgentRun) error {
	class := runs.ClassifyRun(run.RunKind, run.ParentRunID)
	if classed, ok := control.(ClassedRunController); ok {
		return classed.EnqueueWithClass(ctx, run.ID, class)
	}
	return control.Enqueue(ctx, run.ID)
}

// enqueueChildRun schedules one delegated child Run as batch when the wired
// controller can express a class, and through the plain seam otherwise.
func enqueueChildRun(ctx context.Context, control RunController, runID string) error {
	if classed, ok := control.(ClassedRunController); ok {
		return classed.EnqueueWithClass(ctx, runID, runs.ClassBatch)
	}
	return control.Enqueue(ctx, runID)
}

type CoordinatorController struct{ Coordinator *runs.Coordinator }

// Enqueue schedules a Run a person is waiting on: a submitted turn, an
// invocation, a compaction, a retry, or a Run that an approval decision
// resumed. It is the class the coordinator reserves a slot for.
func (c CoordinatorController) Enqueue(ctx context.Context, runID string) error {
	return c.Coordinator.EnqueueWithClass(ctx, runID, runs.ClassInteractive)
}

// EnqueueWithClass carries the class for the one API path that schedules child
// work rather than a person's turn (Server.enqueueChildRuns). It exists so
// delegated children stay inside the batch reservation instead of competing with
// the next turn for the last slot.
func (c CoordinatorController) EnqueueWithClass(ctx context.Context, runID string, class runs.RunClass) error {
	return c.Coordinator.EnqueueWithClass(ctx, runID, class)
}

func (c CoordinatorController) Cancel(ctx context.Context, runID string) error {
	return c.Coordinator.Cancel(ctx, runID)
}
