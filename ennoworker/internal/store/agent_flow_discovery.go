package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// AgentFlowCandidate is a parsed, validated candidate from a project file
// (or a managed profile). Candidates are never executed, bound, or enabled
// implicitly — the UI must explicitly bind/publish them.
type AgentFlowCandidate struct {
	Slug            string                 `json:"slug"`
	Name            string                 `json:"name"`
	Description     string                 `json:"description"`
	SourceKind      string                 `json:"sourceKind"`
	SourceLocator   string                 `json:"sourceLocator,omitempty"`
	ConfigDigest    string                 `json:"configDigest"`
	Definition      *domain.FlowDefinition `json:"definition,omitempty"`
	AlreadyBound    bool                   `json:"alreadyBound"`
	BoundVersionID  string                 `json:"boundVersionId,omitempty"`
	BoundVersion    int                    `json:"boundVersion,omitempty"`
	UpdateAvailable bool                   `json:"updateAvailable"`
	ParseError      string                 `json:"parseError,omitempty"`
	Validation      []string               `json:"validation,omitempty"`
	TaskCount       int                    `json:"taskCount"`
	MaxTotalTokens  int64                  `json:"maxTotalTokens"`
}

// AgentFlowDiscovery is the parse-only project file scanner. It never starts
// processes, never connects, and never auto-enables anything.
type AgentFlowDiscovery struct {
	Profiles *AgentFlowProfileRepo
}

// FindFlowProjectDir returns the `.ennote/agent-flows` directory of a project
// root, or "" when absent.
func FindFlowProjectDir(projectRoot string) string {
	if projectRoot == "" {
		return ""
	}
	return filepath.Join(projectRoot, ".ennote", "agent-flows")
}

// DiscoverCandidates scans `<root>/.ennote/agent-flows/*.yaml` and returns one
// candidate per valid file, matched against materialized project_file profiles
// (same slug + config digest). Parse errors surface per-file, never fail the
// whole discovery.
func (d *AgentFlowDiscovery) DiscoverCandidates(ctx context.Context, projectRoot, projectID string) ([]AgentFlowCandidate, error) {
	dir := FindFlowProjectDir(projectRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan agent-flows dir: %w", err)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	candidates := make([]AgentFlowCandidate, 0, len(files))
	for _, path := range files {
		candidate, err := parseFlowCandidateFile(ctx, d.Profiles, projectID, path)
		if err != nil {
			candidates = append(candidates, AgentFlowCandidate{
				SourceKind: domain.FlowSourceProjectFile, SourceLocator: path, ParseError: err.Error(),
			})
			continue
		}
		candidates = append(candidates, *candidate)
	}
	return candidates, nil
}

func parseFlowCandidateFile(ctx context.Context, profiles *AgentFlowProfileRepo, projectID, path string) (*AgentFlowCandidate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	def, err := agentflow.ParseDefinition(data)
	if err != nil {
		return nil, err
	}
	digest, err := agentflow.ConfigDigest(def)
	if err != nil {
		return nil, err
	}
	candidate := &AgentFlowCandidate{
		Slug: def.ID, Name: def.ID, Description: def.Description,
		SourceKind: domain.FlowSourceProjectFile, SourceLocator: path,
		ConfigDigest: digest, Definition: def,
		TaskCount: len(def.Tasks), MaxTotalTokens: def.Budget.MaxTotalTokens,
	}
	if profiles != nil {
		profile, err := profiles.FindProfileBySource(ctx, def.ID, domain.FlowSourceProjectFile, &projectID)
		if err == nil {
			candidate.Name = profile.Name
			version, versionErr := profiles.FindVersionByDigest(ctx, profile.ID, digest)
			if versionErr == nil {
				candidate.AlreadyBound = true
				candidate.BoundVersionID = version.ID
				candidate.BoundVersion = version.Version
			} else if versionErr == sql.ErrNoRows {
				// Profile exists but the file changed: the bound version is
				// still the immutable old version; the new config is an update.
				var boundID string
				var boundNumber int
				if err := profiles.DB.QueryRowContext(ctx, `SELECT id, version FROM agent_flow_versions
					WHERE profile_id=? ORDER BY version DESC LIMIT 1`, profile.ID).Scan(&boundID, &boundNumber); err == nil {
					candidate.BoundVersionID = boundID
					candidate.BoundVersion = boundNumber
				}
				candidate.AlreadyBound = true
				candidate.UpdateAvailable = true
			}
		}
	}
	return candidate, nil
}
