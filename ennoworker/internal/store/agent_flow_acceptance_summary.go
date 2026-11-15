package store

// Run-level acceptance summaries: the projection the Run cards read so an
// "unverified" task is visible without expanding the run.
//
// The counts come from the node rows and are folded in Go. The SQL never parses
// a JSON document: the verdict vocabulary has exactly one decoder
// (domain.AcceptanceResultFromVerdict), and a malformed verdict counts in
// `total` only instead of being guessed into a bucket.
//
// Only nodes that declared a clause are counted, which is what keeps a run
// without acceptance indistinguishable from one that predates the feature.

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// AcceptanceSummary returns the run-level acceptance summary of one run, or nil
// when no node of the run declared an acceptance clause. One query per call: the
// rows it needs are folded in Go, so the SQL never parses a JSON document.
func (r *AgentFlowRunRepo) AcceptanceSummary(ctx context.Context, runID string) (*domain.AcceptanceSummary, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT run_id, acceptance_json, acceptance_verdict
		FROM run_agent_flow_nodes WHERE run_id=? AND acceptance_json NOT IN ('', '[]')`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries, err := aggregateAcceptanceSummaries(rows)
	if err != nil {
		return nil, err
	}
	return summaries[runID], nil
}

// SessionAcceptanceSummaries returns the run-level acceptance summary of every
// run of one Session that declared an acceptance clause, keyed by run id. It is
// one query for all runs: the list endpoint must not go back to the database
// once per row. A run with no declared clause has no entry.
func (r *AgentFlowRunRepo) SessionAcceptanceSummaries(ctx context.Context,
	sessionID string) (map[string]*domain.AcceptanceSummary, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT n.run_id, n.acceptance_json, n.acceptance_verdict
		FROM run_agent_flow_nodes n JOIN run_agent_flow r ON r.run_id = n.run_id
		WHERE r.session_id=? AND n.acceptance_json NOT IN ('', '[]')`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return aggregateAcceptanceSummaries(rows)
}

// aggregateAcceptanceSummaries folds declared node rows into per-run summaries.
// Both callers' WHERE clause already excludes undeclared nodes; declaredAcceptance
// re-checks the column so the Go fold, not the predicate, stays the authority on
// what counts.
func aggregateAcceptanceSummaries(rows *sql.Rows) (map[string]*domain.AcceptanceSummary, error) {
	summaries := map[string]*domain.AcceptanceSummary{}
	for rows.Next() {
		var runID string
		var acceptanceJSON, verdictJSON sql.NullString
		if err := rows.Scan(&runID, &acceptanceJSON, &verdictJSON); err != nil {
			return nil, err
		}
		if !declaredAcceptance(acceptanceJSON.String) {
			continue
		}
		summary := summaries[runID]
		if summary == nil {
			summary = &domain.AcceptanceSummary{}
			summaries[runID] = summary
		}
		summary.AddDeclared(domain.AcceptanceResultFromVerdict(json.RawMessage(verdictJSON.String)))
	}
	return summaries, rows.Err()
}

// declaredAcceptance reports whether a stored acceptance_json column holds a
// clause. Both the empty string (a row written before the column existed) and
// `[]` (a task that declared nothing) mean "not declared".
func declaredAcceptance(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed != "" && trimmed != "[]"
}
