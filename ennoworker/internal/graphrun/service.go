// Package graphrun starts file-native Graph Runs. At Run start it resolves the
// immutable Graph revision, resolves every Task's Hero/Model/Skill, and
// freezes the complete execution plan (FlowDefinition + per-Task Hero
// definitions) into the owning Session database before any Provider call.
//
// A frozen Graph revision carries its graph-local Heroes and Skills inside the
// revision directory, so execution re-reads no mutable Hero/Graph file and no
// mutable graph-local Skill file for it. Global Skills are the exception: they
// still resolve through the live catalog at Run start and are pinned by digest
// at freeze. A legacy revision published before local resources were frozen
// still resolves local drafts at Run start; such a Run is identifiable by its
// FlowVersionID (graphID@vNNNNNN), which pins the exact revision an auditor can
// open to see whether it recorded frozen content. The removed global
// agent_profile_versions / agent_flow_versions tables are never consulted.
package graphrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// Service starts file-native Graph Runs against immutable file revisions.
type Service struct {
	Sources *globalsource.Store
	Models  *store.ModelRepo
	// Sessions opens the owning Session database.
	Sessions *sessionstore.Manager
	// GlobalSkills maps global Skill id -> its current content digest (the same
	// sha256 directory hash skills.Load computes as LoadedSkill.ContentHash).
	// It is the static fallback used when SkillCatalog is nil (tests, fixtures).
	// nil-safe: an unresolved global Skill, or one whose digest is unknown,
	// fails the Run freeze loudly instead of silently freezing an unpinned
	// reference.
	GlobalSkills map[string]string
	// SkillCatalog, when non-nil, resolves the Skill digest catalog at freeze
	// time instead of using the startup map in GlobalSkills. Production wires
	// skills.DigestCatalog so a Skill created or edited on disk is usable by the
	// next started Graph without restarting the Worker.
	SkillCatalog func() map[string]string
	// OnRunStarted wires the per-Session orchestrator for a freshly frozen
	// Graph run (enqueue child runs, events, recovery).
	OnRunStarted func(ctx context.Context, db *sql.DB, sessionPath, runID string) error
	// Projects supplies the Project GraphBinding a full Graph Run requires.
	// Graph-source Runs keep the historical behavior and do not consult it.
	Projects *projectstore.Store
}

// FlowVersionID returns the portable immutable version ref for a Graph
// revision: "<graphID>@vNNNNNN".
func FlowVersionID(graphID, revisionID string) string {
	return graphID + "@" + revisionID
}

// Start freezes and starts one Graph Run from the Project's pinned binding.
// version 0 selects the pinned revision; a positive version must match the pin.
// The published definition freezes as itself. Runs already frozen into a session
// database do not read the revision again.
func (s *Service) Start(ctx context.Context, projectID, graphID string, version int, sessionID string,
	inputs, vars map[string]any) (*domain.RunAgentFlow, error) {
	if s.Sources == nil || s.Models == nil || s.Sessions == nil {
		return nil, fmt.Errorf("Graph runner is not configured")
	}
	session, err := s.Sessions.FindByID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.ProjectID != projectID {
		return nil, fmt.Errorf("Session %s does not belong to Project %s", sessionID, projectID)
	}
	revisionID, err := s.bindingRevision(ctx, projectID, graphID, version)
	if err != nil {
		return nil, err
	}
	definition, heroes, revision, err := s.Sources.ReadGraphDefinitionRevision(graphID, revisionID)
	if err != nil {
		return nil, fmt.Errorf("graph_legacy_revision: %w", err)
	}
	bundle, err := s.Sources.ReadGraphRevisionBundle(graphID, revisionID)
	if err != nil {
		return nil, err
	}
	return s.freezeAndStart(ctx, projectID, sessionID, graphID, revision, definition, heroes, bundle, inputs, vars)
}

// bindingRevision resolves the revision a Project enabled. A missing, disabled,
// or mismatched binding fails before any Run row is written.
func (s *Service) bindingRevision(ctx context.Context, projectID, graphID string, version int) (string, error) {
	if s.Projects == nil {
		return "", fmt.Errorf("graph_binding_required: Project %s has no enabled binding for Graph %s", projectID, graphID)
	}
	binding, err := s.Projects.FindGraphBinding(ctx, projectID, projectstore.SourceKindGraph, graphID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("graph_binding_required: Project %s has no enabled binding for graph %s", projectID, graphID)
		}
		return "", err
	}
	if !binding.Enabled {
		return "", fmt.Errorf("graph_binding_disabled: Project %s binding for graph %s is disabled", projectID, graphID)
	}
	if binding.Revision == "" {
		return "", fmt.Errorf("graph_binding_revision_missing: graph %s has no pinned revision", graphID)
	}
	if version > 0 && binding.Revision != fmt.Sprintf("v%06d", version) {
		return "", fmt.Errorf("graph_binding_revision_mismatch: Project %s pins graph %s at %s, not v%06d", projectID, graphID, binding.Revision, version)
	}
	return binding.Revision, nil
}

// freezeAndStart is the one freeze shared by a definition-shaped Graph revision
// and a Graph revision. It writes the definition and the per-task snapshots into
// the owning Session database before any Provider call.
func (s *Service) freezeAndStart(ctx context.Context, projectID, sessionID, sourceID string, revision *globalsource.Revision,
	definition *domain.FlowDefinition, heroes map[string]*heroesource.Document, bundle *globalsource.GraphRevisionBundle,
	inputs, vars map[string]any) (*domain.RunAgentFlow, error) {
	inputsJSON, err := store.NormalizeFlowInputs(definition, inputs, vars)
	if err != nil {
		return nil, err
	}
	freeze, err := s.freezeDefinition(ctx, sourceID, definition, heroes, bundle)
	if err != nil {
		return nil, err
	}
	definitionJSON, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	db, err := s.Sessions.OpenSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	run, err := (&store.AgentFlowRunRepo{DB: db}).CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: sessionID, ProjectID: projectID,
		FlowVersionID:  FlowVersionID(sourceID, revision.ID()),
		DefinitionJSON: definitionJSON, ConfigDigest: revision.Digest,
		InputsJSON: inputsJSON,
	}, freeze)
	if err != nil {
		return nil, err
	}
	if s.OnRunStarted != nil {
		sessionPath, pathErr := s.Sessions.SessionPath(sessionID)
		if pathErr != nil {
			return nil, pathErr
		}
		if err := s.OnRunStarted(ctx, db, sessionPath, run.RunID); err != nil {
			return nil, err
		}
	}
	return run, nil
}

// RecoverSession resumes every non-terminal Graph Run of a Session after a
// Worker restart. It is idempotent; the orchestrator reconciles crashed nodes.
func (s *Service) RecoverSession(ctx context.Context, sessionID string) error {
	if s.Sessions == nil || s.OnRunStarted == nil {
		return nil
	}
	db, err := s.Sessions.OpenSession(ctx, sessionID)
	if err != nil {
		return err
	}
	ids, err := (&store.AgentFlowRunRepo{DB: db}).ListRecoverableRuns(ctx)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	sessionPath, err := s.Sessions.SessionPath(sessionID)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		if err := s.OnRunStarted(ctx, db, sessionPath, runID); err != nil {
			return err
		}
	}
	return nil
}

// frozenTaskHero is the per-Task frozen Hero identity + definition captured at
// Run start.
type frozenTaskHero struct {
	ObjectID     string
	VersionID    string
	Handle       string
	DisplayName  string
	ConfigDigest string
	Definition   domain.HeroDefinition
}

// skillDigests returns the Skill digest catalog to use for one freeze: the
// live resolver when configured, otherwise the static startup map. It never
// panics on a nil resolver or nil map, so an unconfigured Service fails closed
// on any global Skill reference.
func (s *Service) skillDigests() map[string]string {
	if s.SkillCatalog != nil {
		if catalog := s.SkillCatalog(); catalog != nil {
			return catalog
		}
	}
	return s.GlobalSkills
}

// resolveLocalHero resolves a Graph-private Hero document, inlining local
// Skills into the prompt and tool allowlist (global Skills keep their catalog
// binding). For a frozen revision local Skills are loaded from the bundle; for
// a legacy revision they come from the mutable draft directories.
func (s *Service) resolveLocalHero(ctx context.Context, bundle *globalsource.GraphRevisionBundle, graphID string, document *heroesource.Document) (*domain.HeroDefinition, error) {
	encoded, _ := json.Marshal(document)
	var copy heroesource.Document
	_ = json.Unmarshal(encoded, &copy)
	copy.Skills = nil
	for _, binding := range document.Skills {
		scope, id, scoped := strings.Cut(binding.ID, "/")
		if !scoped || scope == "global" {
			if scoped {
				binding.ID = id
			}
			copy.Skills = append(copy.Skills, binding)
			continue
		}
		if scope != "local" {
			return nil, fmt.Errorf("Skill %q must use local/ or global/", binding.ID)
		}
		loaded, err := s.loadLocalSkill(bundle, graphID, id)
		if err != nil {
			return nil, err
		}
		copy.Prompt += "\n\n<graph_local_skill id=\"" + id + "\" digest=\"" + loaded.ContentHash + "\">\n" + loaded.PromptText + "\n</graph_local_skill>"
		// Graph-local Skill tool hints never widen the Hero's allowlist.
	}
	definition, diagnostics := (&store.HeroDiscovery{Models: s.Models}).ResolveDocument(ctx, &copy)
	if definition == nil {
		return nil, fmt.Errorf("private Hero is invalid: %s", heroDiagnostic(diagnostics))
	}
	return definition, nil
}

func (s *Service) loadLocalSkill(bundle *globalsource.GraphRevisionBundle, graphID, id string) (*skills.LoadedSkill, error) {
	if bundle != nil && bundle.Frozen {
		directory, ok := bundle.SkillDirs[id]
		if !ok {
			return nil, fmt.Errorf("revision %s local Skill %q missing from frozen bundle", bundle.Revision.ID(), id)
		}
		loaded, err := skills.Load(directory)
		if err != nil {
			return nil, fmt.Errorf("revision %s local Skill %q: %w", bundle.Revision.ID(), id, err)
		}
		digest, ok := bundle.Revision.SkillDigests[id]
		if !ok {
			return nil, fmt.Errorf("revision %s local Skill %q has no recorded digest", bundle.Revision.ID(), id)
		}
		expected, hasPrefix := strings.CutPrefix(digest, "sha256:")
		if !hasPrefix || expected != loaded.ContentHash {
			return nil, fmt.Errorf("revision %s local Skill %q failed integrity validation", bundle.Revision.ID(), id)
		}
		return loaded, nil
	}
	directory, err := s.Sources.GraphResourceDir(graphID, "skills", id)
	if err != nil {
		return nil, err
	}
	return skills.Load(directory)
}

func heroDiagnostic(diagnostics []domain.HeroValidationDiagnostic) string {
	if len(diagnostics) == 0 {
		return "invalid Hero"
	}
	return diagnostics[0].Message
}

const inlineTaskPrompt = "Execute the Task goal independently. Use the provided inputs and dependency outputs, distinguish evidence from assumptions, and submit a concise structured result."

var inlineBaseTools = []string{"read", "write", "edit", "ls", "grep", "find", "bash"}

func inlineHandle(taskID string) string {
	if len(taskID) > 23 {
		taskID = taskID[:23]
	}
	return "inline_" + taskID
}

// inlineHero synthesizes the default Hero for a model-backed Task. Unlike the
// legacy publish path it allows task_only context so delegated children can
// execute it.
func inlineHero(modelID string, thinking domain.ThinkingEffort, prompt string, tools []string) domain.HeroDefinition {
	if thinking == "" {
		thinking = domain.ThinkingDefault
	}
	return domain.HeroDefinition{
		SchemaVersion: 1, HeroPrompt: prompt,
		ModelBinding: domain.HeroModelBinding{Mode: domain.HeroModelFixed, ModelProfileID: modelID, ThinkingEffort: thinking, FallbackModelProfileIDs: []string{}, OverridableFields: []string{}},
		Skills:       domain.HeroSkills{Entries: []domain.HeroSkillEntry{}}, Authority: domain.HeroAuthorityMutation,
		PermissionCeiling: domain.PermissionAsk, AllowedTools: unique(tools),
		ContextPolicy:    domain.HeroContextPolicy{DefaultMode: domain.HeroContextTask, AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}},
		DelegationPolicy: domain.HeroDelegationPolicy{Admission: domain.DelegationAutoWithinBudget, AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"}, MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16, BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 16, MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract:   "text-v1", MaxLoopIterations: 8,
	}
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
