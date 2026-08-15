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
