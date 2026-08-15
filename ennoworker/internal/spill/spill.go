// Package spill owns the oversized-tool-output storage seam (design 二 P2).
// It persists FULL text and returns a model-facing locator plus a retrieval
// hint; it never truncates or drops data. Save failures reject so the caller
// (spill policy) can degrade to the inline result.
package spill

import "context"

// Owner is the save-time storage namespace. The session id lets a backend group
// artifacts under the producing session; the returned Locator is the
// model-facing handle.
type Owner struct {
	SessionID string
}

// Source describes the tool and call that produced one artifact — for naming
// and inspection only, never access control.
type Source struct {
	ToolName string
	CallID   string
	Label    string
}

// Ref is a saved spill artifact: its locator, byte length, and retrieval hint.
type Ref struct {
	Locator       string
	Bytes         int64
	RetrievalHint string
}

// SaveInput is one request to persist text to a spill artifact.
type SaveInput struct {
	Owner         Owner
	Source        Source
	SuggestedName string // a naming hint, never a path
	Content       string // the full text to persist (UTF-8)
}

// SpillStore is the one-method seam. Implementations must:
//
//   - persist Content verbatim and return an opaque locator + exact byte length
//   - scope storage by Owner.SessionID and pick a private, collision-free name
//   - REJECT on a real storage failure (permissions, ENOSPC, unavailable)
type SpillStore interface {
	SaveText(ctx context.Context, input SaveInput) (Ref, error)
}
