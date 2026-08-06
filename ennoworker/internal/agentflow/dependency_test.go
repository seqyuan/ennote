package agentflow

import (
	"context"
	"fmt"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func depDef() *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: "import-me",
		Budget: domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"producer": {Type: domain.FlowTaskRole, Role: "writer@1", Skills: []string{"go-dev", "go-dev"},
				Goal: "produce", Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"reviewer": {Type: domain.FlowTaskRole, Role: "reader@2", Goal: "review",
				Depends: []string{"producer"}, Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"gate": {Type: domain.FlowTaskCheck, Command: "go test ./...", Depends: []string{"reviewer"},
				Next: map[string]string{"pass": "accept", "fail": "revise"}},
			"revise": {Type: domain.FlowTaskRole, Role: "writer@1", Goal: "fix",
				Depends: []string{"gate"}, Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"accept": {Type: domain.FlowTaskRole, Depends: []string{"gate"},
				Terminal: &domain.FlowTerminal{Status: "success"}},
		},
	}
}

// Matrix 3A-1: dependency manifest extracts role@version + skills; check and
// terminal tasks contribute nothing.
func TestDependencyManifest(t *testing.T) {
	refs := DependencyManifest(depDef())
	// reader@2, writer@1 (deduplicated across producer+revise), go-dev
	// (deduplicated within producer's list) = 3 unique refs.
	require.Len(t, refs, 3)
	assert.Equal(t, DependencyRole, refs[0].Kind)
	assert.Equal(t, "reader", refs[0].Name)
	assert.Equal(t, 2, refs[0].Version)
	assert.Equal(t, DependencyRole, refs[1].Kind)
	assert.Equal(t, "writer", refs[1].Name)
	assert.Equal(t, 1, refs[1].Version)
	assert.Equal(t, DependencySkill, refs[2].Kind)
	assert.Equal(t, "go-dev", refs[2].Name)
}

type stubDepResolver struct {
	roles  map[string]bool
	skills map[string]bool
}

func (s *stubDepResolver) ResolveRole(ctx context.Context, roleRef string) (*RoleInfo, error) {
	if s.roles[roleRef] {
		return &RoleInfo{}, nil
	}
	return nil, fmt.Errorf("role %q is not published", roleRef)
}

func (s *stubDepResolver) KnownSkill(ctx context.Context, name string) bool {
	return s.skills[name]
}

// Matrix 3A-2: dependency resolution reports present/missing without
// installing anything.
func TestCheckDependencies(t *testing.T) {
	resolver := &stubDepResolver{roles: map[string]bool{"writer@1": true}, skills: map[string]bool{"go-dev": true}}
	statuses := CheckDependencies(context.Background(), resolver, depDef())
	require.Len(t, statuses, 3)
	byName := map[string]DependencyStatus{}
	for _, s := range statuses {
		byName[string(s.Kind)+":"+s.Name+"@"+fmt.Sprint(s.Version)] = s
	}
	assert.True(t, byName["role:writer@1"].Present)
	assert.True(t, byName["role:reader@2"].Present == false) // reader@2 missing
	assert.Contains(t, byName["role:reader@2"].Reason, "not published")
	assert.True(t, byName["skill:go-dev@0"].Present)
}
