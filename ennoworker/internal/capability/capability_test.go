package capability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotDisposeIdempotent(t *testing.T) {
	calls := 0
	s := New("run-1", 0, nil, nil, func() { calls++ })
	require.NotNil(t, s)

	s.Dispose()
	assert.Equal(t, 1, calls)
	assert.NotPanics(t, s.Dispose) // second call is a no-op
	assert.Equal(t, 1, calls)
}

func TestSnapshotDisposeLIFO(t *testing.T) {
	var order []string
	s := New("run-1", 0, nil, nil,
		func() { order = append(order, "first") },
		func() { order = append(order, "second") },
	)
	s.Dispose()
	// Last registered disposed first.
	assert.Equal(t, []string{"second", "first"}, order)
}

func TestSnapshotDisposeIgnoresNilDisposers(t *testing.T) {
	calls := 0
	s := New("run-1", 0, nil, nil, nil, func() { calls++ }, nil)
	s.Dispose()
	assert.Equal(t, 1, calls)
	assert.Nil(t, s.dispose) // consumed
}

func TestNilSnapshotDispose(t *testing.T) {
	var s *CapabilitySnapshot
	assert.NotPanics(t, s.Dispose)
}

func TestSnapshotFields(t *testing.T) {
	s := New("run-9", 3, nil, nil)
	assert.Equal(t, "run-9", s.RunID)
	assert.Equal(t, 3, s.ExecutionDepth)
	assert.Nil(t, s.Tools)
	assert.Nil(t, s.Policy)
	assert.NotPanics(t, s.Dispose) // no disposers registered
}
