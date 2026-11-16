package graphbuilder

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullTask is every field of domain.FlowTask set to a non-zero value, so a
// partial upsert that drops a field is visible in one comparison.
func fullTask() domain.FlowTask {
	return domain.FlowTask{
		Hero:        "local/writer",
		Skills:      []string{"global/write-report"},
		Goal:        "Write the report.",
		Depends:     []string{"producer"},
		Type:        domain.FlowTaskHero,
		Command:     "go test -v ./...",
		FanOut:      &domain.FlowFanOut{Min: 2, Max: 4},
		Budget:      &domain.FlowTaskBudget{Tokens: 4000},
		Terminal:    &domain.FlowTerminal{Status: "success", Output: "report"},
		Output:      "report",
		Next:        map[string]string{"passed": "done"},
		Writes:      []string{"out/*.tsv"},
		Acceptance:  []domain.FlowAcceptance{{Check: "tests-passed", Command: "go test -v ./..."}},
		ContextMode: domain.FlowTaskContextSnapshot,
		ContextFrom: []string{"producer"},
		Retry:       &domain.FlowTaskRetry{Max: 2},
		TimeoutMS:   120000,
		Optional:    true,
	}
}

// Every field of FlowTask must be set by fullTask, otherwise the merge tests
// below pin nothing for that field. A new field added to the task therefore
// fails here first, which is the point: it has to be given a merge rule.
func TestFullTaskFixtureCoversEveryTaskField(t *testing.T) {
	value := reflect.ValueOf(fullTask())
	for index := 0; index < value.NumField(); index++ {
		field := value.Type().Field(index)
		assert.False(t, value.Field(index).IsZero(),
			"fullTask must set domain.FlowTask.%s so the upsert merge rule is pinned for it", field.Name)
	}
}

// upsert_task merges a partial Task onto the existing one: a field the proposal
// omits keeps the value the definition already had, so a proposal that only
// changes the goal no longer erases the acceptance clause (or anything else).
func TestUpsertTaskKeepsFieldsTheProposalOmits(t *testing.T) {
	goalOnly := fullTask()
	goalOnly.Goal = "Align reads."
	everything := fullTask()

	tests := []struct {
		name     string
		existing domain.FlowTask
		incoming domain.FlowTask
		want     domain.FlowTask
	}{
		{
			name:     "a proposal that only changes the goal keeps every other field",
			existing: fullTask(),
			incoming: domain.FlowTask{Goal: "Align reads."},
			want:     goalOnly,
		},
		{
			name:     "an empty proposal changes nothing",
			existing: fullTask(),
			incoming: domain.FlowTask{},
			want:     fullTask(),
		},
		{
			name:     "a proposal that states every field replaces every field",
			existing: domain.FlowTask{Goal: "old"},
			incoming: everything,
			want:     everything,
		},
		{
			name:     "a task that does not exist yet is taken as proposed",
			existing: domain.FlowTask{},
			incoming: domain.FlowTask{Hero: "local/writer", Goal: "Write the report."},
			want:     domain.FlowTask{Hero: "local/writer", Goal: "Write the report."},
		},
		{
			// Explicit emptiness of a collection IS expressible: JSON [] decodes
			// to a non-nil empty slice, which is distinguishable from an omitted
			// field (nil). The dependency list is the one place this already
			// happened before the merge rule existed.
			name:     "an explicitly empty list clears that list",
			existing: fullTask(),
			incoming: domain.FlowTask{Skills: []string{}, Writes: []string{}, Depends: []string{}},
			want: func() domain.FlowTask {
				cleared := fullTask()
				cleared.Skills, cleared.Writes, cleared.Depends = []string{}, []string{}, []string{}
				return cleared
			}(),
		},
		{
			name:     "an explicitly empty map clears that map",
			existing: fullTask(),
			incoming: domain.FlowTask{Next: map[string]string{}},
			want: func() domain.FlowTask {
				cleared := fullTask()
				cleared.Next = map[string]string{}
				return cleared
			}(),
		},
		{
			name:     "an explicitly empty acceptance clause clears the clause",
			existing: fullTask(),
			incoming: domain.FlowTask{Acceptance: []domain.FlowAcceptance{}},
			want: func() domain.FlowTask {
				cleared := fullTask()
				cleared.Acceptance = []domain.FlowAcceptance{}
				return cleared
			}(),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			incoming := testCase.incoming
			definition := &domain.FlowDefinition{Tasks: map[string]domain.FlowTask{"align": testCase.existing}}
			applyOperations(definition, []Operation{{Kind: "upsert_task", TaskID: "align", Task: &incoming}})
			assert.Equal(t, testCase.want, definition.Tasks["align"])
		})
	}
}

// The merge rule depends on one JSON fact: an omitted field decodes to the zero
// value while an explicitly empty list decodes to a non-nil empty slice. This
// pins that fact, because the rule is stated in terms of it.
func TestProposalJSONDistinguishesOmittedFromExplicitlyEmpty(t *testing.T) {
	var operation Operation
	require.NoError(t, json.Unmarshal([]byte(`{"kind":"upsert_task","taskId":"align",`+
		`"task":{"goal":"g","skills":[],"writes":[],"depends":[],"acceptance":[],"next":{},"contextFrom":[]}}`),
		&operation))
	require.NotNil(t, operation.Task)
	assert.NotNil(t, operation.Task.Skills, "an explicit [] is a non-nil empty slice")
	assert.Empty(t, operation.Task.Skills)
	assert.NotNil(t, operation.Task.Acceptance)
	assert.NotNil(t, operation.Task.Next, "an explicit {} is a non-nil empty map")
	assert.Nil(t, operation.Task.ContextFrom,
		"context_from is canonicalized on decode, so an explicit empty list there cannot be expressed")
	assert.Empty(t, operation.Task.Hero, "an omitted string is the zero value, which is why a string has no explicit-empty form")
}
