package domain

import (
	"fmt"
	"strings"
	"sync"
)

// TodoStatus is the lifecycle state of a single todo item.
type TodoStatus string

const (
	// TodoPending is a task not yet started.
	TodoPending TodoStatus = "pending"
	// TodoInProgress is the task currently being worked on.
	TodoInProgress TodoStatus = "in_progress"
	// TodoCompleted is a finished task.
	TodoCompleted TodoStatus = "completed"

	// MaxTodoItems is the maximum number of todo items accepted in a single call.
	MaxTodoItems = 50
	// MaxTodoContentRunes is the maximum Unicode code points per item content.
	MaxTodoContentRunes = 500
)

// ValidTodoStatus reports whether s is one of the three accepted statuses.
func ValidTodoStatus(status TodoStatus) bool {
	switch status {
	case TodoPending, TodoInProgress, TodoCompleted:
		return true
	default:
		return false
	}
}

// TodoItem is one entry in the task list.
type TodoItem struct {
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

// TodoStore holds the current task list for a run. It is safe for concurrent
// use so the todo tool (which may run in a batch) and reminder providers can
// touch it without racing. A single store is shared per agent run.
type TodoStore struct {
	mu    sync.RWMutex
	items []TodoItem
}

// NewTodoStore returns an empty store.
func NewTodoStore() *TodoStore { return &TodoStore{} }

// Set replaces the whole list with items (a copy, so the caller's slice can be
// reused).
func (s *TodoStore) Set(items []TodoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items[:0:0], items...)
}

// Snapshot returns a copy of the current list, safe to read without holding
// the lock.
func (s *TodoStore) Snapshot() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]TodoItem(nil), s.items...)
}

// RenderTodoList renders items as a checkbox progress block, one line per task,
// with a trailing summary count. An empty list renders as "(no tasks)" so an
// intentional clear is still visible. Marks: [ ] pending, [~] in_progress,
// [x] completed.
func RenderTodoList(items []TodoItem) string {
	if len(items) == 0 {
		return "Todos: (no tasks)"
	}
	var builder strings.Builder
	completed := 0
	builder.WriteString("Todos:")
	for _, item := range items {
		mark := " "
		switch item.Status {
		case TodoInProgress:
			mark = "~"
		case TodoCompleted:
			mark = "x"
			completed++
		}
		fmt.Fprintf(&builder, "\n  [%s] %s", mark, item.Content)
	}
	fmt.Fprintf(&builder, "\n(%d/%d completed)", completed, len(items))
	return builder.String()
}
