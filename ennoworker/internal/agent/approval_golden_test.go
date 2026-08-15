package agent

import (
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
)

// TestApprovalBatchDigestGolden pins the digest bytes for a fixed input,
// independently computed from the algorithm in approval.go. Any change to the
// plannedToolCall structure feeding the digest, the digest algorithm, or the
// decision field vocabulary must update this golden deliberately.
func TestApprovalBatchDigestGolden(t *testing.T) {
	policy := domain.PolicySnapshot{ID: "golden-policy", Version: 7}
	plans := []plannedToolCall{
		{
			original:  domain.ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls -la"}`)},
			effective: domain.ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls -la"}`)},
			decision:  ToolDecision{Action: ToolAllow, RiskClass: domain.RiskShell, RuleID: ""},
		},
		{
			original:  domain.ToolCall{ID: "c2", Name: "read", Arguments: json.RawMessage(`{"path":"a b.txt"}`)},
			effective: domain.ToolCall{ID: "c2", Name: "read", Arguments: json.RawMessage(`{}`)},
			decision:  ToolDecision{Action: ToolRequireApproval, RiskClass: domain.RiskLocalWrite, RuleID: "rule-9"},
		},
	}

	got := approvalBatchDigest(plans, policy, ApprovalDigestV2)
	assert.Equal(t, "8844045cf93c9afe39732f95c18163fa033507f185d12cd3207169366fd2b36f", got)
}

// TestApprovalBatchDigestV1OmitsRuleID pins the V1 variant, whose decision
// payload excludes RuleID.
func TestApprovalBatchDigestV1OmitsRuleID(t *testing.T) {
	policy := domain.PolicySnapshot{ID: "golden-policy", Version: 7}
	plans := []plannedToolCall{
		{
			original:  domain.ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
			effective: domain.ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
			decision:  ToolDecision{Action: ToolAllow, RiskClass: domain.RiskShell, RuleID: "rule-9"},
		},
	}

	got := approvalBatchDigest(plans, policy, ApprovalDigestV1)
	// RuleID must NOT enter the V1 digest: assert the V2 digest with the same
	// input differs solely because V2 includes RuleID.
	v2 := approvalBatchDigest(plans, policy, ApprovalDigestV2)
	assert.NotEqual(t, v2, got)
	assert.Len(t, got, 64)
}
