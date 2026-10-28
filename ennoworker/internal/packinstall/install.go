package packinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
)

// Journal kinds. They mirror the store object a written entry created so a
// rollback knows how to re-read and verify it before deleting.
const (
	journalKindSkill        = "skill"
	journalKindGraphRole    = "graph_role"
	journalKindGraph        = "graph"
	journalKindFlow         = "flow"
	journalKindRole         = "role"
	journalKindRoleRevision = "role_revision"
)

// journalSchemaVersion is the staging journal schema.
const journalSchemaVersion = 1

// journalEntry is one object this transaction wrote, with the digest that proves
// it is still the bytes the installer wrote.
type journalEntry struct {
	Kind    string `json:"kind"`
	StoreID string `json:"storeId"`
	Root    string `json:"root,omitempty"`
	Digest  string `json:"digest"`
}

// journal is written to packs/staging/<txid>/journal.json after every object so
// a crash mid-install leaves a record of what to clean up.
type journal struct {
	SchemaVersion int            `json:"schemaVersion"`
	PackID        string         `json:"packId"`
	PackDigest    string         `json:"packDigest"`
	Written       []journalEntry `json:"written"`
}

// Install plans and (unless DryRun) applies one pack install. It returns the plan
// on every outcome after planning succeeded, including refusals, so a caller can
// surface blockers next to the dry-run plan.
//
// Install never waits for a concurrent install: it reports pack_install_busy so
// the API can answer 409 instead of holding an HTTP request open.
func (s *Service) Install(ctx context.Context, request Request) (*Plan, error) {
	if !s.mu.TryLock() {
		return nil, newError(CodeInstallBusy, "another pack install or removal is in progress")
	}
	defer s.mu.Unlock()
	plan, err := s.plan(ctx, request)
	if err != nil {
		return nil, err
	}
	if request.DryRun {
		// A dry run reports blockers in the plan; it never writes and never
		// creates staging.
		return plan, nil
	}
	if len(plan.Blockers) > 0 {
		return plan, blocked(plan)
	}
	if plan.Reinstall == ReinstallNoop {
		return plan, nil
	}
	if err := s.commit(plan, request); err != nil {
		return plan, err
	}
	return plan, nil
}

// blocked folds the blockers into one non-retryable error that keeps the first
// stable code (the API maps codes onto statuses).
func blocked(plan *Plan) error {
	messages := make([]string, 0, len(plan.Blockers))
	for _, blocker := range plan.Blockers {
		messages = append(messages, blocker.Message)
	}
	return newError(plan.Blockers[0].Code, strings.Join(messages, "; "))
}

func (s *Service) commit(plan *Plan, request Request) error {
	if plan.install == nil {
		return fmt.Errorf("install plan has no payload")
	}
	if err := s.gcStagingLocked(); err != nil {
		return err
	}
	staging := filepath.Join(s.StagingDir(), newTransactionID(s.now()))
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return fmt.Errorf("create pack staging directory: %w", err)
	}
	record := &journal{
		SchemaVersion: journalSchemaVersion,
		PackID:        plan.Pack.ID,
		PackDigest:    plan.PackDigest,
		Written:       []journalEntry{},
	}
	if err := writeJournal(staging, record); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := s.apply(plan, request, staging, record); err != nil {
		s.rollback(record)
		_ = os.RemoveAll(staging)
		return err
	}
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("remove pack staging directory: %w", err)
	}
	return nil
}

// apply writes the pack in the documented order. The state file is written last:
// until it exists the install is not observable through List or Remove.
func (s *Service) apply(plan *Plan, request Request, staging string, record *journal) error {
	install := *plan.install
	state := &State{
		StateVersion:      StateVersion,
		Pack:              plan.Pack,
		ManifestDigest:    plan.ManifestDigest,
		PackDigest:        plan.PackDigest,
		Signed:            false,
		InstalledAt:       s.now().UTC().Format(time.RFC3339Nano),
		InstallState:      InstallStateDraft,
		Source:            SourceRef{Kind: "path", Path: strings.TrimSpace(request.SourcePath)},
		Flows:             []FlowState{},
		Roles:             []RoleState{},
		Skills:            []SkillState{},
		ExternalRefs:      install.externalRefs,
		ModelRequirements: install.modelRequirements,
	}
	// 1. Global vendored Skills.
	for _, target := range install.skills {
		if target.scope != skillScopeGlobal {
			continue
		}
		if target.action == ActionExistsIdentical {
			state.Skills = append(state.Skills, SkillState{
				PackID: target.packID, StoreID: target.storeID, Scope: target.scope, ContentDigest: target.digest,
			})
			continue
		}
		digest, err := writeSkill(target)
		if err != nil {
			return err
		}
		if err := s.record(staging, record, journalEntry{
			Kind: journalKindSkill, StoreID: target.storeID, Root: target.root, Digest: digest,
		}); err != nil {
			return err
		}
		state.Skills = append(state.Skills, SkillState{
			PackID: target.packID, StoreID: target.storeID, Scope: target.scope, ContentDigest: digest,
		})
	}
	// 2. Graph-private Skills, one copy below every referencing flow.
	for _, target := range install.skills {
		if target.scope == skillScopeGlobal {
			continue
		}
		if target.action == ActionExistsIdentical {
			state.Skills = append(state.Skills, SkillState{
				PackID: target.packID, StoreID: target.storeID, Scope: target.scope, ContentDigest: target.digest,
			})
			continue
		}
		digest, err := writeSkill(target)
		if err != nil {
			return err
		}
		if err := s.record(staging, record, journalEntry{
			Kind: journalKindSkill, StoreID: target.packID, Root: target.root, Digest: digest,
		}); err != nil {
			return err
		}
		state.Skills = append(state.Skills, SkillState{
			PackID: target.packID, StoreID: target.storeID, Scope: target.scope, ContentDigest: digest,
		})
	}
	// 3. Graph-private Roles.
	for _, target := range install.roles {
		if target.scope != roleScopeFlow {
			continue
		}
		if target.action == ActionExistsIdentical {
			state.Roles = append(state.Roles, RoleState{
				PackHandle: target.packHandle, Scope: target.scope, StoreID: target.storeID, Digest: target.digest,
			})
			continue
		}
		digest, err := s.Sources.CreateGraphRole(target.graphID, target.document)
		if err != nil {
			return fmt.Errorf("install Graph-private Role %s: %w", target.storeID, err)
		}
		if err := s.record(staging, record, journalEntry{
			Kind: journalKindGraphRole, StoreID: target.storeID, Digest: digest,
		}); err != nil {
			return err
		}
		state.Roles = append(state.Roles, RoleState{
			PackHandle: target.packHandle, Scope: target.scope, StoreID: target.storeID, Digest: digest,
		})
	}
	// 4. Source drafts. PublishGraphRevision/PublishFlowRevision are deliberately
	// never called: an installed source has no revision, so graphrun refuses it
	// until a user publishes and binds one explicitly.
	for _, target := range install.flows {
		revision := (*string)(nil)
		if target.action == ActionExistsIdentical {
			state.Flows = append(state.Flows, FlowState{
				PackFlowID: target.packFlowID, StoreID: target.storeID, Format: target.format,
				SourceDigest: target.sourceDigest, InstalledGraphDigest: target.installedDigest,
				PublishedRevision: revision,
			})
			continue
		}
		var digest string
		var err error
		switch target.format {
		case "flow":
			digest, err = s.Sources.CreateFlowFromDefinition(target.definition)
		default:
			digest, err = s.Sources.CreateGraphFromDocument(target.document)
		}
		if err != nil {
			return fmt.Errorf("install %s %s: %w", target.format, target.storeID, err)
		}
		journalKind := journalKindGraph
		if target.format == "flow" {
			journalKind = journalKindFlow
		}
		if err := s.record(staging, record, journalEntry{
			Kind: journalKind, StoreID: target.storeID, Digest: digest,
		}); err != nil {
			return err
		}
		state.Flows = append(state.Flows, FlowState{
			PackFlowID: target.packFlowID, StoreID: target.storeID, Format: target.format,
			SourceDigest: target.sourceDigest, InstalledGraphDigest: digest,
			PublishedRevision: revision,
		})
	}
	// 5. Global Roles.
	for _, target := range install.roles {
		if target.scope != roleScopeGlobal {
			continue
		}
		if target.action == ActionExistsIdentical {
			state.Roles = append(state.Roles, RoleState{
				PackHandle: target.packHandle, Scope: target.scope, StoreID: target.storeID, Digest: target.digest,
			})
			continue
		}
		_, digest, err := s.Sources.CreateRole(target.document)
		if err != nil {
			return fmt.Errorf("install Role %s: %w", target.storeID, err)
		}
		if err := s.record(staging, record, journalEntry{
			Kind: journalKindRole, StoreID: target.storeID, Digest: digest,
		}); err != nil {
			return err
		}
		state.Roles = append(state.Roles, RoleState{
			PackHandle: target.packHandle, Scope: target.scope, StoreID: target.storeID, Digest: digest,
		})
	}
	// 6. Publish the global Role revisions. Publishing is digest-deduplicated, so
	// a reinstall never appends a duplicate revision.
	for index := range state.Roles {
		installed := state.Roles[index]
		if installed.Scope != roleScopeGlobal {
			continue
		}
		revision, err := s.Sources.PublishRoleRevision(installed.StoreID)
		if err != nil {
			return fmt.Errorf("publish Role %s: %w", installed.StoreID, err)
		}
		if err := s.record(staging, record, journalEntry{
			Kind: journalKindRoleRevision, StoreID: installed.StoreID, Digest: revision.Digest,
		}); err != nil {
			return err
		}
		state.Roles[index].Revision = revision.ID()
	}
	// 7. Provenance copy, then the commit point.
	if err := s.writeManifestCopy(plan.Pack.ID, install.manifest); err != nil {
		return err
	}
	if err := s.writeState(state); err != nil {
		// The manifest copy alone is not an install; drop it so a retry starts
		// from a clean state directory.
		if path, pathErr := s.manifestCopyPath(plan.Pack.ID); pathErr == nil {
			_ = os.RemoveAll(filepath.Dir(path))
		}
		return err
	}
	plan.State = state
	return nil
}

// record appends one written object and persists the journal immediately, so a
// crash between two writes still leaves a complete record.
func (s *Service) record(staging string, record *journal, entry journalEntry) error {
	record.Written = append(record.Written, entry)
	return writeJournal(staging, record)
}

func writeJournal(staging string, record *journal) error {
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode pack install journal: %w", err)
	}
	encoded = append(encoded, '\n')
	return atomicWriteFile(filepath.Join(staging, "journal.json"), encoded, 0o600)
}

// rollback deletes, in reverse order, only the objects whose digest still
// matches what this transaction wrote. User data and locally edited objects are
// never deleted by a rollback.
func (s *Service) rollback(record *journal) {
	for index := len(record.Written) - 1; index >= 0; index-- {
		entry := record.Written[index]
		switch entry.Kind {
		case journalKindGraph:
			if _, digest, err := s.Sources.ReadGraph(entry.StoreID); err == nil && digest == entry.Digest {
				_ = s.Sources.DeleteGraph(entry.StoreID)
			}
		case journalKindFlow:
			if _, digest, err := s.Sources.ReadFlow(entry.StoreID); err == nil && digest == entry.Digest {
				_ = s.Sources.DeleteGraph(entry.StoreID)
			}
		case journalKindGraphRole:
			graphID, handle, found := strings.Cut(entry.StoreID, "/")
			if !found {
				continue
			}
			if _, digest, err := s.Sources.ReadGraphRole(graphID, handle); err == nil && digest == entry.Digest {
				_ = s.Sources.DeleteGraphRole(graphID, handle)
			}
		case journalKindRole:
			if _, digest, err := s.Sources.ReadRole(entry.StoreID); err == nil && digest == entry.Digest {
				_ = s.Sources.DeleteRole(entry.StoreID)
			}
		case journalKindSkill:
			if digest, err := skillsmgmt.ContentDigest(entry.Root, entry.StoreID); err == nil && digest == entry.Digest {
				removeSkillDir(entry.Root, entry.StoreID)
			}
		case journalKindRoleRevision:
			// A Role revision is immutable and the revision sequence must stay
			// contiguous; the owning Role directory entry above removes it whole.
		}
	}
	s.removeEmptyGraphDirs(record)
}

// removeEmptyGraphDirs drops the containers an aborted install created for
// Graph-private objects. os.Remove fails on a non-empty directory, so anything a
// user put there survives.
func (s *Service) removeEmptyGraphDirs(record *journal) {
	for index := len(record.Written) - 1; index >= 0; index-- {
		entry := record.Written[index]
		graphID := ""
		switch entry.Kind {
		case journalKindGraph, journalKindFlow:
			graphID = entry.StoreID
		case journalKindGraphRole:
			graphID, _, _ = strings.Cut(entry.StoreID, "/")
		}
		if graphID == "" {
			continue
		}
		base := filepath.Join(s.Sources.GraphsDir(), graphID)
		for _, child := range []string{"roles", "skills"} {
			_ = os.Remove(filepath.Join(base, child))
		}
		_ = os.Remove(base)
	}
}

// writeSkill publishes one Skill and reports its content digest. An already
// identical Skill (planned, but changed between plan and write) is not a
// failure: the plan target's digest is authoritative for the identical bytes.
func writeSkill(target *skillTarget) (string, error) {
	if _, err := skillsmgmt.Write(target.root, target.input); err != nil {
		if errors.Is(err, skillsmgmt.ErrSkillExistsIdentical) {
			return target.digest, nil
		}
		return "", fmt.Errorf("install Skill %s: %w", target.storeID, err)
	}
	return target.digest, nil
}

func removeSkillDir(root, id string) {
	directory := filepath.Join(root, id)
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return
	}
	_ = os.RemoveAll(directory)
}
