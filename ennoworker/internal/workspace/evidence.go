// Acceptance evidence reader: the production implementation of the
// agentflow.AcceptanceEvidence seam.
//
// The seam is the only filesystem effect of acceptance verification, and the
// rules it enforces are the ones a verifier cannot be trusted to remember:
//
//   - every path resolves through the Jail, so a path outside /workspace —
//     including one that only escapes after symlink resolution — is out of
//     scope and never a verdict;
//   - Stat never opens the file, so a non-regular file (directory, FIFO,
//     socket, device) is reported as such instead of blocking the meta-Run.
//     One `mkfifo` in the workspace must not be able to hang evaluation;
//   - a definite "not found inside the jail" is a fact (Missing), not an
//     error, because `exists` must distinguish "definitely absent"
//     (does_not_hold) from "cannot be decided" (unverified).
package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
)

// acceptanceEvidence resolves acceptance evidence paths through the workspace
// jail.
type acceptanceEvidence struct {
	jail *Jail
}

// NewAcceptanceEvidence returns the evidence reader for one workspace jail.
// Every error it reports is either agentflow.ErrEvidenceOutOfScope (the path is
// not inside the workspace) or an I/O failure; the evaluator maps both to
// unverified, never to a pass.
func NewAcceptanceEvidence(jail *Jail) agentflow.AcceptanceEvidence {
	return &acceptanceEvidence{jail: jail}
}

// Stat reports the file's type and size without opening it.
func (e *acceptanceEvidence) Stat(rel string) (agentflow.AcceptanceFile, error) {
	host, exists, err := e.resolve(rel)
	if err != nil {
		return agentflow.AcceptanceFile{}, err
	}
	if !exists {
		return agentflow.AcceptanceFile{Missing: true}, nil
	}
	info, err := os.Stat(host)
	if err != nil {
		if isNotExist(err) {
			return agentflow.AcceptanceFile{Missing: true}, nil
		}
		return agentflow.AcceptanceFile{}, err
	}
	// Regular is false for a directory, FIFO, socket or device. Nothing is
	// opened here: os.Stat on a FIFO does not block.
	return agentflow.AcceptanceFile{Size: info.Size(), Regular: info.Mode().IsRegular()}, nil
}

// Open returns a reader for an existing regular file. A non-regular file is
// refused before it is opened: opening a FIFO for reading blocks until a writer
// appears, and evaluation has a deadline it must not spend that way.
func (e *acceptanceEvidence) Open(rel string) (io.ReadCloser, error) {
	host, exists, err := e.resolve(rel)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("%w: %s", os.ErrNotExist, rel)
	}
	info, err := os.Stat(host)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", errEvidenceNotRegular, rel)
	}
	return os.Open(host)
}

// errEvidenceNotRegular reports a resolved path that is not a regular file. The
// evaluator's Stat already turns that into does_not_hold; Open only sees it on a
// race and fails closed.
var errEvidenceNotRegular = errors.New("acceptance evidence is not a regular file")

// resolve maps a workspace-relative (or /workspace-absolute) path to its host
// path. exists is false when the target does not exist but its nearest existing
// ancestor is inside the jail, which is the "definitely absent" case.
//
// The parent chain is resolved first so escape and missing stay distinguishable:
// a path inside a directory that escapes the jail is out of scope, never
// "missing".
func (e *acceptanceEvidence) resolve(rel string) (host string, exists bool, err error) {
	if e.jail == nil {
		return "", false, fmt.Errorf("%w: no workspace jail is configured", agentflow.ErrEvidenceOutOfScope)
	}
	candidate := strings.TrimSpace(rel)
	if candidate == "" {
		candidate = "."
	}
	original := candidate
	for {
		resolved, resolveErr := e.jail.ResolveWorkspaceExisting(candidate)
		if resolveErr == nil {
			return resolved, candidate == original, nil
		}
		if errors.Is(resolveErr, ErrPathEscape) {
			return "", false, fmt.Errorf("%w: %s", agentflow.ErrEvidenceOutOfScope, rel)
		}
		if !isNotExist(resolveErr) {
			// Permissions, I/O, an unusable mount: cannot be decided.
			return "", false, resolveErr
		}
		// The path itself does not exist. Walk to the nearest existing ancestor
		// and let it decide: an ancestor inside the jail means the file is
		// definitely absent.
		parent := parentWorkspacePath(candidate)
		if parent == candidate {
			// No ancestor left to resolve; the jail root itself was already
			// tried and failed, so this is an unusable workspace.
			return "", false, fmt.Errorf("%w: %s", agentflow.ErrEvidenceOutOfScope, rel)
		}
		candidate = parent
	}
}

// parentWorkspacePath returns the parent spelling of a jail path, keeping the
// virtual /workspace root as the last stop. It returns its input when there is
// no ancestor left to try, which terminates the missing-path walk.
func parentWorkspacePath(value string) string {
	slash := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(slash)))
	if parent == "." || parent == slash {
		return "."
	}
	if strings.HasPrefix(slash, "/") && parent == "/" {
		return slash // stop at the absolute root instead of escaping it
	}
	return parent
}

// isNotExist reports a definite "no such file or directory" from the jail
// resolution or from os.Stat.
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
