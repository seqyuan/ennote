package agent

import (
	"context"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// StandingApprovalMatcher queries active standing rules for a session.
// It is used by the standing gate to replace require_approval decisions
// with allow when a matching rule exists.
type StandingApprovalMatcher interface {
	MatchActive(ctx context.Context, sessionID string, scopes []domain.StandingScopeRef) (map[domain.StandingScopeRef]domain.StandingApproval, error)
}
