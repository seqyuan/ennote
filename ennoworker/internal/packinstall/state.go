package packinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentpack"
)

const (
	// StateVersion is the install state schema this Worker writes and reads.
	StateVersion = 1
	// CommitPointStateFile is the documented commit point of one install.
	CommitPointStateFile = "state_file"
	// InstallStateDraft is the only S2a install state: the Graph draft exists but
	// no Graph revision was published, so no Run can start from it.
	InstallStateDraft = "installed_draft"
	// ReinstallNoop is reported when the requested pack is already installed
	// byte-identically.
	ReinstallNoop = "noop"
)

// PackIdentity is the installed pack identity recorded in state.
type PackIdentity struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SourceRef records where the pack was installed from.
type SourceRef struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// ExternalRefs records declarations S2a never installs: external Skill refs and
// MCP profile refs.
type ExternalRefs struct {
	Skills []string `json:"skills"`
	MCP    []string `json:"mcp"`
}

// FlowState records one installed pack flow.
type FlowState struct {
	PackFlowID string `json:"packFlowId"`
	StoreID    string `json:"storeId"`
	Format     string `json:"format"`
	// SourceDigest is the pack flow document digest; InstalledGraphDigest is the
	// digest of the Graph document actually written (it differs when the flow was
	// renamed, because the document id is the store id).
	SourceDigest         string  `json:"sourceDigest"`
	InstalledGraphDigest string  `json:"installedGraphDigest"`
	PublishedRevision    *string `json:"publishedRevision"`
}

// RoleState records one installed pack role.
type RoleState struct {
	PackHandle string `json:"packHandle"`
	Scope      string `json:"scope"`
	// StoreID is "<handle>" for a global Role and "<flow>/<handle>" for a
	// Graph-private Role.
	StoreID  string `json:"storeId"`
	Digest   string `json:"digest"`
	Revision string `json:"revision,omitempty"`
}

// SkillState records one installed vendored skill.
type SkillState struct {
	PackID string `json:"packId"`
	// StoreID is "<id>" for a global Skill and "<flow>/<id>" for a flow-scoped
	// Skill; ContentDigest is the skillsmgmt content digest of its two files.
	StoreID       string `json:"storeId"`
	Scope         string `json:"scope"`
	ContentDigest string `json:"contentDigest"`
}

// State is the durable install record. It is written last (the commit point) and
// is the only thing List and Remove read; the stores themselves remain the
// installed truth.
type State struct {
	StateVersion      int                     `json:"stateVersion"`
	Pack              PackIdentity            `json:"pack"`
	ManifestDigest    string                  `json:"manifestDigest"`
	PackDigest        string                  `json:"packDigest"`
	Signed            bool                    `json:"signed"`
	InstalledAt       string                  `json:"installedAt"`
	InstallState      string                  `json:"installState"`
	Source            SourceRef               `json:"source"`
	Flows             []FlowState             `json:"flows"`
	Roles             []RoleState             `json:"roles"`
	Skills            []SkillState            `json:"skills"`
	ExternalRefs      ExternalRefs            `json:"externalRefs"`
	ModelRequirements agentpack.ModelEnvelope `json:"modelRequirements"`
}

// StateSummary is the List projection: counts instead of every object.
type StateSummary struct {
	Pack           PackIdentity `json:"pack"`
	ManifestDigest string       `json:"manifestDigest"`
	PackDigest     string       `json:"packDigest"`
	InstalledAt    string       `json:"installedAt"`
	InstallState   string       `json:"installState"`
	Source         SourceRef    `json:"source"`
	FlowCount      int          `json:"flowCount"`
	RoleCount      int          `json:"roleCount"`
	SkillCount     int          `json:"skillCount"`
	ExternalRefs   ExternalRefs `json:"externalRefs"`
}

func (s *State) summary() StateSummary {
	external := ExternalRefs{Skills: []string{}, MCP: []string{}}
	if s.ExternalRefs.Skills != nil {
		external.Skills = s.ExternalRefs.Skills
	}
	if s.ExternalRefs.MCP != nil {
		external.MCP = s.ExternalRefs.MCP
	}
	return StateSummary{
		Pack:           s.Pack,
		ManifestDigest: s.ManifestDigest,
		PackDigest:     s.PackDigest,
		InstalledAt:    s.InstalledAt,
		InstallState:   s.InstallState,
		Source:         s.Source,
		FlowCount:      len(s.Flows),
		RoleCount:      len(s.Roles),
		SkillCount:     len(s.Skills),
		ExternalRefs:   external,
	}
}

// readState loads one install state file. A missing file is os.ErrNotExist.
func (s *Service) readState(packID string) (*State, error) {
	path, err := s.statePath(packID)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("pack state %s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode pack state %s: %w", path, err)
	}
	if state.StateVersion != StateVersion {
		return nil, fmt.Errorf("pack state %s has unsupported stateVersion %d", path, state.StateVersion)
	}
	if state.Pack.ID != packID {
		return nil, fmt.Errorf("pack state %s records pack %q", path, state.Pack.ID)
	}
	return &state, nil
}

func (s *Service) writeState(state *State) error {
	path, err := s.statePath(state.Pack.ID)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode pack state: %w", err)
	}
	encoded = append(encoded, '\n')
	return atomicWriteFile(path, encoded, 0o600)
}

func (s *Service) writeManifestCopy(packID string, manifest []byte) error {
	path, err := s.manifestCopyPath(packID)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, manifest, 0o600)
}

// List reports every installed pack by reading the state files only: the state
// directory is the install index, so listing never parses store objects.
func (s *Service) List(ctx context.Context) ([]StateSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateState(); err != nil {
		return nil, err
	}
	root := s.StateDir()
	publishers, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []StateSummary{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pack state root: %w", err)
	}
	summaries := make([]StateSummary, 0, len(publishers))
	for _, publisher := range publishers {
		if !publisher.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, publisher.Name()))
		if err != nil {
			return nil, fmt.Errorf("read pack state publisher %q: %w", publisher.Name(), err)
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			packID := publisher.Name() + "/" + strings.TrimSuffix(file.Name(), ".json")
			state, err := s.readState(packID)
			if err != nil {
				return nil, err
			}
			summaries = append(summaries, state.summary())
		}
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Pack.ID < summaries[j].Pack.ID })
	return summaries, nil
}
