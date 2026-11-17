package agentflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The upstream section used to be cut by bytes, which turned a JSON document into
// a fragment no downstream task could parse. These tests pin what replaced it: the
// payload is always valid JSON, the loss is always announced, and the whole section
// stays inside its byte budget.

func TestProjectSnapshotAlwaysValidJSON(t *testing.T) {
	cases := map[string]string{
		"object":          `{"a":1,"b":[1,2,3],"c":{"d":"e"}}`,
		"array root":      `[1,2,{"a":"b"}]`,
		"null root":       `null`,
		"number root":     `42`,
		"string root":     `"just a string"`,
		"deep chain":      buildDeepPayload(20),
		"wide array":      buildWideArray(5000),
		"huge string":     `{"blob":"` + strings.Repeat("x", 10*1024) + `"}`,
		"not json at all": `this is not json`,
		"empty object":    `{}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			for _, budget := range []int{24, 128, 2048} {
				projected, _, ok := ProjectSnapshot(json.RawMessage(payload), budget)
				if !ok {
					assert.Nil(t, projected, "a refused projection writes no bytes")
					continue
				}
				assert.True(t, json.Valid(projected), "budget %d produced invalid JSON: %s", budget, projected)
				assert.LessOrEqual(t, len(projected), budget, "budget %d overrun: %d bytes", budget, len(projected))
			}
		})
	}
}

func TestProjectSnapshotIsDeterministic(t *testing.T) {
	// Two objects with the same content built in different insertion orders: the
	// projection sorts keys, so the bytes must agree.
	first := json.RawMessage(`{"b":1,"a":{"z":[3,2,1],"y":"text"}}`)
	second := json.RawMessage(`{"a":{"y":"text","z":[3,2,1]},"b":1}`)
	left, _, leftOK := ProjectSnapshot(first, 256)
	right, _, rightOK := ProjectSnapshot(second, 256)
	require.True(t, leftOK)
	require.True(t, rightOK)
	assert.Equal(t, string(left), string(right))

	// And the same input twice is byte-identical.
	again, _, _ := ProjectSnapshot(first, 256)
	assert.Equal(t, string(left), string(again))
}

func TestProjectSnapshotReportsOmissions(t *testing.T) {
	projected, omissions, ok := ProjectSnapshot(json.RawMessage(buildWideArray(100)), 512)
	require.True(t, ok)
	require.True(t, json.Valid(projected))

	var found bool
	for _, omit := range omissions {
		if omit.Kind == "items" {
			found = true
			assert.Equal(t, 100-snapshotMaxItems, omit.Dropped)
		}
	}
	assert.True(t, found, "a truncated array must say how many items it dropped: %v", omissions)

	// A truncated string reports bytes, not items.
	_, stringOmissions, ok := ProjectSnapshot(json.RawMessage(`{"blob":"`+strings.Repeat("y", 4096)+`"}`), 200)
	require.True(t, ok)
	require.NotEmpty(t, stringOmissions)
	assert.Equal(t, "blob", stringOmissions[0].Path)
	assert.Equal(t, "bytes", stringOmissions[0].Kind)
}

func TestProjectSnapshotCollapsesDepth(t *testing.T) {
	projected, omissions, ok := ProjectSnapshot(json.RawMessage(buildDeepPayload(30)), 4096)
	require.True(t, ok)
	assert.True(t, json.Valid(projected))
	var found bool
	for _, omit := range omissions {
		if omit.Kind == "depth" {
			found = true
		}
	}
	assert.True(t, found, "a node past the depth cap must be reported as collapsed")
}

func TestProjectSnapshotRefusesWhenNothingFits(t *testing.T) {
	projected, omissions, ok := ProjectSnapshot(json.RawMessage(`{"a":1}`), snapshotMinShare-1)
	assert.False(t, ok)
	assert.Nil(t, projected)
	require.Len(t, omissions, 1)
	assert.Equal(t, "budget", omissions[0].Kind)
}

func TestProjectSnapshotWrapsNonJSONPayload(t *testing.T) {
	projected, omissions, ok := ProjectSnapshot(json.RawMessage("not json at all"), 256)
	require.True(t, ok)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(projected, &decoded))
	assert.Equal(t, true, decoded["invalidJSON"])
	assert.Equal(t, "not json at all", decoded["text"])
	assert.Empty(t, omissions, "nothing was dropped from a payload that fitted")
}

// renderOmissions must be a hard cap, not a hint: it is the only thing that can
// eat the budget the payload was told it had.
func TestRenderOmissionsRespectsItsBudget(t *testing.T) {
	many := make([]omission, 0, 40)
	for index := 0; index < 40; index++ {
		many = append(many, omission{Path: fmt.Sprintf("field%d", index), Kind: "bytes", Dropped: index})
	}
	for _, budget := range []int{0, 7, 31, 32, 64, 128} {
		rendered := renderOmissions(many, budget)
		assert.LessOrEqual(t, len(rendered), budget, "budget %d: %q", budget, rendered)
		// Below snapshotMarkerMin the reserve cannot say anything useful, so the
		// renderer writes nothing and the caller must treat that as "no room".
		if budget >= snapshotMarkerMin {
			assert.Contains(t, rendered, contextSnapshotProjectionNote)
		} else {
			assert.Empty(t, rendered)
		}
	}
}

// The integration guard: whatever the payloads look like, the section parses and
// stays inside 8 KiB.
func TestSnapshotSectionIsParseable(t *testing.T) {
	task := domain.FlowTask{Depends: []string{"p1", "p2", "p3"}, ContextMode: domain.FlowTaskContextSnapshot}
	outputs := map[string]json.RawMessage{}
	for _, name := range []string{"p1", "p2", "p3"} {
		outputs[name] = bigPayload(t, 3000)
	}
	assignment := renderTaskAssignment("Review", nil, task, outputs)
	sections := upstreamSections(t, assignment)
	require.NotEmpty(t, sections)
	for _, section := range sections {
		assert.True(t, json.Valid([]byte(section)), "the data between the markers must parse: %.80s", section)
	}

	// The whole upstream section, markers and headers included, stays inside the
	// budget. This is the check the old byte-cut could not pass.
	section := assignment[strings.Index(assignment, contextSnapshotHeader):]
	assert.LessOrEqual(t, len(section), contextSnapshotTotalBytes+len(contextSnapshotHeader),
		"the section, as written, is bounded")
}

func buildDeepPayload(depth int) string {
	var builder strings.Builder
	for index := 0; index < depth; index++ {
		builder.WriteString(`{"n":`)
	}
	builder.WriteString(`1`)
	for index := 0; index < depth; index++ {
		builder.WriteString(`}`)
	}
	return builder.String()
}

func buildWideArray(items int) string {
	entries := make([]string, 0, items)
	for index := 0; index < items; index++ {
		entries = append(entries, fmt.Sprintf(`"item-%d"`, index))
	}
	return "[" + strings.Join(entries, ",") + "]"
}
