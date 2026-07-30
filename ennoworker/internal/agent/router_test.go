package agent

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextTurnPlannerUpgradesWithoutDowngrading(t *testing.T) {
	routing := domain.FrozenRoutingConfig{AllowAutoRoute: true, Threshold: 0.7, Candidates: []domain.ModelRuntimeSnapshot{
		{ModelProfileID: "small", ContextTokens: 100}, {ModelProfileID: "large", ContextTokens: 200},
	}}
	planner := ContextTurnPlanner{}
	plan, err := planner.PrepareNextTurn(context.Background(), TurnContext{Current: routing.Candidates[0], Routing: routing, EstimatedTokens: 75})
	require.NoError(t, err)
	assert.Equal(t, "large", plan.ModelProfileID)
	plan, err = planner.PrepareNextTurn(context.Background(), TurnContext{Current: routing.Candidates[1], Routing: routing, EstimatedTokens: 190})
	require.NoError(t, err)
	assert.Equal(t, "large", plan.ModelProfileID)
}

func TestContextTurnPlannerHonorsPinnedModel(t *testing.T) {
	routing := domain.FrozenRoutingConfig{AllowAutoRoute: true, Pinned: true, Threshold: 0.7,
		Candidates: []domain.ModelRuntimeSnapshot{{ModelProfileID: "small", ContextTokens: 100}, {ModelProfileID: "large", ContextTokens: 200}}}
	plan, err := (ContextTurnPlanner{}).PrepareNextTurn(context.Background(), TurnContext{Current: routing.Candidates[0], Routing: routing, EstimatedTokens: 99})
	require.NoError(t, err)
	assert.Equal(t, "small", plan.ModelProfileID)
}

func TestSnapshotRouterAppliesVisionConstraintToFrozenCandidates(t *testing.T) {
	provider := llm.NewFakeProvider()
	router := &SnapshotModelRouter{Factory: func(domain.ModelRuntimeSnapshot) (llm.Provider, error) { return provider, nil }}
	routing := domain.FrozenRoutingConfig{Candidates: []domain.ModelRuntimeSnapshot{
		{ModelProfileID: "text", APIModel: "text", ContextTokens: 100},
		{ModelProfileID: "vision", APIModel: "vision", ContextTokens: 200, SupportsVision: true},
	}}
	runtime, err := router.ResolveTurn(context.Background(), routing, TurnPlan{ModelProfileID: "text"}, RoutingConstraint{RequiresVision: true})
	require.NoError(t, err)
	assert.Equal(t, "vision", runtime.Snapshot.ModelProfileID)
	_, err = router.ResolveTurn(context.Background(), routing, TurnPlan{ModelProfileID: "outside"}, RoutingConstraint{})
	assert.Equal(t, domain.ErrorModelRouteFailed, domain.ErrorCodeOf(err))
}
