// Package fileconfig owns ennote's layered patchable configuration (design 六).
// A patch replaces one row by id with the whole replacement value; there is no
// field-level merge, so the final value of a row is always one layer's complete
// value and is auditable to that layer (D6).
package fileconfig

import (
	"encoding/json"
	"fmt"
	"sort"
)

// PatchOp replaces one row by id (whole-row replacement). Set omitted or null
// deletes the row. Source records the winning layer for audit.
type PatchOp struct {
	ID     string          `json:"id"`
	Set    json.RawMessage `json:"set,omitempty"` // omitted or null = delete
	Source string          `json:"source"`
}

// PatchedRow is one resolved row plus its winning source layer.
type PatchedRow struct {
	Raw    json.RawMessage
	Source string
}

// RowRef is a lightweight id + source pair for listing.
type RowRef struct {
	ID     string
	Source string
}

// PatchTable is a patchable row set: layers apply whole-row replacements by id
// in call order; a later Apply (higher layer) wins. The final value of a row is
// always one layer's whole value, never a field-level merge (D6).
type PatchTable struct {
	rows map[string]PatchedRow
}

// NewPatchTable returns an empty patch table.
func NewPatchTable() *PatchTable {
	return &PatchTable{rows: make(map[string]PatchedRow)}
}

// Apply replaces or deletes the row named by op.ID. A nil/empty op.Set deletes
// the row (a later layer can remove an earlier row). Later Apply calls win.
func (t *PatchTable) Apply(op PatchOp) error {
	if op.ID == "" {
		return fmt.Errorf("patch id is required")
	}
	if op.Source == "" {
		return fmt.Errorf("patch source is required")
	}
	if len(op.Set) == 0 {
		delete(t.rows, op.ID)
		return nil
	}
	if !json.Valid(op.Set) {
		return fmt.Errorf("patch %q from %q is not valid JSON", op.ID, op.Source)
	}
	t.rows[op.ID] = PatchedRow{Raw: append(json.RawMessage(nil), op.Set...), Source: op.Source}
	return nil
}

// Resolve returns the row's final value and winning source, or ok=false when the
// id is absent (deleted or never present).
func (t *PatchTable) Resolve(id string) (json.RawMessage, string, bool) {
	row, ok := t.rows[id]
	if !ok {
		return nil, "", false
	}
	return row.Raw, row.Source, true
}

// ListRows returns id/source pairs in sorted id order.
func (t *PatchTable) ListRows() []RowRef {
	refs := make([]RowRef, 0, len(t.rows))
	for id, row := range t.rows {
		refs = append(refs, RowRef{ID: id, Source: row.Source})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs
}
