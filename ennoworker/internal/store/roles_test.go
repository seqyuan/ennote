package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRoleRepo(t *testing.T) (*store.RoleRepo, string, string) {
	t.Helper()
	db := store.SetupDB(t)
	ctx := context.Background()
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "Roles", HostPath: t.TempDir()})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test", CredentialRef: "env:ROLE_TEST_KEY",
	})
	require.NoError(t, err)
	model, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "role-model", ContextWindow: 32000, MaxOutputTokens: 2048,
		SupportsToolUse: true, SupportsThinking: true,
		ThinkingDialect: domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{
			domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium,
		},
	})
	require.NoError(t, err)
	return &store.RoleRepo{DB: db, KnownTools: map[string]bool{"read": true, "grep": true}}, project.ID, model.ID
}

func validRoleDefinition(modelID string) domain.RoleDefinition {
	return domain.RoleDefinition{
		SchemaVersion: 1,
		RolePrompt:    "Review the supplied evidence independently.",
		ModelBinding: domain.RoleModelBinding{
			Mode: domain.RoleModelFixed, ModelProfileID: modelID, ThinkingEffort: domain.ThinkingMedium,
			FallbackModelProfileIDs: []string{}, OverridableFields: []string{},
		},
		Skills:            domain.RoleSkills{Entries: []domain.RoleSkillEntry{}},
		Authority:         domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss,
		AllowedTools:      []string{"read", "grep"},
		ContextPolicy: domain.RoleContextPolicy{
			DefaultMode: domain.RoleContextRoom, AllowedModes: []domain.RoleContextMode{domain.RoleContextRoom, domain.RoleContextReply, domain.RoleContextFresh},
			OwnExecutionContinuity: domain.RoleContinuityNone,
		},
		DelegationPolicy: domain.RoleDelegationPolicy{
			Admission: domain.DelegationApprovalRequired, AllowedCallerKinds: []string{"host"},
			AllowedStrategies: []string{"single"}, MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000},
		},
		OutputContract: "text-v1", MaxLoopIterations: 8,
	}
}

func TestRoleDraftPublishCreatesImmutableVersions(t *testing.T) {
	repo, projectID, modelID := setupRoleRepo(t)
	ctx := context.Background()
	role, err := repo.Create(ctx, store.CreateRoleInput{
		Handle: "security-reviewer", Name: "Security Reviewer", Description: "Independent review",
		Positioning: "Use after trust-boundary changes.", Icon: "shield-check", Color: "red",
		Scope: domain.RoleScopeProject, ProjectID: &projectID, Definition: validRoleDefinition(modelID),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, role.DraftRevision)
	assert.Nil(t, role.CurrentVersionID)

	validation, err := repo.Validate(ctx, role.ID)
	require.NoError(t, err)
	assert.True(t, validation.Valid, validation.Diagnostics)
	assert.Len(t, validation.ConfigDigest, 71)
	version1, err := repo.Publish(ctx, role.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, version1.Version)
	assert.Equal(t, validation.ConfigDigest, version1.ConfigDigest)

	definition2 := validRoleDefinition(modelID)
	definition2.RolePrompt = "Review authorization and credential boundaries."
	updated, err := repo.UpdateDraft(ctx, role.ID, store.UpdateRoleDraftInput{
		ExpectedRevision: 0, Definition: definition2,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, updated.DraftRevision)
	_, err = repo.UpdateDraft(ctx, role.ID, store.UpdateRoleDraftInput{ExpectedRevision: 0, Definition: definition2})
	assert.ErrorIs(t, err, store.ErrRoleDraftConflict)
	version2, err := repo.Publish(ctx, role.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, version2.Version)

	storedV1, err := repo.GetVersion(ctx, role.ID, version1.ID)
	require.NoError(t, err)
	assert.Equal(t, "Review the supplied evidence independently.", storedV1.Definition.RolePrompt)
	versions, err := repo.ListVersions(ctx, role.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, 2, versions[0].Version)
	require.NoError(t, repo.Archive(ctx, role.ID))
	versions, err = repo.ListVersions(ctx, role.ID)
	require.NoError(t, err)
	assert.Len(t, versions, 2)
}

func TestRoleValidationFailsClosedOnCapabilitiesAndReferences(t *testing.T) {
	repo, projectID, modelID := setupRoleRepo(t)
	ctx := context.Background()
	definition := validRoleDefinition(modelID)
	definition.ModelBinding.ThinkingEffort = domain.ThinkingHigh
	definition.AllowedTools = append(definition.AllowedTools, "missing-tool")
	definition.Skills.Entries = []domain.RoleSkillEntry{{SkillID: "missing-skill", Mode: domain.RoleSkillPreload}}
	role, err := repo.Create(ctx, store.CreateRoleInput{
		Handle: "invalid-role", Name: "Invalid", Scope: domain.RoleScopeProject,
		ProjectID: &projectID, Definition: definition,
	})
	require.NoError(t, err)
	validation, err := repo.Validate(ctx, role.ID)
	require.NoError(t, err)
	assert.False(t, validation.Valid)
	codes := make([]string, len(validation.Diagnostics))
	for index := range validation.Diagnostics {
		codes[index] = validation.Diagnostics[index].Code
	}
	assert.Contains(t, codes, "thinking_effort_unsupported")
	assert.Contains(t, codes, "tool_not_found")
	assert.Contains(t, codes, "skill_catalog_unavailable")
	_, err = repo.Publish(ctx, role.ID, 0)
	assert.ErrorIs(t, err, store.ErrRoleValidation)
}

func TestRoleCatalogSearchAndScopeFilters(t *testing.T) {
	repo, projectID, modelID := setupRoleRepo(t)
	ctx := context.Background()
	for _, handle := range []string{"alpha-reviewer", "beta-auditor"} {
		_, err := repo.Create(ctx, store.CreateRoleInput{Handle: handle, Name: handle,
			Positioning: "Inspect boundaries.", Scope: domain.RoleScopeProject, ProjectID: &projectID,
			Definition: validRoleDefinition(modelID)})
		require.NoError(t, err)
	}
	search, err := repo.List(ctx, store.ListRolesInput{ProjectID: &projectID, Status: "active", Query: "beta", Limit: 20})
	require.NoError(t, err)
	require.Len(t, search, 1)
	assert.Equal(t, "beta-auditor", search[0].Handle)
	positioning, err := repo.List(ctx, store.ListRolesInput{ProjectID: &projectID, Status: "active", Query: "boundaries", Limit: 20})
	require.NoError(t, err)
	require.Len(t, positioning, 2)
	scopeFiltered, err := repo.List(ctx, store.ListRolesInput{Status: "active", Scope: domain.RoleScopeProject, Limit: 20})
	require.NoError(t, err)
	assert.Empty(t, scopeFiltered, "project-scoped Roles require a matching projectId")
}

func TestDelegationRoleResolutionIsProjectScopedAndDeterministic(t *testing.T) {
	repo, projectID, modelID := setupRoleRepo(t)
	ctx := context.Background()
	globalDefinition := validRoleDefinition(modelID)
	globalDefinition.DelegationPolicy.Admission = domain.DelegationAutoWithinBudget
	globalRole, err := repo.Create(ctx, store.CreateRoleInput{Handle: "shared-reviewer", Name: "Global",
		Scope: domain.RoleScopeGlobal, Definition: globalDefinition})
	require.NoError(t, err)
	globalVersion, err := repo.Publish(ctx, globalRole.ID, 0)
	require.NoError(t, err)
	projectRole, err := repo.Create(ctx, store.CreateRoleInput{Handle: "shared-reviewer", Name: "Project",
		Scope: domain.RoleScopeProject, ProjectID: &projectID, Definition: globalDefinition})
	require.NoError(t, err)
	projectVersion, err := repo.Publish(ctx, projectRole.ID, 0)
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: repo.DB}).Create(ctx, domain.CreateSessionInput{ProjectID: projectID})
	require.NoError(t, err)

	resolved, err := (&store.DelegationRepo{DB: repo.DB}).ResolveRoleForDelegation(ctx, session.ID, "shared-reviewer")
	require.NoError(t, err)
	assert.Equal(t, projectVersion.ID, resolved.VersionID)
	assert.NotEqual(t, globalVersion.ID, resolved.VersionID)

	otherProject, _, err := (&store.ProjectRepo{DB: repo.DB}).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "Other", HostPath: t.TempDir()})
	require.NoError(t, err)
	isolated, err := repo.Create(ctx, store.CreateRoleInput{Handle: "isolated-reviewer", Name: "Isolated",
		Scope: domain.RoleScopeProject, ProjectID: &otherProject.ID, Definition: globalDefinition})
	require.NoError(t, err)
	_, err = repo.Publish(ctx, isolated.ID, 0)
	require.NoError(t, err)
	_, err = (&store.DelegationRepo{DB: repo.DB}).ResolveRoleForDelegation(ctx, session.ID, "isolated-reviewer")
	assert.ErrorIs(t, err, store.ErrDelegationRoleUnavailable)
}

func TestDelegationCreationEnforcesAdmissionKillSwitchAndBudget(t *testing.T) {
	repo, projectID, modelID := setupRoleRepo(t)
	ctx := context.Background()
	definition := validRoleDefinition(modelID)
	role, err := repo.Create(ctx, store.CreateRoleInput{Handle: "approval-reviewer", Name: "Approval",
		Scope: domain.RoleScopeProject, ProjectID: &projectID, Definition: definition})
	require.NoError(t, err)
	version, err := repo.Publish(ctx, role.ID, 0)
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: repo.DB}).Create(ctx, domain.CreateSessionInput{ProjectID: projectID})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: repo.DB}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID,
		ClientRequestID: "role-policy", Text: "delegate"})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	delegations := &store.DelegationRepo{DB: repo.DB}
	item := store.CreateDelegationItemInput{Name: "review", RoleVersionID: version.ID,
		AssignmentJSON: []byte(`{"task":"review"}`), OutputContract: "text-v1",
		Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
			MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000}}

	_, err = delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{ParentRunID: submission.Run.ID,
		ParentToolCallID: "not-approved", Strategy: domain.DelegationStrategySingle, Items: []store.CreateDelegationItemInput{item}})
	assert.ErrorIs(t, err, store.ErrDelegationNotAuthorized)
	tooLarge := item
	tooLarge.Budget.MaxModelCalls = 5
	_, err = delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{ParentRunID: submission.Run.ID,
		ParentToolCallID: "over-budget", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{tooLarge}, AdmissionApproved: true})
	assert.ErrorIs(t, err, store.ErrDelegationNotAuthorized)
	_, err = repo.DB.Exec(`UPDATE agent_profiles SET delegation_enabled=0 WHERE id=?`, role.ID)
	require.NoError(t, err)
	_, err = delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{ParentRunID: submission.Run.ID,
		ParentToolCallID: "disabled", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{item}, AdmissionApproved: true})
	assert.ErrorIs(t, err, store.ErrDelegationNotAuthorized)
}

func TestRoleCatalogExcludesLegacyHostProfiles(t *testing.T) {
	repo, projectID, modelID := setupRoleRepo(t)
	ctx := context.Background()
	_, err := repo.DB.Exec(`INSERT INTO agent_profiles(id,name,created_at,updated_at) VALUES('legacy','Legacy','2026-08-03T00:00:00Z','2026-08-03T00:00:00Z')`)
	require.NoError(t, err)
	created, err := repo.Create(ctx, store.CreateRoleInput{
		Handle: "catalog-role", Name: "Catalog Role", Scope: domain.RoleScopeProject,
		ProjectID: &projectID, Definition: validRoleDefinition(modelID),
	})
	require.NoError(t, err)
	items, err := repo.List(ctx, store.ListRolesInput{ProjectID: &projectID, Status: "active", Limit: 20})
	require.NoError(t, err)
	require.Len(t, items, 2)
	ids := make([]string, len(items))
	for index := range items {
		ids[index] = items[index].ID
	}
	assert.Contains(t, ids, created.ID)
	assert.Contains(t, ids, "builtin-workspace-explorer")
}

func TestRoleWithSkillsPublishesWhenKnownCatalogIsWired(t *testing.T) {
	repo, projectID, modelID := setupRoleRepo(t)
	repo.KnownSkills = map[string]bool{"review-guard": true}
	ctx := context.Background()
	definition := validRoleDefinition(modelID)
	definition.Skills.Entries = []domain.RoleSkillEntry{{SkillID: "review-guard", Mode: domain.RoleSkillPreload}}
	role, err := repo.Create(ctx, store.CreateRoleInput{Handle: "skilled-role", Name: "Skilled Role",
		Scope: domain.RoleScopeProject, ProjectID: &projectID, Definition: definition})
	require.NoError(t, err)
	validation, err := repo.Validate(ctx, role.ID)
	require.NoError(t, err)
	assert.True(t, validation.Valid, validation.Diagnostics)
	version, err := repo.Publish(ctx, role.ID, role.DraftRevision)
	require.NoError(t, err)
	assert.Equal(t, 1, version.Version)
	stored, err := repo.GetVersion(ctx, role.ID, version.ID)
	require.NoError(t, err)
	assert.Equal(t, "review-guard", stored.Definition.Skills.Entries[0].SkillID)
}
