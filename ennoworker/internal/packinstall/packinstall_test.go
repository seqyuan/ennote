package packinstall_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/packinstall"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const roleOnlyManifest = `schema_version: 1
id: seqyuan/report-writer
name: Report Writer
version: 1.0.0
roles:
  - handle: report-writer
    path: roles/report-writer/role.md
    scope: global
`

const graphPackManifest = `schema_version: 1
id: seqyuan/cell-qc-review
name: Cell QC Review
version: 1.3.0
description: Multi-sample scRNA QC.
model_requirements:
  defaults:
    capabilities: [tool_use]
    min_context_window: 32000
flows:
  - id: qc-review
    path: flows/qc-review/graph.yaml
    format: graph
    roles: [qc-reviewer]
roles:
  - handle: qc-reviewer
    path: roles/qc-reviewer/role.md
    scope: flow
  - handle: report-writer
    path: roles/report-writer/role.md
    scope: global
skills:
  - id: cell-qc-tables
    path: skills/cell-qc-tables/
    scope: flow
  - id: shared-notes
    path: skills/shared-notes/
    scope: global
docs:
  readme: README.md
`

const graphFlowSource = `schema_version: 1
id: qc-review
name: QC Review
tasks:
  load:
    name: Load
    model: provider/pack-model
    goal: Load the samplesheet
    skills: [local/cell-qc-tables]
  review:
    name: Review
    role: local/qc-reviewer
    goal: Review QC metrics
    writes: [results/qc.csv]
graph:
  load: []
  review: [load]
`

const flowScopedSkillJSON = `{"id":"cell-qc-tables","prompt":"SKILL.md","allowedTools":["read"]}`

const flowScopedSkillMD = `---
name: Cell QC Tables
description: Build QC tables
---

Summarize the QC metrics table by table.
`

const globalSkillJSON = `{"id":"shared-notes","prompt":"SKILL.md"}`

const globalSkillMD = `---
name: Shared Notes
description: Shared notes
---

Record shared notes.
`

func roleOnlyPack(t *testing.T, f *fixture) string {
	t.Helper()
	return writePack(t, []packFile{
		{path: "pack.yaml", body: roleOnlyManifest},
		{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer")},
	})
}

// mixedPack ships a graph flow, a Graph-private Role, a Graph-private Skill, and
// a Worker-global Role plus a global Skill.
func mixedPack(t *testing.T, f *fixture) string {
	t.Helper()
	flowRole := roleFile(t, f.modelID, "qc-reviewer", "QC Reviewer",
		rolesource.SkillBinding{ID: "global/shared-notes", Mode: domain.RoleSkillPreload})
	return writePack(t, []packFile{
		{path: "pack.yaml", body: graphPackManifest},
		{path: "README.md", body: "# Cell QC Review\n"},
		{path: "flows/qc-review/graph.yaml", body: graphFlowSource},
		{path: "roles/qc-reviewer/role.md", body: flowRole},
		{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer")},
		{path: "skills/cell-qc-tables/skill.json", body: flowScopedSkillJSON},
		{path: "skills/cell-qc-tables/SKILL.md", body: flowScopedSkillMD},
		{path: "skills/shared-notes/skill.json", body: globalSkillJSON},
		{path: "skills/shared-notes/SKILL.md", body: globalSkillMD},
	})
}

func TestPlanRoleOnlyPack(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: roleOnlyPack(t, f)})
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.Equal(t, packinstall.PlanVersion, plan.PlanVersion)
	assert.Equal(t, "seqyuan/report-writer", plan.Pack.ID)
	assert.Equal(t, "1.0.0", plan.Pack.Version)
	assert.True(t, strings.HasPrefix(plan.ManifestDigest, "sha256:"))
	assert.True(t, strings.HasPrefix(plan.PackDigest, "sha256:"))
	assert.Empty(t, plan.Blockers)
	assert.Equal(t, packinstall.CommitPointStateFile, plan.CommitPoint)
	assert.False(t, plan.WillPublishFlowRevision)
	assert.Empty(t, plan.Reinstall)
	assert.Nil(t, plan.State)

	require.Len(t, plan.Actions, 2)
	assert.Equal(t, packinstall.Action{Kind: packinstall.ActionKindRole, StoreID: "report-writer",
		Action: packinstall.ActionCreate, Scope: "global"}, plan.Actions[0])
	assert.Equal(t, packinstall.Action{Kind: packinstall.ActionKindRoleRevision, StoreID: "report-writer",
		Action: packinstall.ActionPublish, Scope: "global"}, plan.Actions[1])
}

func TestPlanGraphPackAndDryRunWritesNothing(t *testing.T) {
	f := newFixture(t)
	source := mixedPack(t, f)
	plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)

	kinds := make([]string, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		kinds = append(kinds, action.Kind+":"+action.StoreID+":"+action.Action)
	}
	assert.Equal(t, []string{
		"graph:qc-review:create",
		"graph_role:qc-review/qc-reviewer:create",
		"role:report-writer:create",
		"role_revision:report-writer:publish",
		"skill:qc-review/cell-qc-tables:create",
		"skill:shared-notes:create",
	}, kinds)

	// A dry run returns the same plan and touches nothing: no staging, no state,
	// no store object.
	dry, err := f.service.Install(context.Background(), packinstall.Request{SourcePath: source, DryRun: true})
	require.NoError(t, err)
	require.Empty(t, dry.Blockers)
	assert.Nil(t, dry.State)
	f.requireNoPackState()
	_, err = os.Lstat(filepath.Join(f.home, "agents"))
	assert.True(t, os.IsNotExist(err), "dry run must not create agents/: %v", err)
	_, err = os.Lstat(f.skillsRoot)
	assert.True(t, os.IsNotExist(err), "dry run must not create the skills root: %v", err)
}

func TestInstallGraphPackWritesDraftAndScopedObjects(t *testing.T) {
	f := newFixture(t)
	source := mixedPack(t, f)
	plan, err := f.service.Install(context.Background(), packinstall.Request{SourcePath: source})
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)
	require.NotNil(t, plan.State)

	// The Graph is a draft: graph.yaml exists and no revision was published.
	document, _, err := f.sources.ReadGraph("qc-review")
	require.NoError(t, err)
	assert.Equal(t, "qc-review", document.ID)
	revisions, err := f.sources.ListGraphRevisions("qc-review")
	require.NoError(t, err)
	assert.Empty(t, revisions)

	// Graph-private Role and Skill.
	roleDoc, _, err := f.sources.ReadGraphRole("qc-review", "qc-reviewer")
	require.NoError(t, err)
	assert.Equal(t, "qc-reviewer", roleDoc.Handle)
	loadedFlowSkill, err := skills.Load(f.graphSkillPath("qc-review", "cell-qc-tables"))
	require.NoError(t, err)
	assert.Equal(t, "cell-qc-tables", loadedFlowSkill.Manifest.ID)
	assert.Equal(t, []string{"read"}, loadedFlowSkill.Manifest.AllowedTools)

	// Global Role with one published revision, and a global Skill under the user
	// skills root.
	globalRole, globalDigest, err := f.sources.ReadRole("report-writer")
	require.NoError(t, err)
	assert.Equal(t, "report-writer", globalRole.Handle)
	roleRevisions, err := f.sources.ListRoleRevisions("report-writer")
	require.NoError(t, err)
	require.Len(t, roleRevisions, 1)
	assert.Equal(t, globalDigest, roleRevisions[0].Digest)
	loadedGlobalSkill, err := skills.Load(f.skillPath("shared-notes"))
	require.NoError(t, err)
	assert.Equal(t, "shared-notes", loadedGlobalSkill.Manifest.ID)
	_, err = os.Lstat(f.graphSkillPath("qc-review", "shared-notes"))
	assert.True(t, os.IsNotExist(err), "a global Skill must not be copied into the Graph")

	// State is the commit point and records provenance.
	stateData, err := os.ReadFile(f.statePath("seqyuan/cell-qc-review"))
	require.NoError(t, err)
	var state packinstall.State
	require.NoError(t, json.Unmarshal(stateData, &state))
	assert.Equal(t, packinstall.StateVersion, state.StateVersion)
	assert.Equal(t, packinstall.InstallStateDraft, state.InstallState)
	assert.Equal(t, "installed_draft", state.InstallState)
	assert.Equal(t, "2026-10-27T12:00:00Z", state.InstalledAt)
	assert.Equal(t, packinstall.SourceRef{Kind: "path", Path: source}, state.Source)
	require.Len(t, state.Flows, 1)
	assert.Equal(t, "qc-review", state.Flows[0].StoreID)
	assert.Nil(t, state.Flows[0].PublishedRevision)
	assert.Equal(t, "graph", state.Flows[0].Format)
	require.Len(t, state.Roles, 2)
	require.Len(t, state.Skills, 2)
	assert.Empty(t, state.ExternalRefs.Skills)
	assert.Empty(t, state.ExternalRefs.MCP)

	manifestCopy, err := os.ReadFile(f.manifestCopyPath("seqyuan/cell-qc-review"))
	require.NoError(t, err)
	original, err := os.ReadFile(filepath.Join(source, "pack.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(original), string(manifestCopy))

	// There is no second installed payload copy.
	_, err = os.Lstat(filepath.Join(f.home, "packs", "installed"))
	assert.True(t, os.IsNotExist(err))
	staging, err := os.ReadDir(filepath.Join(f.home, "packs", "staging"))
	require.NoError(t, err)
	assert.Empty(t, staging)

	// Default disabled: the installed Graph has no revision, so a Run start is
	// refused by the existing graphrun contract.
	session := f.createSession()
	runner := &graphrun.Service{Sources: f.sources, Models: &store.ModelRepo{Files: f.models}, Sessions: f.sessions}
	_, err = runner.Start(context.Background(), session.ProjectID, "qc-review", 0, session.ID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no published revision")
}

func TestInstallReinstallIsNoop(t *testing.T) {
	f := newFixture(t)
	source := mixedPack(t, f)
	_, err := f.service.Install(context.Background(), packinstall.Request{SourcePath: source})
	require.NoError(t, err)

	plan, err := f.service.Install(context.Background(), packinstall.Request{SourcePath: source})
	require.NoError(t, err)
	assert.Equal(t, packinstall.ReinstallNoop, plan.Reinstall)
	require.NotNil(t, plan.State)
	for _, action := range plan.Actions {
		assert.Equal(t, packinstall.ActionExistsIdentical, action.Action, action.StoreID)
	}
	// The reinstall published no duplicate revision.
	revisions, err := f.sources.ListRoleRevisions("report-writer")
	require.NoError(t, err)
	assert.Len(t, revisions, 1)
}

func TestInstallRollsBackWhenTheCommitPointFails(t *testing.T) {
	f := newFixture(t)
	// Occupy the manifest-copy path with a directory: the copy write fails after
	// every store object was written, which exercises the rollback path.
	manifestCopy := f.manifestCopyPath("seqyuan/cell-qc-review")
	require.NoError(t, os.MkdirAll(filepath.Join(manifestCopy, "blocker"), 0o700))
	// An unrelated user Graph must survive the rollback untouched.
	_, _, err := f.sources.CreateGraph("user-graph", "User Graph")
	require.NoError(t, err)

	_, err = f.service.Install(context.Background(), packinstall.Request{SourcePath: mixedPack(t, f)})
	require.Error(t, err)

	for _, path := range []string{
		f.graphPath("qc-review"),
		f.rolePath("report-writer"),
		f.skillPath("shared-notes"),
		filepath.Join(f.home, "agents", "graphs", "qc-review"),
	} {
		_, statErr := os.Lstat(path)
		assert.True(t, os.IsNotExist(statErr), "rollback must remove %s", path)
	}
	_, err = os.Lstat(f.statePath("seqyuan/cell-qc-review"))
	assert.True(t, os.IsNotExist(err))
	staging, err := os.ReadDir(filepath.Join(f.home, "packs", "staging"))
	require.NoError(t, err)
	assert.Empty(t, staging)
	_, _, err = f.sources.ReadGraph("user-graph")
	require.NoError(t, err)
}

func TestPlanBlockers(t *testing.T) {
	const agentFlowSource = `schemaVersion: 1
id: qc-review
version: 1
budget:
  max_total_tokens: 20000
tasks:
  load:
    role: qc-reviewer
    goal: Load the samplesheet
  gate:
    type: check
    command: "test -s results/qc.csv"
    depends: [load]
  review:
    role: qc-reviewer
    goal: Review QC metrics
    depends: [gate]
    fan_out: {min: 1, max: 4}
`
	flowFormatManifest := `schema_version: 1
id: seqyuan/qc-flow
name: QC Flow
version: 1.0.0
flows:
  - id: qc-review
    path: flows/qc-review/flow.yaml
    format: flow
roles:
  - handle: qc-reviewer
    path: roles/qc-reviewer/role.md
    scope: global
`
	t.Run("flow format installs a draft", func(t *testing.T) {
		f := newFixture(t)
		f.service.CheckAllowlistAllowsAll = true
		source := writePack(t, []packFile{
			{path: "pack.yaml", body: flowFormatManifest},
			{path: "flows/qc-review/flow.yaml", body: agentFlowSource},
			{path: "roles/qc-reviewer/role.md", body: roleFile(t, f.modelID, "qc-reviewer", "QC Reviewer")},
		})
		plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
		require.NoError(t, err)
		require.Empty(t, plan.Blockers, blockerCodes(plan))
		flowAction, ok := findAction(plan, packinstall.ActionKindFlow, "qc-review")
		require.True(t, ok, plan.Actions)
		assert.Equal(t, packinstall.ActionCreate, flowAction.Action)
		assert.False(t, plan.WillPublishFlowRevision)

		source = writePack(t, []packFile{
			{path: "pack.yaml", body: flowFormatManifest},
			{path: "flows/qc-review/flow.yaml", body: agentFlowSource},
			{path: "roles/qc-reviewer/role.md", body: roleFile(t, f.modelID, "qc-reviewer", "QC Reviewer")},
		})
		installed, err := f.service.Install(context.Background(), packinstall.Request{SourcePath: source})
		require.NoError(t, err)
		require.NotNil(t, installed.State)
		require.Len(t, installed.State.Flows, 1)
		assert.Equal(t, "flow", installed.State.Flows[0].Format)
		assert.Nil(t, installed.State.Flows[0].PublishedRevision)
		_, _, err = f.sources.ReadFlow("qc-review")
		require.NoError(t, err)
		revisions, err := f.sources.ListFlowRevisions("qc-review")
		require.NoError(t, err)
		assert.Empty(t, revisions)
	})

	t.Run("mcp and dependency", func(t *testing.T) {
		f := newFixture(t)
		manifest := roleOnlyManifest + "mcp:\n  - ref: pubmed@^1\nrequires:\n  - seqyuan/other-pack@1.0.0\n"
		source := writePack(t, []packFile{
			{path: "pack.yaml", body: manifest},
			{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer")},
		})
		plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
		require.NoError(t, err)
		assert.True(t, hasBlocker(plan, packinstall.CodeMCPUnsupported), blockerCodes(plan))
		assert.True(t, hasBlocker(plan, packinstall.CodeDependencyUnsupported), blockerCodes(plan))
	})

	t.Run("model unresolved", func(t *testing.T) {
		f := newFixture(t)
		document := roleDocument(f.modelID, "report-writer", "Report Writer")
		document.Model.Ref = "provider/ghost-model"
		encoded, err := rolesource.Encode(document)
		require.NoError(t, err)
		source := writePack(t, []packFile{
			{path: "pack.yaml", body: roleOnlyManifest},
			{path: "roles/report-writer/role.md", body: string(encoded)},
		})
		plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
		require.NoError(t, err)
		require.True(t, hasBlocker(plan, packinstall.CodeModelUnresolved), blockerCodes(plan))
		assert.Contains(t, plan.Blockers[0].Message, "provider/ghost-model")
	})

	t.Run("external skill unresolved", func(t *testing.T) {
		f := newFixture(t)
		manifest := strings.Replace(roleOnlyManifest, "roles:", "skills:\n  - ref: bio-workspace@0.1.0\nroles:", 1)
		source := writePack(t, []packFile{
			{path: "pack.yaml", body: manifest},
			{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer")},
		})
		plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
		require.NoError(t, err)
		require.True(t, hasBlocker(plan, packinstall.CodeSkillUnresolved), blockerCodes(plan))
		assert.Contains(t, plan.Blockers[0].Message, "bio-workspace@0.1.0")

		// Once the external Skill exists in the user catalog the ref resolves and
		// the pack plans cleanly.
		f.installExternalSkill("bio-workspace")
		plan, err = f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
		require.NoError(t, err)
		require.Empty(t, plan.Blockers)
	})

	t.Run("role preload local skill is rejected", func(t *testing.T) {
		f := newFixture(t)
		source := writePack(t, []packFile{
			{path: "pack.yaml", body: roleOnlyManifest},
			{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer",
				rolesource.SkillBinding{ID: "local/cell-qc-tables", Mode: domain.RoleSkillPreload})},
		})
		plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
		require.NoError(t, err)
		require.True(t, hasBlocker(plan, packinstall.CodeSkillUnresolved), blockerCodes(plan))
		assert.Contains(t, plan.Blockers[0].Message, "local/cell-qc-tables")
	})

	t.Run("role preload missing global skill is rejected", func(t *testing.T) {
		f := newFixture(t)
		source := writePack(t, []packFile{
			{path: "pack.yaml", body: roleOnlyManifest},
			{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer",
				rolesource.SkillBinding{ID: "global/shared-notes", Mode: domain.RoleSkillPreload})},
		})
		plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
		require.NoError(t, err)
		require.True(t, hasBlocker(plan, packinstall.CodeSkillUnresolved), blockerCodes(plan))
		assert.Contains(t, plan.Blockers[0].Message, "shared-notes")
	})

	t.Run("skill not prompt only", func(t *testing.T) {
		f := newFixture(t)
		manifest := roleOnlyManifest + "skills:\n  - id: scripted\n    path: skills/scripted/\n"
		source := writePack(t, []packFile{
			{path: "pack.yaml", body: manifest},
			{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer")},
			{path: "skills/scripted/skill.json", body: `{"id":"scripted","prompt":"SKILL.md"}`},
			{path: "skills/scripted/SKILL.md", body: "Run the script.\n"},
			{path: "skills/scripted/scripts/run.sh", body: "echo hi\n"},
		})
		plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
		require.NoError(t, err)
		require.True(t, hasBlocker(plan, packinstall.CodeSkillNotPromptOnly), blockerCodes(plan))
		assert.Contains(t, plan.Blockers[0].Message, "scripts/run.sh")
	})
}

func TestPlanConflictsAndVersionRules(t *testing.T) {
	f := newFixture(t)

	// An existing Graph with the same id but different bytes is a conflict.
	_, _, err := f.sources.CreateGraph("qc-review", "Something Else")
	require.NoError(t, err)
	plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: mixedPack(t, f)})
	require.NoError(t, err)
	graphAction, ok := findAction(plan, packinstall.ActionKindGraph, "qc-review")
	require.True(t, ok)
	assert.Equal(t, packinstall.ActionConflict, graphAction.Action)
	assert.True(t, hasBlocker(plan, packinstall.CodeFlowIDConflict), blockerCodes(plan))
	_, err = f.service.Install(context.Background(), packinstall.Request{SourcePath: mixedPack(t, f)})
	require.Error(t, err)
	assert.Equal(t, packinstall.CodeFlowIDConflict, packinstall.Code(err))

	// Fresh home: install, then plan a different version and different bytes.
	f = newFixture(t)
	_, err = f.service.Install(context.Background(), packinstall.Request{SourcePath: mixedPack(t, f)})
	require.NoError(t, err)

	newer := strings.Replace(graphPackManifest, "version: 1.3.0", "version: 1.4.0", 1)
	newerSource := writePack(t, []packFile{
		{path: "pack.yaml", body: newer},
		{path: "README.md", body: "# Cell QC Review\n"},
		{path: "flows/qc-review/graph.yaml", body: graphFlowSource},
		{path: "roles/qc-reviewer/role.md", body: roleFile(t, f.modelID, "qc-reviewer", "QC Reviewer")},
		{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer")},
		{path: "skills/cell-qc-tables/skill.json", body: flowScopedSkillJSON},
		{path: "skills/cell-qc-tables/SKILL.md", body: flowScopedSkillMD},
		{path: "skills/shared-notes/skill.json", body: globalSkillJSON},
		{path: "skills/shared-notes/SKILL.md", body: globalSkillMD},
	})
	plan, err = f.service.Plan(context.Background(), packinstall.Request{SourcePath: newerSource})
	require.NoError(t, err)
	assert.True(t, hasBlocker(plan, packinstall.CodeVersionConflict), blockerCodes(plan))

	// Same version, different bytes (a comment edit changes both digests).
	edited := strings.Replace(graphPackManifest, "description: Multi-sample scRNA QC.", "description: Multi-sample scRNA QC (edited).", 1)
	editedSource := writePack(t, []packFile{
		{path: "pack.yaml", body: edited},
		{path: "README.md", body: "# Cell QC Review\n"},
		{path: "flows/qc-review/graph.yaml", body: graphFlowSource},
		{path: "roles/qc-reviewer/role.md", body: roleFile(t, f.modelID, "qc-reviewer", "QC Reviewer")},
		{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer")},
		{path: "skills/cell-qc-tables/skill.json", body: flowScopedSkillJSON},
		{path: "skills/cell-qc-tables/SKILL.md", body: flowScopedSkillMD},
		{path: "skills/shared-notes/skill.json", body: globalSkillJSON},
		{path: "skills/shared-notes/SKILL.md", body: globalSkillMD},
	})
	plan, err = f.service.Plan(context.Background(), packinstall.Request{SourcePath: editedSource})
	require.NoError(t, err)
	assert.True(t, hasBlocker(plan, packinstall.CodeAlreadyInstalled), blockerCodes(plan))
}

func TestRenamesChangeInstalledStoreIDsOnly(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Install(context.Background(), packinstall.Request{
		SourcePath: mixedPack(t, f),
		Renames: packinstall.Renames{
			Flows: map[string]string{"qc-review": "cell-qc-review"},
		},
	})
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)

	document, _, err := f.sources.ReadGraph("cell-qc-review")
	require.NoError(t, err)
	assert.Equal(t, "cell-qc-review", document.ID)
	// In-pack references are not rewritten: the Graph still points at the pack
	// handle, which is exactly why a rename warns.
	assert.Equal(t, "local/qc-reviewer", document.Tasks["review"].Role)
	roleDoc, _, err := f.sources.ReadGraphRole("cell-qc-review", "qc-reviewer")
	require.NoError(t, err)
	assert.Equal(t, "qc-reviewer", roleDoc.Handle)
	_, _, err = f.sources.ReadRole("report-writer")
	require.NoError(t, err)
	_, err = skills.Load(f.graphSkillPath("cell-qc-review", "cell-qc-tables"))
	require.NoError(t, err)

	renamed := false
	for _, warning := range plan.Warnings {
		if warning.Code == packinstall.WarningRenamedFlow {
			renamed = true
		}
	}
	assert.True(t, renamed, "a flow rename must warn that in-pack references are not rewritten")
	assert.Equal(t, "cell-qc-review", plan.State.Flows[0].StoreID)
}

func TestRoleOnlyPackAllowsRoleRename(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Install(context.Background(), packinstall.Request{
		SourcePath: roleOnlyPack(t, f),
		Renames:    packinstall.Renames{Roles: map[string]string{"report-writer": "cell-report-writer"}},
	})
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)
	_, _, err = f.sources.ReadRole("cell-report-writer")
	require.NoError(t, err)
	_, _, err = f.sources.ReadRole("report-writer")
	assert.Error(t, err)
}

func TestRenamesRejectCollapsingStoreIDs(t *testing.T) {
	f := newFixture(t)
	// Two roles cannot land on one global handle.
	manifest := roleOnlyManifest + `  - handle: report-reviewer
    path: roles/report-reviewer/role.md
    scope: global
`
	source := writePack(t, []packFile{
		{path: "pack.yaml", body: manifest},
		{path: "roles/report-writer/role.md", body: roleFile(t, f.modelID, "report-writer", "Report Writer")},
		{path: "roles/report-reviewer/role.md", body: roleFile(t, f.modelID, "report-reviewer", "Report Reviewer")},
	})
	plan, err := f.service.Plan(context.Background(), packinstall.Request{
		SourcePath: source,
		Renames:    packinstall.Renames{Roles: map[string]string{"report-reviewer": "report-writer"}},
	})
	require.NoError(t, err)
	require.True(t, hasBlocker(plan, packinstall.CodeRoleHandleConflict), blockerCodes(plan))
	assert.Contains(t, plan.Blockers[0].Message, "both install as global Role")
}

func TestRemoveRefusesActiveRunAndEditedFilesThenDeletes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, err := f.service.Install(ctx, packinstall.Request{SourcePath: mixedPack(t, f)})
	require.NoError(t, err)

	// Publish through the ordinary Graph path so a Run can start, then verify
	// the installed Graph is otherwise runnable.
	_, err = f.sources.PublishGraphRevision("qc-review")
	require.NoError(t, err)
	session := f.createSession()
	runner := &graphrun.Service{Sources: f.sources, Models: &store.ModelRepo{Files: f.models}, Sessions: f.sessions}
	run, err := runner.Start(ctx, session.ProjectID, "qc-review", 0, session.ID, nil, nil)
	require.NoError(t, err)

	err = f.service.Remove(ctx, "seqyuan/cell-qc-review", true)
	require.Error(t, err)
	assert.Equal(t, packinstall.CodeInstallBlocked, packinstall.Code(err))
	assert.Contains(t, err.Error(), "non-terminal")

	db, err := f.sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE run_agent_flow SET state='completed' WHERE run_id=?`, run.RunID)
	require.NoError(t, err)

	// A locally edited Graph blocks an unforced remove. Graph identity is the
	// canonical document digest, so the edit must change the document, not just a
	// comment.
	graphData, err := os.ReadFile(f.graphPath("qc-review"))
	require.NoError(t, err)
	editedGraph := strings.Replace(string(graphData), "Review QC metrics", "Review QC metrics and plots", 1)
	require.NotEqual(t, string(graphData), editedGraph)
	require.NoError(t, os.WriteFile(f.graphPath("qc-review"), []byte(editedGraph), 0o600))
	err = f.service.Remove(ctx, "seqyuan/cell-qc-review", false)
	require.Error(t, err)
	assert.Equal(t, packinstall.CodeInstallBlocked, packinstall.Code(err))
	assert.Contains(t, err.Error(), "edited locally")

	require.NoError(t, f.service.Remove(ctx, "seqyuan/cell-qc-review", true))
	for _, path := range []string{
		filepath.Join(f.home, "agents", "graphs", "qc-review"),
		f.rolePath("report-writer"),
		f.skillPath("shared-notes"),
		f.statePath("seqyuan/cell-qc-review"),
	} {
		_, statErr := os.Lstat(path)
		assert.True(t, os.IsNotExist(statErr), "remove must delete %s", path)
	}
	summaries, err := f.service.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestRemoveKeepsLocallyEditedSkill(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, err := f.service.Install(ctx, packinstall.Request{SourcePath: mixedPack(t, f)})
	require.NoError(t, err)
	promptPath := filepath.Join(f.skillPath("shared-notes"), "SKILL.md")
	require.NoError(t, os.WriteFile(promptPath, []byte("Locally rewritten prompt.\n"), 0o600))

	require.NoError(t, f.service.Remove(ctx, "seqyuan/cell-qc-review", false))
	_, err = os.Stat(promptPath)
	require.NoError(t, err, "an edited Skill must be kept")
	_, err = os.Lstat(f.graphPath("qc-review"))
	assert.True(t, os.IsNotExist(err))
}

func TestListReportsStateOnly(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	summaries, err := f.service.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, summaries)

	_, err = f.service.Install(ctx, packinstall.Request{SourcePath: mixedPack(t, f)})
	require.NoError(t, err)
	_, err = f.service.Install(ctx, packinstall.Request{SourcePath: roleOnlyPack(t, f)})
	require.NoError(t, err)

	summaries, err = f.service.List(ctx)
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	assert.Equal(t, "seqyuan/cell-qc-review", summaries[0].Pack.ID)
	assert.Equal(t, 1, summaries[0].FlowCount)
	assert.Equal(t, 2, summaries[0].RoleCount)
	assert.Equal(t, 2, summaries[0].SkillCount)
	assert.Equal(t, packinstall.InstallStateDraft, summaries[0].InstallState)
	assert.Equal(t, "seqyuan/report-writer", summaries[1].Pack.ID)
	assert.Equal(t, 0, summaries[1].FlowCount)
	assert.Equal(t, 1, summaries[1].RoleCount)

	// Removing the state file is enough for List to forget the pack.
	require.NoError(t, os.Remove(f.statePath("seqyuan/report-writer")))
	summaries, err = f.service.List(ctx)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "seqyuan/cell-qc-review", summaries[0].Pack.ID)
}

func TestRemoveUnknownPack(t *testing.T) {
	f := newFixture(t)
	err := f.service.Remove(context.Background(), "seqyuan/unknown", false)
	require.Error(t, err)
	assert.Equal(t, packinstall.CodeNotInstalled, packinstall.Code(err))
}

func TestGCStagingRemovesLeftovers(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	leftover := filepath.Join(f.home, "packs", "staging", "crashed")
	require.NoError(t, os.MkdirAll(leftover, 0o700))
	require.NoError(t, f.service.GCStaging())
	entries, err := os.ReadDir(filepath.Join(f.home, "packs", "staging"))
	require.NoError(t, err)
	assert.Empty(t, entries)

	_, err = f.service.Install(ctx, packinstall.Request{SourcePath: roleOnlyPack(t, f)})
	require.NoError(t, err)
}
