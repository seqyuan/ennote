package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

// FlowCheckPolicyResolver resolves the active file-backed tool policy for a
// check task. The legacy session-DB settings/policy_profiles lookup does not
// exist in V2, so production must inject this resolver.
func FlowCheckPolicyResolver(policies *fileconfig.PolicyStore) func(context.Context, *sql.DB, string) (*agentflow.CheckPolicy, error) {
	return func(ctx context.Context, _ *sql.DB, _ string) (*agentflow.CheckPolicy, error) {
		if policies == nil {
			return nil, fmt.Errorf("tool policy store is unavailable")
		}
		snapshot, err := policies.Resolve(ctx, "", domain.PolicyKindTool)
		if err != nil {
			return nil, err
		}
		var config domain.ToolPolicyConfig
		if err := json.Unmarshal(snapshot.Config, &config); err != nil {
			return nil, fmt.Errorf("decode tool policy: %w", err)
		}
		return &agentflow.CheckPolicy{
			Mode:               config.Mode,
			AllowedTools:       append([]string(nil), config.AllowedTools...),
			AllowedExecutables: append([]string(nil), config.AllowedExecutables...),
			MaxTimeoutSeconds:  config.MaxTimeoutSeconds,
		}, nil
	}
}

// CheckAllowlistFromPolicy derives the publish/install check allowlist from the
// same active tool policy the runtime gate uses. allow_existing_behavior already
// permits every executable, so it reports unrestricted=true instead of forcing
// every check pack to be uninstallable under the default policy.
func CheckAllowlistFromPolicy(policies *fileconfig.PolicyStore) (allowlist []string, unrestricted bool, err error) {
	if policies == nil {
		return nil, false, fmt.Errorf("tool policy store is unavailable")
	}
	snapshot, err := policies.Resolve(context.Background(), "", domain.PolicyKindTool)
	if err != nil {
		return nil, false, err
	}
	var config domain.ToolPolicyConfig
	if err := json.Unmarshal(snapshot.Config, &config); err != nil {
		return nil, false, fmt.Errorf("decode tool policy: %w", err)
	}
	if config.Mode == "allow_existing_behavior" {
		return nil, true, nil
	}
	return append([]string(nil), config.AllowedExecutables...), false, nil
}
