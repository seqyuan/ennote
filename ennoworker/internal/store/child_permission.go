package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// flowAnchorParentTx reports whether a Run is a Graph flow anchor. CreateFlowRun
// writes the anchor row and its run_agent_flow row in one transaction, so the
// row's presence is what distinguishes an anchor from an agent Run whose
// snapshot went missing.
func flowAnchorParentTx(ctx context.Context, tx *sql.Tx, runID string) (bool, error) {
	var anchor bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_agent_flow WHERE run_id = ?)`, runID).
		Scan(&anchor); err != nil {
		return false, fmt.Errorf("read parent Run flow anchor: %w", err)
	}
	return anchor, nil
}

// parentToolPolicyMode is the inheritance rule for one parent snapshot, split out
// so the decision table is testable without a database.
//
// A parent with no tool policy is either an anchor (nothing to inherit: the Hero
// ceiling decides) or a broken snapshot (fail closed). deploymentModeAllowExistingBehavior
// is the "never tightens a child" mode resolveChildPermissionCeiling already
// honours, which is exactly what "no parent constraint" means.
func parentToolPolicyMode(isFlowAnchor bool, config json.RawMessage) (domain.PermissionMode, error) {
	trimmed := bytes.TrimSpace(config)
	absent := len(trimmed) == 0 || string(trimmed) == "null"
	if !absent {
		var policy domain.ToolPolicyConfig
		if err := json.Unmarshal(trimmed, &policy); err != nil {
			return "", domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
				fmt.Errorf("decode parent Run tool policy: %w", err))
		}
		if policy.Mode != "" {
			return domain.PermissionMode(policy.Mode), nil
		}
	}
	if isFlowAnchor {
		return deploymentModeAllowExistingBehavior, nil
	}
	return "", domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
		errors.New("parent Run has no frozen tool policy"))
}
