package fileconfig

import (
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchFile watches one config file and calls onChanged after a debounce
// window whenever the file changes. It returns a stop function. A watch that
// cannot be established degrades to a no-op stop: reads already re-read the
// file on every access, so hot reload keeps working without the watcher.
func watchFile(path string, debounce time.Duration, onChanged func()) (stop func()) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return func() {}
	}
	if err := watcher.Add(path); err != nil {
		_ = watcher.Close()
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer watcher.Close()
		var timer *time.Timer
		var timerC <-chan time.Time
		for {
			select {
			case <-done:
				return
			case _, ok := <-watcher.Events:
				if !ok {
					return
				}
				if timer == nil {
					timer = time.NewTimer(debounce)
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(debounce)
				}
				timerC = timer.C
			case <-timerC:
				timerC = nil
				onChanged()
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
				// Watch errors are best-effort; fall back to re-read-on-access.
			}
		}
	}()
	return func() { close(done) }
}
