package fileconfig

import (
	"sync"
	"time"
)

// fileSnapshot caches the last successfully parsed value of a config file. It
// lets a running Worker keep serving the last valid document when an external
// edit leaves the file unparsable. Each store owns one snapshot and mutates it
// under the store's own mutex.
type fileSnapshot[T any] struct {
	mu       sync.Mutex
	value    T
	modified time.Time
	present  bool
}

func (s *fileSnapshot[T]) set(value T, modified time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	s.modified = modified
	s.present = true
}

func (s *fileSnapshot[T]) get() (T, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, s.modified, s.present
}
