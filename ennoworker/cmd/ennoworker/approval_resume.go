package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// frozenResume is a Run's decoded approval checkpoint: the durable record, the
// loop's continuation state, and the resolution the loop requires to execute the
// approved batch.
type frozenResume struct {
	Record     *domain.ApprovalResume
	State      *agent.ResumeState
	Resolution *agent.ApprovalResolution
}

// decodeFrozenResumeState validates one persisted approval checkpoint into the
// loop's continuation state.
//
// This is the single version matrix. The checkpoint decides which tool batch the
// model is allowed to execute next, so a mismatch has to fail closed rather than
// be coerced: accepting a checkpoint this binary cannot interpret would mean
// executing a batch whose digest was never verified the way this binary verifies
// it. Every resume path must decode through here, or one path could accept what
// another rejects.
func decodeFrozenResumeState(encoded json.RawMessage) (*agent.ResumeState, error) {
	state := &agent.ResumeState{}
	if err := json.Unmarshal(encoded, state); err != nil {
		return nil, fmt.Errorf("decode approval checkpoint: %w", err)
	}
	// Checkpoints written before the digest version existed normalize to V1;
	// the current version must already carry V2.
	switch {
	case state.Version <= 4 && state.Version >= 1:
		if state.ApprovalDigestVersion != 0 && state.ApprovalDigestVersion != agent.ApprovalDigestV1 {
			return nil, fmt.Errorf("legacy checkpoint %d carries unsupported digest version %d",
				state.Version, state.ApprovalDigestVersion)
		}
		state.ApprovalDigestVersion = agent.ApprovalDigestV1
	case state.Version == agent.ResumeStateVersion:
		if state.ApprovalDigestVersion != agent.ApprovalDigestV2 {
			return nil, fmt.Errorf("checkpoint version %d requires digest version %d, got %d",
				state.Version, agent.ApprovalDigestV2, state.ApprovalDigestVersion)
		}
	default:
		return nil, fmt.Errorf("unsupported checkpoint version %d", state.Version)
	}
	return state, nil
}

// loadFrozenResume consumes the Run's resolved approval checkpoint, if it has
// one, and returns nil when the Run is starting fresh.
//
// Claiming the checkpoint is a one-shot transition (pending -> executing), so a
// resume that later fails leaves the checkpoint claimed rather than pending: the
// Run failed, and a retry is a new attempt, exactly like the public Run path.
func (e *agentExecutor) loadFrozenResume(ctx context.Context, runID string) (*frozenResume, error) {
	if e.approvals == nil {
		return nil, nil
	}
	record, err := e.approvals.BeginResume(ctx, runID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	state, err := decodeFrozenResumeState(record.Checkpoint.State)
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid, err)
	}
	return &frozenResume{
		Record: record, State: state,
		Resolution: &agent.ApprovalResolution{
			Decision:    record.Decision,
			BatchDigest: record.Approval.BatchDigest,
		},
	}, nil
}
