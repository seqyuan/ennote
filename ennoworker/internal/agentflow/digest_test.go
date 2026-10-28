package agentflow

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskOrderAcceptsDeclaredConvergenceBackEdge(t *testing.T) {
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "loop",
		Budget:      domain.FlowBudget{MaxTotalTokens: 1000},
		Convergence: []domain.ConvergenceRule{{From: "review", To: "draft", MaxRounds: 2}},
		Tasks: map[string]domain.FlowTask{
			"draft":  {Type: domain.FlowTaskRole, Role: "writer@1", Goal: "draft"},
			"review": {Type: domain.FlowTaskRole, Role: "reader@1", Goal: "review", Depends: []string{"draft"}},
		},
	}
	// review depends on draft (the forward edge) and draft depends on review
	// (the declared back-edge); removing the back-edge leaves review -> draft.
	def.Tasks["draft"] = domain.FlowTask{Type: domain.FlowTaskRole, Role: "writer@1", Goal: "draft", Depends: []string{"review"}}
	order, err := TaskOrder(def)
	require.NoError(t, err)
	assert.Equal(t, []string{"review", "draft"}, order)
}

func TestTaskOrderRejectsRealCycle(t *testing.T) {
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "cycle",
		Budget: domain.FlowBudget{MaxTotalTokens: 1000},
		Tasks: map[string]domain.FlowTask{
			"a": {Type: domain.FlowTaskRole, Role: "writer@1", Goal: "a", Depends: []string{"b"}},
			"b": {Type: domain.FlowTaskRole, Role: "writer@1", Goal: "b", Depends: []string{"a"}},
		},
	}
	_, err := TaskOrder(def)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}
