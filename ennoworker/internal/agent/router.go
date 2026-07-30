package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
)

type ProviderFactory func(domain.ModelRuntimeSnapshot) (llm.Provider, error)

type SnapshotModelRouter struct {
	Factory ProviderFactory
}

func (r *SnapshotModelRouter) ResolveTurn(_ context.Context, routing domain.FrozenRoutingConfig, plan TurnPlan, constraint RoutingConstraint) (TurnRuntime, error) {
	if r == nil || r.Factory == nil {
		return TurnRuntime{}, fmt.Errorf("model router provider factory is required")
	}
	modelID := plan.ModelProfileID
	if modelID == "" && len(routing.Candidates) > 0 {
		modelID = routing.Candidates[0].ModelProfileID
	}
	var selected domain.ModelRuntimeSnapshot
	for _, candidate := range routing.Candidates {
		if candidate.ModelProfileID == modelID {
			selected = candidate
			break
		}
	}
	if selected.ModelProfileID == "" {
		return TurnRuntime{}, domain.NewCodedError(domain.ErrorModelRouteFailed,
			fmt.Errorf("model %s is not in the frozen candidate set", modelID))
	}
	if constraint.RequiresVision && !selected.SupportsVision {
		for _, candidate := range routing.Candidates {
			if candidate.SupportsVision {
				selected = candidate
				break
			}
		}
		if !selected.SupportsVision {
			return TurnRuntime{}, domain.NewCodedError(domain.ErrorVisionUnsupported,
				fmt.Errorf("no frozen candidate supports vision"))
		}
	}
	provider, err := r.Factory(selected)
	if err != nil {
		return TurnRuntime{}, domain.NewCodedError(domain.ErrorModelRouteFailed, err)
	}
	effective, err := json.Marshal(selected)
	if err != nil {
		return TurnRuntime{}, domain.NewCodedError(domain.ErrorModelRouteFailed, err)
	}
	return TurnRuntime{Provider: provider, Snapshot: selected, RouteReason: plan.Reason,
		EffectiveConfig: effective}, nil
}

type ContextTurnPlanner struct{}

func (ContextTurnPlanner) PrepareNextTurn(_ context.Context, turn TurnContext) (TurnPlan, error) {
	currentID := turn.Current.ModelProfileID
	if turn.Routing.Pinned || !turn.Routing.AllowAutoRoute || turn.Current.ContextTokens <= 0 {
		return TurnPlan{ModelProfileID: currentID, Reason: "fixed_model"}, nil
	}
	threshold := turn.Routing.Threshold
	if threshold <= 0 || threshold >= 1 {
		threshold = 0.7
	}
	if float64(turn.EstimatedTokens)/float64(turn.Current.ContextTokens) < threshold {
		return TurnPlan{ModelProfileID: currentID, Reason: "context_below_threshold"}, nil
	}
	for _, candidate := range turn.Routing.Candidates {
		if candidate.ContextTokens > turn.Current.ContextTokens {
			return TurnPlan{ModelProfileID: candidate.ModelProfileID, Reason: "context_threshold_upgrade"}, nil
		}
	}
	return TurnPlan{ModelProfileID: currentID, Reason: "largest_context_model"}, nil
}

func (ContextTurnPlanner) ShouldStopAfterTurn(_ context.Context, turn TurnContext) (StopDecision, error) {
	if turn.StopAfterToolBatch {
		return StopDecision{Stop: true, Code: "tool_policy_stop_after_batch", Reason: "tool policy requested stop after batch"}, nil
	}
	return StopDecision{}, nil
}
