package domain

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTodoStoreSetSnapshotAndClear(t *testing.T) {
	store := NewTodoStore()
	assert.Empty(t, store.Snapshot())

	store.Set([]TodoItem{{Content: "load data", Status: TodoInProgress}})

	snapshot := store.Snapshot()
	require.Len(t, snapshot, 1)
	assert.Equal(t, "load data", snapshot[0].Content)
	snapshot[0].Content = "mutated"
	assert.Equal(t, "load data", store.Snapshot()[0].Content)

	store.Set(nil)
	assert.Empty(t, store.Snapshot())
}

func TestTodoStoreConcurrentAccess(t *testing.T) {
	store := NewTodoStore()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 100; iteration++ {
				store.Set([]TodoItem{{Content: "x", Status: TodoPending}})
				_ = store.Snapshot()
			}
		}()
	}
	wg.Wait()
}

func TestValidTodoStatus(t *testing.T) {
	assert.True(t, ValidTodoStatus(TodoPending))
	assert.True(t, ValidTodoStatus(TodoInProgress))
	assert.True(t, ValidTodoStatus(TodoCompleted))
	assert.False(t, ValidTodoStatus("done"))
	assert.False(t, ValidTodoStatus(""))
}

func TestRenderTodoList(t *testing.T) {
	assert.Equal(t, "Todos: (no tasks)", RenderTodoList(nil))
	assert.Equal(t, "Todos: (no tasks)", RenderTodoList([]TodoItem{}))

	rendered := RenderTodoList([]TodoItem{
		{Content: "a", Status: TodoInProgress},
		{Content: "b", Status: TodoCompleted},
		{Content: "c", Status: TodoPending},
	})
	assert.True(t, strings.HasPrefix(rendered, "Todos:"))
	assert.Contains(t, rendered, "[~] a")
	assert.Contains(t, rendered, "[x] b")
	assert.Contains(t, rendered, "[ ] c")
	assert.Contains(t, rendered, "(1/3 completed)")
}
