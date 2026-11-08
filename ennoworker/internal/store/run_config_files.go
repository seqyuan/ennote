package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

// loadTurnTargetKind reads the invocation target kind of a turn so a format-2
// config freeze can tell a Role turn (which must resolve a Role revision) from
// a Host turn (which must not). A missing turn is an invalid invocation target.
func loadTurnTargetKind(ctx context.Context, tx *sql.Tx, turnID string) (string, error) {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT target_kind FROM turns WHERE id=?`, turnID).Scan(&kind); err != nil {
		return "", domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("load invocation target: %w", err))
	}
	return kind, nil
}

// loadTurnSkills reads the global Skills the user attached to the turn.
// A missing turn or an empty list means "none": only a malformed blob is an
// error, and even then the freeze refuses rather than silently dropping a
// selection the user made.
func loadTurnSkills(ctx context.Context, tx *sql.Tx, turnID string) ([]string, error) {
	if strings.TrimSpace(turnID) == "" {
		// Delegated children and Flow task Runs have no turn of their own; their
		// Skills arrive as task bindings, not as a user selection.
		return nil, nil
	}
	var encoded string
	if err := tx.QueryRowContext(ctx, `SELECT skills_json FROM turns WHERE id=?`, turnID).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load turn skills: %w", err)
	}
	if strings.TrimSpace(encoded) == "" || strings.TrimSpace(encoded) == "null" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(encoded), &ids); err != nil {
		return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("decode turn skills: %w", err))
	}
	return ids, nil
}

// freezeSelectedSkills pins the global Skills the user attached to the turn.
// They are disjoint from the Hero's preload bindings: a Hero preload wins on an
// id collision, because it is already pinned by a published revision and the
// selected copy would only restate the same digest.
func (r *RunRepo) freezeSelectedSkills(hero *domain.FrozenHeroExecution, ids []string) (domain.FrozenSkillRefs, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	catalog := r.skillDigests()
	refs := make(domain.FrozenSkillRefs, 0, len(ids))
	seen := map[string]bool{}
	if hero != nil {
		for _, id := range hero.FrozenSkills.IDs() {
			seen[id] = true
		}
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		digest := ""
		if catalog != nil {
			digest = strings.TrimSpace(catalog[id])
		}
		if digest == "" {
			return nil, domain.NewCodedError(domain.ErrorSkillNotFound,
				fmt.Errorf("selected skill %q is missing from the Skill catalog or has no content digest", id))
		}
		refs = append(refs, domain.FrozenSkillRef{ID: id, Digest: digest})
	}
	if len(refs) == 0 {
		return nil, nil
	}
	return domain.NormalizeFrozenSkills(refs)
}

// skillDigests returns the Skill digest catalog to use for one freeze: the
// live resolver when configured, otherwise the static startup map. It never
// panics on a nil resolver or nil map; a nil result is the legacy/fixture path
// that leaves preload refs unpinned.
func (r *RunRepo) skillDigests() map[string]string {
	if r.SkillCatalog != nil {
		if catalog := r.SkillCatalog(); catalog != nil {
			return catalog
		}
	}
	return r.SkillDigests
}

// freezeHeroPreloadSkills pins every preload-mode Role Skill against the
// current Skill catalog. A published Role revision is immutable, but its
// preload binding names a Skill that lives outside the revision; without a
// digest a later on-disk edit would silently change the Role's prompt.
//
// A nil catalog (legacy tests/fixtures) leaves the refs unpinned. A non-nil
// catalog must be able to name every preload Skill: a missing or digest-less
// entry fails the freeze instead of publishing an unverifiable prompt.
// 'available' Skills are not injected into the prompt and are not pinned.
func (r *RunRepo) freezeHeroPreloadSkills(hero *domain.FrozenHeroExecution) error {
	if hero == nil {
		return nil
	}
	hero.FrozenSkills = nil
	// Read the catalog once per freeze, straight from the current on-disk
	// catalog, so a Skill created or edited after startup pins correctly
	// without restarting the Worker.
	catalog := r.skillDigests()
	if catalog == nil {
		return nil
	}
	refs := make(domain.FrozenSkillRefs, 0, len(hero.Skills.Entries))
	for _, entry := range hero.Skills.Entries {
		if entry.Mode != domain.HeroSkillPreload {
			continue
		}
		id := strings.TrimSpace(entry.SkillID)
		digest := strings.TrimSpace(catalog[id])
		if digest == "" {
			return domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
				fmt.Errorf("hero %s (%s@%d) preload skill %q is missing from the Skill catalog or has no content digest; refusing to freeze an unpinned Role prompt",
					hero.Handle, hero.ObjectID, hero.Version, id))
		}
		refs = append(refs, domain.FrozenSkillRef{ID: id, Digest: digest})
	}
	if len(refs) == 0 {
		return nil
	}
	normalized, err := domain.NormalizeFrozenSkills(refs)
	if err != nil {
		return domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("hero %s preload skills: %w", hero.Handle, err))
	}
	hero.FrozenSkills = normalized
	return nil
}

func (r *RunRepo) resolveAndFreezeFileConfig(ctx context.Context, run *domain.AgentRun) (*ResolvedRunConfig, error) {
	if r.Providers == nil || r.Models == nil {
		return nil, fmt.Errorf("file-backed model resolver is incomplete")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin effective config transaction: %w", err)
	}
	defer tx.Rollback()
	var currentStatus domain.RunStatus
	var storedEffective, storedPromptSnapshot, storedPromptDigest, requested string
	var sessionModelID, sessionAgentID, sessionCompactionPolicyID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT ar.status,ar.effective_config_json,
		ar.system_prompt_snapshot_json,ar.system_prompt_digest,ar.requested_config_json,
		s.default_model_profile_id,s.default_agent_profile_id,s.compaction_policy_profile_id
		FROM agent_runs ar JOIN sessions s ON s.id=ar.session_id WHERE ar.id=?`, run.ID).Scan(
		&currentStatus, &storedEffective, &storedPromptSnapshot, &storedPromptDigest, &requested,
		&sessionModelID, &sessionAgentID, &sessionCompactionPolicyID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if currentStatus != domain.RunRunning {
		return nil, fmt.Errorf("%w: effective config requires running run", ErrInvalidRunState)
	}
	if strings.TrimSpace(storedEffective) != "" && strings.TrimSpace(storedEffective) != "{}" {
		var effective domain.EffectiveRunConfig
		if err := json.Unmarshal([]byte(storedEffective), &effective); err != nil {
			return nil, err
		}
		resolved, err := r.resolvedFromFrozenRuntime(effective)
		if err != nil {
			return nil, err
		}
		prompt, err := decodeSystemPromptSnapshot(storedPromptSnapshot, storedPromptDigest)
		if err != nil {
			return nil, err
		}
		resolved.SystemPrompt = prompt
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return resolved, nil
	}
	var requestedConfig requestedRunConfig
	if strings.TrimSpace(requested) != "" {
		if err := json.Unmarshal([]byte(requested), &requestedConfig); err != nil {
			return nil, fmt.Errorf("decode requested config: %w", err)
		}
	}
	var frozenHero *domain.FrozenHeroExecution
	var heroDefinition domain.HeroDefinition
	var heroPrompt string
	if run.CommitFormatVersion == domain.CommitFormatSpeakerV2 {
		if run.RunKind == domain.RunKindDelegatedAgent {
			// Private children resolve the exact Role facts frozen on the
			// delegation item at Run start; no global hero SQL is consulted.
			var metaJSON string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(di.hero_meta_json, '{}')
				FROM delegation_item_attempts a
				JOIN delegation_items di ON di.id=a.item_id
				WHERE a.child_run_id=?`, run.ID).Scan(&metaJSON); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
						errors.New("child Run has no frozen delegation Role meta"))
				}
				return nil, err
			}
			if strings.TrimSpace(metaJSON) == "" || strings.TrimSpace(metaJSON) == "{}" {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
					errors.New("file-backed delegated Role execution requires a frozen Role meta"))
			}
			var meta DelegationHeroMeta
			if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					fmt.Errorf("decode frozen delegation Role meta: %w", err))
			}
			if err := validateFrozenDelegationMeta(meta); err != nil {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid, err)
			}
			heroDefinition = meta.Definition
			var frozenSpeaker struct {
				ObjectID     string `json:"objectId"`
				VersionID    string `json:"versionId"`
				Handle       string `json:"handle"`
				DisplayName  string `json:"displayName"`
				ConfigDigest string `json:"configDigest"`
			}
			if err := json.Unmarshal(run.SpeakerSnapshot, &frozenSpeaker); err != nil || frozenSpeaker.ObjectID != meta.ObjectID ||
				frozenSpeaker.VersionID != meta.VersionID || frozenSpeaker.ConfigDigest != meta.ConfigDigest ||
				frozenSpeaker.Handle != meta.Handle || frozenSpeaker.Handle == "" {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					errors.New("child speaker snapshot does not match the frozen Role meta"))
			}
			allowedContext := false
			for _, allowed := range heroDefinition.ContextPolicy.AllowedModes {
				allowedContext = allowedContext || allowed == domain.HeroContextTask
			}
			if !allowedContext {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
					errors.New("frozen Role revision does not allow task_only context"))
			}
			if heroDefinition.Authority == domain.HeroAuthorityReadOnly {
				for _, tool := range heroDefinition.AllowedTools {
					if heroToolRequiresMutation(tool) {
						return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
							fmt.Errorf("read-only Role revision contains mutation tool %s", tool))
					}
				}
			}
			frozenHero = &domain.FrozenHeroExecution{
				ObjectID: meta.ObjectID, VersionID: meta.VersionID, Version: frozenHeroVersion(meta.VersionID),
				Handle: meta.Handle, DisplayName: meta.DisplayName, ConfigDigest: meta.ConfigDigest,
				Authority: heroDefinition.Authority, PermissionCeiling: heroDefinition.PermissionCeiling,
				AllowedTools: append([]string(nil), heroDefinition.AllowedTools...), Skills: heroDefinition.Skills,
				OutputContract: heroDefinition.OutputContract,
			}
			heroPrompt = heroDefinition.HeroPrompt
		} else if targetKind, kindErr := loadTurnTargetKind(ctx, tx, run.TurnID); kindErr != nil {
			return nil, kindErr
		} else if targetKind == "hero" {
			// Only a Role turn carries a Role target to resolve. A format-2 Host
			// turn (a plain user message on a room lineage already upgraded to the
			// speaker ledger) must keep the Agent/default base prompt instead of
			// failing as if its Role target were missing.
			var heroID, versionID, contextMode string
			if err := tx.QueryRowContext(ctx, `SELECT target_object_id,target_version_id,context_mode
				FROM turns WHERE id=? AND target_kind='hero'`, run.TurnID).Scan(&heroID, &versionID, &contextMode); err != nil {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("load Role target: %w", err))
			}
			resolvedHero, err := r.resolveFileHero(ctx, heroID, versionID)
			if err != nil {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, err)
			}
			heroDefinition = resolvedHero.Definition
			allowedContext := false
			for _, allowed := range heroDefinition.ContextPolicy.AllowedModes {
				allowedContext = allowedContext || string(allowed) == contextMode ||
					(allowed == domain.HeroContextReply && contextMode == string(domain.InvocationContextReplyTo))
			}
			if !allowedContext {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
					fmt.Errorf("Role revision does not allow %s context", contextMode))
			}
			var speaker struct {
				ObjectID, VersionID, Handle, DisplayName, ConfigDigest string
			}
			if err := json.Unmarshal(run.SpeakerSnapshot, &speaker); err != nil || speaker.ObjectID != heroID ||
				speaker.VersionID != versionID || speaker.ConfigDigest != resolvedHero.Revision.Digest || speaker.Handle != resolvedHero.Document.Handle {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					errors.New("Role speaker snapshot does not match the immutable revision"))
			}
			if requestedConfig.ModelProfileID != "" || len(requestedConfig.CandidateModelProfileIDs) != 0 ||
				requestedConfig.AllowAutoRoute || requestedConfig.ThinkingEffort != "" || requestedConfig.MaxIterations != 0 ||
				requestedConfig.ToolPolicyProfileID != "" {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					errors.New("direct Role execution does not allow runtime identity, model, loop, or permission overrides"))
			}
			if heroDefinition.Authority == domain.HeroAuthorityReadOnly {
				for _, tool := range heroDefinition.AllowedTools {
					if heroToolRequiresMutation(tool) {
						return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
							fmt.Errorf("read-only Role revision contains mutation tool %s", tool))
					}
				}
			}
			frozenHero = &domain.FrozenHeroExecution{
				ObjectID: heroID, VersionID: versionID, Version: resolvedHero.Revision.Version,
				Handle: resolvedHero.Document.Handle, DisplayName: resolvedHero.Document.Name,
				ConfigDigest: resolvedHero.Revision.Digest, Authority: heroDefinition.Authority,
				PermissionCeiling: heroDefinition.PermissionCeiling,
				AllowedTools:      append([]string(nil), heroDefinition.AllowedTools...), Skills: heroDefinition.Skills,
				OutputContract: heroDefinition.OutputContract,
			}
			heroPrompt = heroDefinition.HeroPrompt
		}
	}
	if err := r.freezeHeroPreloadSkills(frozenHero); err != nil {
		return nil, err
	}
	turnSkills, err := loadTurnSkills(ctx, tx, run.TurnID)
	if err != nil {
		return nil, err
	}
	selectedSkills, err := r.freezeSelectedSkills(frozenHero, turnSkills)
	if err != nil {
		return nil, err
	}
	if frozenHero == nil && sessionAgentID.Valid {
		return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("managed agent profile %s is unsupported by the V2 file store", sessionAgentID.String))
	}
	modelID := strings.TrimSpace(requestedConfig.ModelProfileID)
	if frozenHero != nil {
		modelID = heroDefinition.ModelBinding.ModelProfileID
	}
	if modelID == "" && sessionModelID.Valid {
		modelID = sessionModelID.String
	}
	if modelID == "" {
		settings, err := r.Models.Files.Settings.Read()
		if err != nil {
			return nil, err
		}
		modelID = settings.DefaultModel
	}
	if modelID == "" {
		models, err := r.Models.List(ctx)
		if err != nil {
			return nil, err
		}
		if len(models) != 0 {
			modelID = models[0].ID
		}
	}
	if modelID == "" {
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, errors.New("no active model profile is configured"))
	}
	model, err := r.Models.FindByID(ctx, modelID)
	if err != nil || model == nil {
		if err == nil {
			err = fmt.Errorf("active model profile not found: %s", modelID)
		}
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, err)
	}
	provider, err := r.Providers.FindByID(ctx, model.ProviderID)
	if err != nil || provider == nil {
		if err == nil {
			err = fmt.Errorf("active provider profile not found: %s", model.ProviderID)
		}
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, err)
	}
	initialRuntime := runtimeSnapshot(*model, *provider)
	thinkingEffort := requestedConfig.ThinkingEffort
	if frozenHero != nil {
		thinkingEffort = heroDefinition.ModelBinding.ThinkingEffort
	}
	if thinkingEffort == "" {
		thinkingEffort = domain.ThinkingDefault
	}
	if err := validateThinkingSelection(initialRuntime, thinkingEffort); err != nil {
		return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid, err)
	}
	policies := r.Policies
	if policies == nil {
		policies = &fileconfig.PolicyStore{}
	}
	// A frozen Role's permission ceiling pins the tool policy the same way the
	// published definition does: discuss/ask/auto map to the builtin profiles.
	// A delegated child additionally converges on its parent's frozen mode so a
	// Role can never loosen the permission the parent Run is running under.
	toolPolicyID := requestedConfig.ToolPolicyProfileID
	if frozenHero != nil {
		ceiling := heroDefinition.PermissionCeiling
		if run.ParentRunID != "" {
			parentMode, err := parentFrozenToolPolicyModeTx(ctx, tx, run.ParentRunID)
			if err != nil {
				return nil, err
			}
			ceiling, err = resolveChildPermissionCeiling(parentMode, ceiling)
			if err != nil {
				return nil, err
			}
		}
		switch ceiling {
		case domain.PermissionDiscuss:
			toolPolicyID = "builtin-tool-discuss-v3"
		case domain.PermissionAsk:
			toolPolicyID = "builtin-tool-ask-v1"
		case domain.PermissionAuto:
			toolPolicyID = "builtin-tool-auto-v1"
		default:
			return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
				errors.New("invalid Role permission ceiling"))
		}
	}
	toolPolicy, err := policies.Resolve(ctx, toolPolicyID, domain.PolicyKindTool)
	if err != nil {
		return nil, err
	}
	turnPolicy, err := policies.Resolve(ctx, requestedConfig.TurnPolicyProfileID, domain.PolicyKindTurn)
	if err != nil {
		return nil, err
	}
	visionPolicy, err := policies.Resolve(ctx, requestedConfig.VisionPolicyProfileID, domain.PolicyKindVision)
	if err != nil {
		return nil, err
	}
	compactionID := firstNonEmpty(requestedConfig.CompactionPolicyProfileID, nullString(sessionCompactionPolicyID))
	compactionPolicy, err := policies.Resolve(ctx, compactionID, domain.PolicyKindCompaction)
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, err)
	}
	var compactionConfig domain.CompactionPolicyConfig
	if err := json.Unmarshal(compactionPolicy.Config, &compactionConfig); err != nil {
		return nil, err
	}
	compactionRuntime := initialRuntime
	if compactionConfig.CompactionModelProfileID != nil && strings.TrimSpace(*compactionConfig.CompactionModelProfileID) != "" {
		compactionModel, err := r.Models.FindByID(ctx, strings.TrimSpace(*compactionConfig.CompactionModelProfileID))
		if err != nil || compactionModel == nil {
			return nil, domain.NewCodedError(domain.ErrorCompactionModelUnavailable, fmt.Errorf("compaction model is unavailable"))
		}
		compactionProvider, err := r.Providers.FindByID(ctx, compactionModel.ProviderID)
		if err != nil || compactionProvider == nil {
			return nil, domain.NewCodedError(domain.ErrorCompactionModelUnavailable, fmt.Errorf("compaction provider is unavailable"))
		}
		compactionRuntime = runtimeSnapshot(*compactionModel, *compactionProvider)
	}
	var turnConfig domain.TurnPolicyConfig
	if err := json.Unmarshal(turnPolicy.Config, &turnConfig); err != nil {
		return nil, err
	}
	candidateIDs := append([]string(nil), requestedConfig.CandidateModelProfileIDs...)
	if frozenHero != nil {
		candidateIDs = append([]string(nil), heroDefinition.ModelBinding.FallbackModelProfileIDs...)
	} else if len(candidateIDs) == 0 {
		candidateIDs = append(candidateIDs, turnConfig.CandidateModelProfileIDs...)
	}
	candidateIDs = append(candidateIDs, model.ID)
	candidates, err := r.fileRuntimeCandidates(ctx, candidateIDs)
	if err != nil {
		return nil, err
	}
	threshold := turnConfig.Threshold
	if threshold == 0 {
		threshold = 0.7
	}
	maxIterations := requestedConfig.MaxIterations
	if frozenHero != nil {
		maxIterations = heroDefinition.MaxLoopIterations
	}
	if maxIterations == 0 {
		maxIterations = defaultMaxIterations
	}
	if maxIterations < 1 {
		return nil, fmt.Errorf("maxIterations must be at least 1")
	}
	toolMode := requestedConfig.ToolExecution
	if toolMode == "" {
		toolMode = "sequential"
	}
	if toolMode != "sequential" && toolMode != "safe_parallel" {
		return nil, fmt.Errorf("invalid tool execution mode %q", toolMode)
	}
	maxReadTools := requestedConfig.MaxConcurrentReadTools
	if maxReadTools == 0 {
		maxReadTools = defaultMaxConcurrentReadTools
	}
	effective := domain.EffectiveRunConfig{
		ProviderProfileID: provider.ID, ModelProfileID: model.ID, APIModel: model.ModelName,
		ContextTokens: model.ContextWindow, MaxOutputTokens: model.MaxOutputTokens,
		MaxIterations: maxIterations, ThinkingEffort: thinkingEffort,
		ToolExecution:  domain.ToolExecutionConfig{Mode: toolMode, MaxConcurrentReadTools: maxReadTools},
		InitialRuntime: initialRuntime,
		Routing: domain.FrozenRoutingConfig{Candidates: candidates, Threshold: threshold,
			Pinned:         frozenHero != nil || (requestedConfig.ModelProfileID != "" && !requestedConfig.AllowAutoRoute),
			AllowAutoRoute: frozenHero == nil && (requestedConfig.AllowAutoRoute || (turnConfig.Mode == "context_upgrade" && requestedConfig.ModelProfileID == ""))},
		ToolPolicy: toolPolicy, TurnPolicy: turnPolicy, VisionPolicy: visionPolicy,
		CompactionPolicy: compactionPolicy, CompactionRuntime: compactionRuntime, Hero: frozenHero,
		SelectedSkills: selectedSkills,
	}
	if run.ParentRunID == "" && run.RunKind == domain.RunKindAgent {
		snapshot, err := freezeFileDelegationPolicyTx(ctx, tx, policies, run.ID)
		if err != nil {
			return nil, err
		}
		effective.Delegation = snapshot
	}
	encoded, err := json.Marshal(effective)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(encoded), provider.APIKey) && provider.APIKey != "" {
		return nil, errors.New("credential leaked into effective config")
	}
	promptProfileID := ""
	if frozenHero != nil {
		promptProfileID = frozenHero.ObjectID
	}
	prompt, err := newSystemPromptSnapshot(promptProfileID, heroPrompt)
	if err != nil {
		return nil, err
	}
	encodedPrompt, _ := json.Marshal(prompt)
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET effective_config_json=?,
		system_prompt_snapshot_json=?,system_prompt_digest=?
		WHERE id=? AND status='running' AND effective_config_json='{}'`, string(encoded), string(encodedPrompt), prompt.Digest, run.ID)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, fmt.Errorf("%w: effective config was already changed", ErrInvalidRunState)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	run.EffectiveConfig = append(json.RawMessage(nil), encoded...)
	return &ResolvedRunConfig{Effective: effective, Provider: *provider, Model: *model, SystemPrompt: prompt}, nil
}

func (r *RunRepo) resolvedFromFrozenRuntime(effective domain.EffectiveRunConfig) (*ResolvedRunConfig, error) {
	runtime := effective.InitialRuntime
	if runtime.ModelProfileID == "" {
		return nil, errors.New("frozen effective config has no model runtime")
	}
	credentialRef := firstNonEmpty(runtime.CredentialRef, runtime.ProviderProfileID)
	apiKey, err := r.Models.Files.Credentials.Resolve(credentialRef)
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorProviderCredentialUnavailable, err)
	}
	runtime.APIKey = apiKey
	effective.InitialRuntime.APIKey = apiKey
	for index := range effective.Routing.Candidates {
		if effective.Routing.Candidates[index].CredentialRef == credentialRef {
			effective.Routing.Candidates[index].APIKey = apiKey
		}
	}
	if effective.CompactionRuntime.CredentialRef == credentialRef {
		effective.CompactionRuntime.APIKey = apiKey
	}
	model := domain.ModelProfile{
		ID: runtime.ModelProfileID, ProviderID: runtime.ProviderProfileID, ModelName: runtime.APIModel,
		DisplayName: runtime.APIModel, ContextWindow: runtime.ContextTokens, MaxOutputTokens: runtime.MaxOutputTokens,
		SupportsVision: runtime.SupportsVision, SupportsToolUse: runtime.SupportsToolUse,
		SupportsThinking: runtime.SupportsThinking, ThinkingDialect: runtime.ThinkingDialect,
		SupportedThinkingEfforts: append([]domain.ThinkingEffort(nil), runtime.SupportedThinkingEfforts...), Status: "frozen",
	}
	provider := domain.ProviderProfile{
		ID: runtime.ProviderProfileID, Name: runtime.ProviderProfileID,
		ProviderType: domain.ProviderOpenAICompatible, API: runtime.Protocol(), BaseURL: runtime.BaseURL,
		CredentialRef: credentialRef, APIKey: apiKey, Proxy: runtime.Proxy, Status: "frozen",
	}
	return &ResolvedRunConfig{Effective: effective, Provider: provider, Model: model}, nil
}

func (r *RunRepo) fileRuntimeCandidates(ctx context.Context, ids []string) ([]domain.ModelRuntimeSnapshot, error) {
	seen := map[string]bool{}
	candidates := make([]domain.ModelRuntimeSnapshot, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		model, err := r.Models.FindByID(ctx, id)
		if err != nil || model == nil {
			return nil, fmt.Errorf("active model profile not found: %s", id)
		}
		provider, err := r.Providers.FindByID(ctx, model.ProviderID)
		if err != nil || provider == nil {
			return nil, fmt.Errorf("active provider profile not found: %s", model.ProviderID)
		}
		candidates = append(candidates, runtimeSnapshot(*model, *provider))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ContextTokens == candidates[j].ContextTokens {
			return candidates[i].ModelProfileID < candidates[j].ModelProfileID
		}
		return candidates[i].ContextTokens < candidates[j].ContextTokens
	})
	return candidates, nil
}

// permissionModeRank orders the three selectable builtin modes from strictest
// (0) to loosest. The two deployment modes are deliberately absent: they are not
// expressible as a builtin profile, so a child cannot be pinned to them.
func permissionModeRank(mode domain.PermissionMode) (rank int, ok bool) {
	switch mode {
	case domain.PermissionDiscuss:
		return 0, true
	case domain.PermissionAsk:
		return 1, true
	case domain.PermissionAuto:
		return 2, true
	default:
		return 0, false
	}
}

// deploymentModeAllowExistingBehavior is the loosest tool policy the Worker can
// hold: it permits every executable, so it can never tighten a child.
const deploymentModeAllowExistingBehavior = domain.PermissionMode("allow_existing_behavior")

// resolveChildPermissionCeiling returns the mode a delegated child freezes: the
// stricter of the parent's mode and the Role's ceiling. It fails closed rather
// than leaving the child loose whenever an answer cannot be proven.
//
// restricted is the strictest deployment mode, but a child can only be pinned to
// a builtin profile, so the strictest builtin (discuss) is the tightest ceiling
// available. An unrecognized mode (an older frozen snapshot, a mode this build
// does not know) is treated the same way: the child must not inherit the looser
// Role ceiling just because the parent's mode could not be ranked.
func resolveChildPermissionCeiling(parentMode, heroCeiling domain.PermissionMode) (domain.PermissionMode, error) {
	ceilingRank, ok := permissionModeRank(heroCeiling)
	if !ok {
		return "", domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			errors.New("invalid Role permission ceiling"))
	}
	if parentMode == deploymentModeAllowExistingBehavior {
		return heroCeiling, nil
	}
	parentRank, known := permissionModeRank(parentMode)
	if !known {
		return domain.PermissionDiscuss, nil
	}
	if parentRank < ceilingRank {
		return parentMode, nil
	}
	return heroCeiling, nil
}

// parentFrozenToolPolicyModeTx reads the tool policy mode frozen on a child
// Run's parent. A delegated child is only created after its parent froze an
// effective config, so a missing or unreadable snapshot is an inconsistent
// child state rather than a reason to fall back to the looser Role ceiling.
func parentFrozenToolPolicyModeTx(ctx context.Context, tx *sql.Tx, parentRunID string) (domain.PermissionMode, error) {
	var effectiveJSON string
	if err := tx.QueryRowContext(ctx, `SELECT effective_config_json FROM agent_runs WHERE id=?`, parentRunID).
		Scan(&effectiveJSON); err != nil {
		return "", domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("load parent Run effective config: %w", err))
	}
	if strings.TrimSpace(effectiveJSON) == "" || strings.TrimSpace(effectiveJSON) == "{}" {
		return "", domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			errors.New("parent Run has no frozen effective config"))
	}
	var effective domain.EffectiveRunConfig
	if err := json.Unmarshal([]byte(effectiveJSON), &effective); err != nil {
		return "", domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("decode parent Run effective config: %w", err))
	}
	var toolPolicy domain.ToolPolicyConfig
	if err := json.Unmarshal(effective.ToolPolicy.Config, &toolPolicy); err != nil {
		return "", domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("decode parent Run tool policy: %w", err))
	}
	return domain.PermissionMode(toolPolicy.Mode), nil
}

func freezeFileDelegationPolicyTx(ctx context.Context, tx *sql.Tx, policies *fileconfig.PolicyStore, runID string) (*domain.DelegationPolicySnapshot, error) {
	policy, err := policies.Resolve(ctx, "", domain.PolicyKind("delegation"))
	if err != nil {
		return nil, err
	}
	var config delegationPolicyConfig
	if err := json.Unmarshal(policy.Config, &config); err != nil {
		return nil, err
	}
	snapshot := &domain.DelegationPolicySnapshot{
		ID: policy.ID, Version: policy.Version, MaxConcurrentChildren: config.MaxConcurrentChildren,
		Budget: config.Budget,
	}
	digest, err := digestDelegationPolicy(snapshot)
	if err != nil {
		return nil, err
	}
	snapshot.Digest = digest
	snapshotJSON, _ := json.Marshal(snapshot)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO delegation_root_budgets
		(root_run_id,policy_snapshot_json,policy_snapshot_digest,max_model_calls,max_tool_calls,
		 max_total_tokens,max_output_tokens,max_cost_usd_micros,max_concurrent_children,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, runID, string(snapshotJSON), digest,
		config.Budget.MaxModelCalls, config.Budget.MaxToolCalls, config.Budget.MaxTotalTokens,
		config.Budget.MaxOutputTokens, config.Budget.MaxCostMicros, config.MaxConcurrentChildren, now, now)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}
