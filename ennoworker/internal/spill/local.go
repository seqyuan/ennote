package spill

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Local is the local filesystem spill provider. It writes session-scoped files
// under a private (0700) root: <root>/session-<sha256 前 16 hex>/<random>-<safeName>.
// Writes use O_EXCL so a planted symlink cannot redirect the output.
type Local struct {
	Root string
}

// NewLocal returns a local provider rooted at root (created lazily, 0700).
func NewLocal(root string) *Local {
	return &Local{Root: root}
}

var safeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// sanitizeName maps a caller-suggested name to a single safe path segment. It
// is a naming hint only; anything unsafe falls back to "spill.txt".
func sanitizeName(name string) string {
	base := filepath.Base(name)
	if base == "." || base == ".." || base == "/" || base == "" || !safeNamePattern.MatchString(base) {
		return "spill.txt"
	}
	return base
}

func randomSuffix() (string, error) {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// SaveText implements SpillStore. It never truncates content and rejects on any
// storage failure.
func (l *Local) SaveText(ctx context.Context, input SaveInput) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	if l.Root == "" {
		return Ref{}, fmt.Errorf("spill root is empty")
	}
	if err := os.MkdirAll(l.Root, 0o700); err != nil {
		return Ref{}, fmt.Errorf("create spill root: %w", err)
	}
	sum := sha256.Sum256([]byte(input.Owner.SessionID))
	sessionDir := filepath.Join(l.Root, "session-"+hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return Ref{}, fmt.Errorf("create spill session dir: %w", err)
	}
	name := sanitizeName(input.SuggestedName)
	for attempt := 0; attempt < 8; attempt++ {
		suffix, err := randomSuffix()
		if err != nil {
			return Ref{}, fmt.Errorf("generate spill name: %w", err)
		}
		path := filepath.Join(sessionDir, suffix+"-"+name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return Ref{}, fmt.Errorf("open spill file: %w", err)
		}
		written, writeErr := file.WriteString(input.Content)
		closeErr := file.Close()
		if writeErr != nil {
			_ = os.Remove(path)
			return Ref{}, fmt.Errorf("write spill file: %w", writeErr)
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return Ref{}, fmt.Errorf("close spill file: %w", closeErr)
		}
		return Ref{Locator: path, Bytes: int64(written), RetrievalHint: "read or grep this file"}, nil
	}
	return Ref{}, fmt.Errorf("could not allocate a spill filename")
}

// Reclaim defaults. Spill files hold full tool output so a model can read it
// back; nothing consumes them after the Run that produced them, and the locator
// only lives in that Run's transcript, so an unbounded root grows forever on a
// long-lived install.
const (
	DefaultMaxAge   = 14 * 24 * time.Hour
	DefaultMaxBytes = int64(512 << 20)
)

// ReclaimOptions bounds what Reclaim keeps. A zero MaxAge or MaxBytes disables
// that rule; Reclaim never deletes anything else.
type ReclaimOptions struct {
	MaxAge   time.Duration
	MaxBytes int64
	// Now is the reference time for MaxAge; zero means time.Now().
	Now time.Time
}

// ReclaimResult reports what Reclaim did. Failed counts entries it could not
// inspect or remove: reclamation is best effort and never blocks startup.
type ReclaimResult struct {
	Removed      int
	RemovedBytes int64
	Failed       int
}

// Reclaim removes spill files past the retention age, then the oldest files
// until the root is under the byte cap. Symlinks are left untouched and never
// followed: a spill root is user-writable, so removing what a link points at
// would turn cleanup into an arbitrary delete.
func (l *Local) Reclaim(ctx context.Context, opts ReclaimOptions) (ReclaimResult, error) {
	var result ReclaimResult
	if l.Root == "" {
		return result, nil
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	type entry struct {
		path    string
		modTime time.Time
		size    int64
	}
	var files []entry
	var total int64
	err := filepath.WalkDir(l.Root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			result.Failed++
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			result.Failed++
			return nil
		}
		files = append(files, entry{path: path, modTime: info.ModTime(), size: info.Size()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return result, err
	}

	keep := opts.MaxAge <= 0
	for _, file := range files {
		if !keep && now.Sub(file.modTime) > opts.MaxAge {
			if removeErr := os.Remove(file.path); removeErr != nil && !os.IsNotExist(removeErr) {
				result.Failed++
				continue
			}
			result.Removed++
			result.RemovedBytes += file.size
			total -= file.size
			continue
		}
	}
	if opts.MaxBytes > 0 && total > opts.MaxBytes {
		sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
		for _, file := range files {
			if total <= opts.MaxBytes {
				break
			}
			if !fileStillPresent(file.path) {
				continue
			}
			if removeErr := os.Remove(file.path); removeErr != nil {
				result.Failed++
				continue
			}
			result.Removed++
			result.RemovedBytes += file.size
			total -= file.size
		}
	}
	l.removeEmptySessionDirs()
	return result, nil
}

func fileStillPresent(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// removeEmptySessionDirs drops the per-session directories left behind once all
// their files are gone, so a long-lived install does not accumulate one empty
// directory per session forever.
func (l *Local) removeEmptySessionDirs() {
	entries, err := os.ReadDir(l.Root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "session-") {
			continue
		}
		_ = os.Remove(filepath.Join(l.Root, entry.Name()))
	}
}
