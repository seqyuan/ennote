package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A direct Role invocation freezes the Role definition, so it must also freeze
// the content digest of every preload Skill. Order is the published order and
// 'available' Skills are not part of the prompt, so they are not pinned.
func TestRoleConfigFreezePinsPreloadSkillsByContentDigest(t *testing.T) {
	runs, db, session, role, version, _ := setupPublishedRoleInvocationWithSkills(t,
		[]rolesource.SkillBinding{
			{ID: "role-skill-a", Mode: domain.RoleSkillPreload},
			{ID: "role-skill-b", Mode: domain.RoleSkillPreload},
			{ID: "available-skill", Mode: domain.RoleSkillAvailable},
		})
	runs.SkillDigests = map[string]string{
		"role-skill-a":    "sha256:aaa",
		"role-skill-b":    "sha256:bbb",
		"available-skill": "sha256:ccc",
	}
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "role-preload-pin", role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Role)
	assert.Equal(t, domain.FrozenSkillRefs{
		{ID: "role-skill-a", Digest: "sha256:aaa"},
		{ID: "role-skill-b", Digest: "sha256:bbb"},
	}, resolved.Effective.Role.FrozenSkills, "preload order is preserved and available entries are excluded")

	var effective string
	require.NoError(t, db.QueryRow(`SELECT effective_config_json FROM agent_runs WHERE id=?`, claimed.ID).Scan(&effective))
	assert.Contains(t, effective, `"frozenSkills"`)
	assert.Contains(t, effective, `"role-skill-a"`)
	assert.Contains(t, effective, `"sha256:bbb"`)
	assert.NotContains(t, effective, `"sha256:ccc"`, "available Skills are not pinned")
}

// A nil digest catalog is the legacy/fixture path: preload refs stay unpinned
// so old behavior (and old snapshots) keep working.
func TestRoleConfigFreezeLeavesPreloadSkillsUnpinnedWithoutCatalog(t *testing.T) {
	runs, _, session, role, version, _ := setupPublishedRoleInvocationWithSkills(t,
		[]rolesource.SkillBinding{{ID: "role-skill-a", Mode: domain.RoleSkillPreload}})
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "role-preload-legacy", role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Role)
	assert.Empty(t, resolved.Effective.Role.FrozenSkills)
	require.Len(t, resolved.Effective.Role.Skills.Entries, 1)
	assert.Equal(t, "role-skill-a", resolved.Effective.Role.Skills.Entries[0].SkillID)
}

// With a catalog present, an unnameable preload Skill must not freeze: an
// unpinned published Role prompt is exactly the silent-change hole this pins.
func TestRoleConfigFreezeFailsWhenPreloadSkillIsNotInCatalog(t *testing.T) {
	runs, _, session, role, version, _ := setupPublishedRoleInvocationWithSkills(t,
		[]rolesource.SkillBinding{{ID: "missing-skill", Mode: domain.RoleSkillPreload}})
	runs.SkillDigests = map[string]string{"another-skill": "sha256:aaa"}
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "role-preload-missing", role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	_, err = runs.ResolveAndFreezeConfig(ctx, claimed)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderConfigurationInvalid, domain.ErrorCodeOf(err))
	assert.Contains(t, err.Error(), "missing-skill")
	assert.Contains(t, err.Error(), "security-reviewer")
}

// An entry with an empty digest is just as unpinnable as a missing one.
func TestRoleConfigFreezeFailsWhenPreloadSkillHasNoDigest(t *testing.T) {
	runs, _, session, role, version, _ := setupPublishedRoleInvocationWithSkills(t,
		[]rolesource.SkillBinding{{ID: "role-skill-a", Mode: domain.RoleSkillPreload}})
	runs.SkillDigests = map[string]string{"role-skill-a": "   "}
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "role-preload-empty-digest", role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	_, err = runs.ResolveAndFreezeConfig(ctx, claimed)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderConfigurationInvalid, domain.ErrorCodeOf(err))
	assert.Contains(t, err.Error(), "role-skill-a")
	assert.Contains(t, err.Error(), "no content digest")
}

// The delegated-child freeze path reads the Role definition from the frozen
// delegation meta, not from global SQL, so it must pin preloads there too.
func TestDelegatedChildConfigFreezePinsPreloadSkills(t *testing.T) {
	fixture := newFileRunFixture(t, "child-preload-pin")
	fixture.Runs.SkillDigests = map[string]string{
		"preload-a": "sha256:aaa",
		"preload-b": "sha256:bbb",
	}
	claimed := claimDelegatedChildWithRoleSkills(t, fixture, "child-preload-pin", []domain.RoleSkillEntry{
		{SkillID: "preload-a", Mode: domain.RoleSkillPreload},
		{SkillID: "available-x", Mode: domain.RoleSkillAvailable},
		{SkillID: "preload-b", Mode: domain.RoleSkillPreload},
	})

	resolved, err := fixture.Runs.ResolveAndFreezeConfig(context.Background(), claimed)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Role)
	assert.Equal(t, domain.FrozenSkillRefs{
		{ID: "preload-a", Digest: "sha256:aaa"},
		{ID: "preload-b", Digest: "sha256:bbb"},
	}, resolved.Effective.Role.FrozenSkills)
}

func TestDelegatedChildConfigFreezeFailsWhenPreloadSkillIsNotInCatalog(t *testing.T) {
	fixture := newFileRunFixture(t, "child-preload-missing")
	fixture.Runs.SkillDigests = map[string]string{"preload-a": "sha256:aaa"}
	claimed := claimDelegatedChildWithRoleSkills(t, fixture, "child-preload-missing", []domain.RoleSkillEntry{
		{SkillID: "preload-a", Mode: domain.RoleSkillPreload},
		{SkillID: "missing-child-skill", Mode: domain.RoleSkillPreload},
	})

	_, err := fixture.Runs.ResolveAndFreezeConfig(context.Background(), claimed)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderConfigurationInvalid, domain.ErrorCodeOf(err))
	assert.Contains(t, err.Error(), "missing-child-skill")
	assert.Contains(t, err.Error(), "workspace-explorer")
}

// claimDelegatedChildWithRoleSkills creates a parent Run, a delegation item
// whose frozen meta carries the supplied Role Skills, and returns the claimed
// child Run ready for config resolution.
func claimDelegatedChildWithRoleSkills(t *testing.T, fixture *fileRunFixture, requestID string,
	entries []domain.RoleSkillEntry) *domain.AgentRun {
	t.Helper()
	ctx := context.Background()
	submission, err := fixture.Runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: fixture.SessionID, ClientRequestID: requestID, Text: "run",
	})
	require.NoError(t, err)
	_, err = fixture.Runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = fixture.Runs.ResolveAndFreezeConfig(ctx, &domain.AgentRun{
		ID: submission.Run.ID, TurnID: submission.TurnID, SessionID: submission.Run.SessionID,
		RunKind: domain.RunKindAgent, CommitFormatVersion: domain.CommitFormatLegacyV1,
	})
	require.NoError(t, err)

	delegations := fixture.Delegations()
	item := explorerItem()
	item.RoleMeta.Definition.Skills = domain.RoleSkills{Entries: entries}
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-" + requestID,
		Strategy: domain.DelegationStrategySingle,
		Items:    []store.CreateDelegationItemInput{item},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: submission.Run.ID, ItemID: items[0].ID, SessionID: submission.Run.SessionID,
	})
	require.NoError(t, err)
	claimed, err := fixture.Runs.Claim(ctx, child.ID)
	require.NoError(t, err)
	return claimed
}

// writeCatalogSkill materializes one Skill directory under skillsDir and
// returns the loader content digest the catalog must report for it.
func writeCatalogSkill(t *testing.T, skillsDir, id, body string) string {
	t.Helper()
	dir := filepath.Join(skillsDir, id)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
		[]byte(`{"id":"`+id+`","version":"1","prompt":"SKILL.md"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600))
	loaded, err := skills.Load(dir)
	require.NoError(t, err)
	require.NotEmpty(t, loaded.ContentHash)
	return loaded.ContentHash
}

// freezeRoleInvocation submits, claims, and freezes one direct Role invocation,
// returning the resolved config and the claimed Run.
func freezeRoleInvocation(t *testing.T, runs *store.RunRepo, sessionID, requestID string,
	role domain.RoleIdentity, version domain.RoleVersion) (*store.ResolvedRunConfig, *domain.AgentRun, error) {
	t.Helper()
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(sessionID, requestID, role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	return resolved, claimed, err
}

// A live SkillCatalog resolver must be read at freeze time, not when the RunRepo
// fixture is constructed: a preload Skill created after the fixture exists pins
// correctly, and a later edit pins the new digest on the next Run.
func TestRoleConfigFreezeResolvesPreloadSkillsFromLiveCatalog(t *testing.T) {
	skillsDir := t.TempDir()
	runs, _, session, role, version, _ := setupPublishedRoleInvocationWithSkills(t,
		[]rolesource.SkillBinding{{ID: "role-skill-a", Mode: domain.RoleSkillPreload}})
	runs.SkillCatalog = func() map[string]string {
		return skills.DigestCatalog(skillsDir)
	}

	// The Skill does not exist yet, so the live catalog cannot pin it: the
	// freeze must fail loudly rather than publish an unpinned prompt.
	_, stale, err := freezeRoleInvocation(t, runs, session.ID, "role-live-missing", role, version)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderConfigurationInvalid, domain.ErrorCodeOf(err))
	assert.Contains(t, err.Error(), "role-skill-a")
	// The failed freeze left no usable config, but its Run still owns the
	// Session's single active slot; retire it before the next freeze.
	require.NoError(t, runs.Fail(context.Background(), stale.ID, "qualification", "missing preload skill"))

	// Create the preload Skill *after* the fixture exists.
	firstDigest := writeCatalogSkill(t, skillsDir, "role-skill-a", "ROLE_SKILL_BODY_V1")
	resolved, first, err := freezeRoleInvocation(t, runs, session.ID, "role-live-first", role, version)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Role)
	assert.Equal(t, domain.FrozenSkillRefs{{ID: "role-skill-a", Digest: firstDigest}},
		resolved.Effective.Role.FrozenSkills)

	// A Session admits one active Run at a time; retire it so the next freeze
	// runs against the edited Skill.
	require.NoError(t, runs.Fail(context.Background(), first.ID, "qualification", "freeze captured"))

	// Edit the Skill and freeze another Run: the new digest, not the first one.
	secondDigest := writeCatalogSkill(t, skillsDir, "role-skill-a", "ROLE_SKILL_BODY_V2")
	require.NotEqual(t, firstDigest, secondDigest)
	resolved, _, err = freezeRoleInvocation(t, runs, session.ID, "role-live-second", role, version)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Role)
	assert.Equal(t, domain.FrozenSkillRefs{{ID: "role-skill-a", Digest: secondDigest}},
		resolved.Effective.Role.FrozenSkills)
}
