package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

type requestOwner struct {
	kind string
	id   string
}

func (s *Server) scopeForRequest(r *http.Request) (*Server, error) {
	owner := ownerFromPath(r.URL.Path)
	if owner == nil {
		return s, nil
	}
	var (
		db        *sql.DB
		sessionID string
		err       error
	)
	if owner.kind == "session" {
		sessionID = owner.id
		db, err = s.SessionStores.OpenSession(r.Context(), owner.id)
	} else {
		db, sessionID, err = s.SessionStores.OpenByResource(r.Context(), owner.kind, owner.id)
	}
	if err != nil {
		if owner.kind == "session" {
			// An invalid or missing Session id is a client error (404), not an
			// internal failure. Owner routing must fail closed for unknown ids.
			return nil, fmt.Errorf("%w: session %s", store.ErrSessionNotFound, owner.id)
		}
		return nil, err
	}
	return s.withSessionDB(db, sessionID), nil
}

func ownerFromPath(path string) *requestOwner {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "v1" {
		return nil
	}
	kinds := map[string]string{
		"sessions": "session", "runs": "run", "delegations": "delegation_group",
		"delegation-handles": "delegation_handle", "delegation-items": "delegation_item",
		"delegation-approvals": "delegation_approval", "approval-requests": "approval",
		"compactions": "compaction", "attention": "attention",
	}
	if kind := kinds[parts[1]]; kind != "" {
		return &requestOwner{kind: kind, id: parts[2]}
	}
	if len(parts) >= 6 && parts[1] == "projects" && parts[3] == "agent-flows" && parts[4] == "runs" {
		return &requestOwner{kind: "flow_run", id: parts[5]}
	}
	return nil
}

func (s *Server) withSessionDB(db *sql.DB, sessionID string) *Server {
	clone := *s
	clone.DB = db
	// Keep both the scoped DB and the manager: session-level writes (rename,
	// archive/restore) must still invalidate the manager's per-project list
	// cache, which lives on SessionStores, not on the scoped database.
	clone.Sessions = &store.SessionRepo{DB: db, Files: s.SessionStores}
	if s.Branches != nil {
		value := *s.Branches
		value.DB = db
		clone.Branches = &value
	}
	if s.Messages != nil {
		value := *s.Messages
		value.DB = db
		clone.Messages = &value
	}
	if s.Compactions != nil {
		value := *s.Compactions
		value.DB = db
		clone.Compactions = &value
	}
	if s.Approvals != nil {
		value := *s.Approvals
		value.DB = db
		clone.Approvals = &value
	}
	if s.StandingApprovals != nil {
		value := *s.StandingApprovals
		value.DB = db
		clone.StandingApprovals = &value
	}
	if s.Delegations != nil {
		value := *s.Delegations
		value.DB = db
		clone.Delegations = &value
	}
	if s.DelegationApprovals != nil {
		value := *s.DelegationApprovals
		value.DB = db
		clone.DelegationApprovals = &value
	}
	if s.Attention != nil {
		value := *s.Attention
		value.DB = db
		clone.Attention = &value
	}
	if s.Runs != nil {
		value := *s.Runs
		value.DB = db
		clone.Runs = &value
	}
	if s.Queue != nil {
		value := *s.Queue
		value.DB = db
		clone.Queue = &value
	}
	if s.Events != nil {
		value := *s.Events
		value.DB = db
		clone.Events = &value
	}
	if s.Artifacts != nil {
		root := s.Artifacts.Root
		if s.SessionStores != nil && sessionID != "" {
			if path, err := s.SessionStores.SessionPath(sessionID); err == nil {
				root = filepath.Join(path, "artifacts")
			}
		}
		clone.Artifacts = s.Artifacts.ForScope(db, root)
	}
	if s.MCP != nil {
		mcp := *s.MCP
		if s.MCP.Runs != nil {
			value := *s.MCP.Runs
			value.DB = db
			mcp.Runs = &value
		}
		clone.MCP = &mcp
	}
	return &clone
}

func (s *Server) sessionDBForResource(ctx context.Context, kind, id string) (*sql.DB, error) {
	if s.SessionStores == nil {
		return s.DB, nil
	}
	db, _, err := s.SessionStores.OpenByResource(ctx, kind, id)
	return db, err
}
