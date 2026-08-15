package fileconfig

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPatchTableWholeRowReplacement(t *testing.T) {
	table := NewPatchTable()

	// Lower layer sets a full row.
	require.NoError(t, table.Apply(PatchOp{ID: "r1", Source: "global", Set: json.RawMessage(`{"a":1,"b":2}`)}))
	// Higher layer replaces the whole row with a DIFFERENT field set — no merge.
	require.NoError(t, table.Apply(PatchOp{ID: "r1", Source: "project", Set: json.RawMessage(`{"c":3}`)}))

	raw, source, ok := table.Resolve("r1")
	require.True(t, ok)
	assert.Equal(t, "project", source)
	assert.JSONEq(t, `{"c":3}`, string(raw)) // whole row from project, not a merge
}

func TestPatchTableDeleteRemovesLowerLayer(t *testing.T) {
	table := NewPatchTable()
	require.NoError(t, table.Apply(PatchOp{ID: "r1", Source: "global", Set: json.RawMessage(`{"a":1}`)}))
	require.NoError(t, table.Apply(PatchOp{ID: "r1", Source: "project", Set: nil}))

	_, _, ok := table.Resolve("r1")
	assert.False(t, ok)
}

func TestPatchTableListRowsAudit(t *testing.T) {
	table := NewPatchTable()
	require.NoError(t, table.Apply(PatchOp{ID: "b", Source: "global", Set: json.RawMessage(`{}`)}))
	require.NoError(t, table.Apply(PatchOp{ID: "a", Source: "project", Set: json.RawMessage(`{}`)}))

	refs := table.ListRows()
	assert.Equal(t, []RowRef{{ID: "a", Source: "project"}, {ID: "b", Source: "global"}}, refs)
}

func TestPatchTableRejectsInvalid(t *testing.T) {
	table := NewPatchTable()
	assert.Error(t, table.Apply(PatchOp{ID: "", Source: "global", Set: json.RawMessage(`{}`)}))
	assert.Error(t, table.Apply(PatchOp{ID: "r1", Source: "", Set: json.RawMessage(`{}`)}))
	assert.Error(t, table.Apply(PatchOp{ID: "r1", Source: "global", Set: json.RawMessage(`{invalid`)}))
}
