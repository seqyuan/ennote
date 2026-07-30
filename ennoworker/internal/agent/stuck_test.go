package agent

import (
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestStuckGuardDetectsAlternatingRepeatedBatch(t *testing.T) {
	guard := &stuckGuard{}
	a := []domain.ToolCall{{Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)}}
	b := []domain.ToolCall{{Name: "read", Arguments: json.RawMessage(`{"path":"b"}`)}}
	assert.False(t, guard.Repeated(a))
	assert.False(t, guard.Repeated(b))
	assert.False(t, guard.Repeated(a))
	assert.False(t, guard.Repeated(b))
	assert.False(t, guard.Repeated(a))
	assert.False(t, guard.Repeated(a))
	assert.True(t, guard.Repeated(a))
}
