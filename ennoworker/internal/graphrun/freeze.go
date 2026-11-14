package graphrun

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// localHeroVersionID names the frozen local Hero. Every Graph uses the graph prefix.
func (s *Service) localHeroVersionID(sourceID, heroID, digest string) string {
	return "graph:" + sourceID + "/" + heroID + "@" + digest
}

// freezeDefinition snapshots every task, including check and terminal gates.
// It is the only freeze: every Graph Run writes the same
// run_agent_flow_nodes contract.
func (s *Service) freezeDefinition(ctx context.Context, graphID string, definition *domain.FlowDefinition,
	bundledHeroes map[string]*heroesource.Document, bundle *globalsource.GraphRevisionBundle) ([]store.FlowNodeFreeze, error) {
	order, err := agentflow.TaskOrder(definition)
	if err != nil {
		return nil, err
	}
	indexOf := make(map[string]int, len(order))
	for index, name := range order {
		indexOf[name] = index
	}
	skillCatalog := s.skillDigests()
	freeze := make([]store.FlowNodeFreeze, 0, len(order))
	emptyBudget, _ := json.Marshal(domain.BudgetCeilingJSON{})
	for _, name := range order {
		task := definition.Tasks[name]
		node := store.FlowNodeFreeze{
			TaskIndex: indexOf[name], Handle: name,
			GoalDigest: agentflow.TaskGoalDigest(task.Goal), GoalText: task.Goal,
			BudgetJSON: emptyBudget,
			Writes:     append([]string(nil), task.Writes...),
		}
		if task.Terminal != nil {
			node.ReadOnly = true
			node.Writes = nil
			freeze = append(freeze, node)
			continue
		}
		if task.Type == domain.FlowTaskCheck {
			if strings.TrimSpace(task.Command) == "" {
				return nil, fmt.Errorf("Task %q: check command is required", name)
			}
			node.ReadOnly = false
			freeze = append(freeze, node)
			continue
		}
		if task.Type != domain.FlowTaskHero {
			return nil, fmt.Errorf("Task %q: unsupported type %q", name, task.Type)
		}
		hero, globalSkills, err := s.freezeHero(ctx, graphID, name, task, bundledHeroes, skillCatalog, bundle)
		if err != nil {
			return nil, fmt.Errorf("Task %q: %w", name, err)
		}
		readOnly := domain.HeroDefinitionIsReadOnly(hero.Definition)
		if task.FanOut != nil && !readOnly {
			return nil, fmt.Errorf("graph_fan_out_not_read_only: Task %q fan_out requires a fully read-only Hero", name)
		}
		frozenSkills, err := domain.NormalizeFrozenSkills(globalSkills)
		if err != nil {
			return nil, fmt.Errorf("Task %q: %w", name, err)
		}
		switch hero.Definition.DelegationPolicy.Admission {
		case domain.DelegationAutoWithinBudget:
		case domain.DelegationDenied:
			return nil, fmt.Errorf("Task %q: Hero %s denies Host delegation", name, hero.VersionID)
		case domain.DelegationApprovalRequired:
			return nil, fmt.Errorf("Task %q: Hero %s requires explicit delegation approval; Graph Runs only admit auto_within_budget Heroes", name, hero.VersionID)
		default:
			return nil, fmt.Errorf("Task %q: Hero %s has an invalid admission policy", name, hero.VersionID)
		}
		ceiling := hero.Definition.DelegationPolicy.BudgetCeiling
		if task.Budget != nil && task.Budget.Tokens > 0 {
			if task.Budget.Tokens > ceiling.MaxTotalTokens {
				return nil, fmt.Errorf("Task %q: budget tokens %d exceed the Hero ceiling %d", name, task.Budget.Tokens, ceiling.MaxTotalTokens)
			}
			ceiling.MaxTotalTokens = task.Budget.Tokens
		}
		budgetJSON, _ := json.Marshal(domain.BudgetCeilingJSON{
			MaxModelCalls: ceiling.MaxModelCalls, MaxToolCalls: ceiling.MaxToolCalls,
			MaxTotalTokens: ceiling.MaxTotalTokens, MaxOutputTokens: ceiling.MaxOutputTokens,
			MaxCostMicros: ceiling.MaxCostUSDMicros, MaxWallTimeMS: ceiling.MaxWallTimeMS,
		})
		heroDefJSON, _ := json.Marshal(hero.Definition)
		node.HeroVersionID = hero.VersionID
		node.Skills = frozenSkills
		node.BudgetJSON = budgetJSON
		node.ReadOnly = readOnly
		node.HeroDefinitionJSON = heroDefJSON
		freeze = append(freeze, node)
	}
	return freeze, nil
}

func (s *Service) freezeHero(ctx context.Context, graphID, taskID string, task domain.FlowTask,
	bundled map[string]*heroesource.Document, skillCatalog map[string]string, bundle *globalsource.GraphRevisionBundle) (*frozenTaskHero, domain.FrozenSkillRefs, error) {
	ref := strings.TrimSpace(task.Hero)
	if ref == "" {
		return nil, nil, fmt.Errorf("hero is required")
	}
	handle, versionText, hasVersion := strings.Cut(ref, "@")
	handle = strings.TrimSpace(handle)
	var document *heroesource.Document
	var versionID, digest string
	switch {
	case strings.HasPrefix(handle, "local/"):
		if hasVersion {
			return nil, nil, fmt.Errorf("graph_local_hero_version_unsupported: Hero %q cannot be version-pinned", ref)
		}
		id := strings.TrimSpace(strings.TrimPrefix(handle, "local/"))
		bundledDocument, ok := bundled[id]
		if !ok {
			// A revision that recorded any local digest is frozen. A missing Hero
			// then fails closed. A revision with no local digests is the historical
			// shape and still reads the mutable draft.
			if bundle != nil && bundle.Frozen {
				return nil, nil, fmt.Errorf("revision %s local Hero %q missing from frozen bundle", bundle.Revision.ID(), id)
			}
			draft, draftDigest, err := s.Sources.ReadGraphHero(graphID, id)
			if err != nil {
				return nil, nil, fmt.Errorf("graph-local Hero %q is not present in the published revision bundle", id)
			}
			bundledDocument, ok, digest = draft, true, draftDigest
			_ = ok
		}
		document = bundledDocument
		if digest == "" {
			digest, _ = heroesource.SourceDigest(document)
		}
		versionID = s.localHeroVersionID(graphID, id, digest)
	case strings.HasPrefix(handle, "global/"):
		id := strings.TrimSpace(strings.TrimPrefix(handle, "global/"))
		doc, revision, err := s.globalHero(id, versionText, hasVersion)
		if err != nil {
			return nil, nil, err
		}
		document, versionID, digest = doc, id+"@"+revision.ID(), revision.Digest
	default:
		if !hasVersion {
			if bundledDocument, ok := bundled[handle]; ok {
				document = bundledDocument
				digest, _ = heroesource.SourceDigest(document)
				versionID = s.localHeroVersionID(graphID, handle, digest)
				break
			}
		}
		doc, revision, err := s.globalHero(handle, versionText, hasVersion)
		if err != nil {
			return nil, nil, err
		}
		document, versionID, digest = doc, handle+"@"+revision.ID(), revision.Digest
	}
	definition, err := s.resolveLocalHero(ctx, bundle, graphID, document)
	if err != nil {
		return nil, nil, fmt.Errorf("Hero %q: %w", handle, err)
	}
	globalSkills, err := s.applyTaskSkills(ctx, graphID, definition, task.Skills, skillCatalog, bundle)
	if err != nil {
		return nil, nil, err
	}
	return &frozenTaskHero{
		ObjectID: versionID, VersionID: versionID,
		Handle: document.Handle, DisplayName: document.Name,
		ConfigDigest: digest, Definition: *definition,
	}, globalSkills, nil
}

func (s *Service) globalHero(handle, versionText string, hasVersion bool) (*heroesource.Document, *globalsource.Revision, error) {
	if !hasVersion {
		document, revision, err := s.Sources.LatestHeroRevision(handle)
		if err != nil {
			return nil, nil, fmt.Errorf("global Hero %q: %w", handle, err)
		}
		return document, revision, nil
	}
	versionText = strings.TrimSpace(versionText)
	revisions, err := s.Sources.ListHeroRevisions(handle)
	if err != nil {
		return nil, nil, err
	}
	for _, revision := range revisions {
		if revision.ID() == versionText || strconv.Itoa(revision.Version) == versionText {
			document, read, err := s.Sources.ReadHeroRevision(handle, revision.ID())
			if err != nil {
				return nil, nil, err
			}
			return document, read, nil
		}
	}
	return nil, nil, fmt.Errorf("global Hero %q has no published version %q", handle, versionText)
}

// applyTaskSkills inlines local Skills (never widening the Hero's tool
// allowlist) and returns the content-pinned global Skill refs.
func (s *Service) applyTaskSkills(ctx context.Context, graphID string, definition *domain.HeroDefinition,
	refs []string, skillCatalog map[string]string, bundle *globalsource.GraphRevisionBundle) (domain.FrozenSkillRefs, error) {
	globalSkills := make(domain.FrozenSkillRefs, 0, len(refs))
	for _, ref := range refs {
		scope, id, scoped := strings.Cut(strings.TrimSpace(ref), "/")
		if !scoped {
			// Bare refs resolve graph-local first, exactly like task Heroes.
			if loaded, err := s.loadLocalSkill(bundle, graphID, id); err == nil {
				definition.HeroPrompt += "\n\n<graph_local_skill id=\"" + id + "\" digest=\"" + loaded.ContentHash + "\">\n" + loaded.PromptText + "\n</graph_local_skill>"
				continue
			}
			scope, id = "global", ref
		}
		switch scope {
		case "local":
			loaded, err := s.loadLocalSkill(bundle, graphID, id)
			if err != nil {
				return nil, fmt.Errorf("local Skill %q: %w", id, err)
			}
			definition.HeroPrompt += "\n\n<graph_local_skill id=\"" + id + "\" digest=\"" + loaded.ContentHash + "\">\n" + loaded.PromptText + "\n</graph_local_skill>"
		case "global":
			digest, ok := skillCatalog[id]
			if !ok {
				return nil, fmt.Errorf("global Skill %q is not available", id)
			}
			if digest == "" {
				return nil, fmt.Errorf("global Skill %q has no content digest; refusing to freeze an unpinned Skill", id)
			}
			globalSkills = append(globalSkills, domain.FrozenSkillRef{ID: id, Digest: digest})
		default:
			return nil, fmt.Errorf("unsupported Skill reference %q", ref)
		}
	}
	definition.AllowedTools = sortedUniqueStrings(definition.AllowedTools)
	return globalSkills, nil
}

func sortedUniqueStrings(values []string) []string {
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
