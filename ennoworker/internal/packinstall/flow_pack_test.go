package packinstall_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/packinstall"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func flowPackFiles(f *fixture, roleDocumentBody string) []packFile {
	return []packFile{
		{path: "pack.yaml", body: `schema_version: 1
id: seqyuan/flow-pack
name: Flow Pack
version: 1.0.0
flows:
  - id: qc-flow
    path: flows/qc-flow/flow.yaml
    format: flow
    roles: [qc-reviewer]
roles:
  - handle: qc-reviewer
    path: roles/qc-reviewer/role.md
    scope: flow
`},
		{path: "flows/qc-flow/flow.yaml", body: `schemaVersion: 1
id: qc-flow
budget:
  max_total_tokens: 20000
tasks:
  load:
    role: local/qc-reviewer
    goal: Load the samplesheet
    budget: {tokens: 1000}
  gate:
    type: check
    command: "test -f ready"
    depends: [load]
  review:
    role: local/qc-reviewer
    goal: Review QC metrics
    depends: [gate]
    fan_out: {min: 1, max: 2}
    budget: {tokens: 1000}
  done:
    type: role
    depends: [review]
    terminal: {status: success}
`},
		{path: "roles/qc-reviewer/role.md", body: roleDocumentBody},
	}
}

func TestFlowPackValidationBlocksUnsafeFanOut(t *testing.T) {
	f := newFixture(t)
	f.service.CheckAllowlistAllowsAll = true
	role := roleDocument(f.modelID, "qc-reviewer", "QC Reviewer")
	role.Authority = rolesource.AuthorityWriteWorkspace
	role.AllowedTools = []string{"read", "write"}
	encoded, err := rolesource.Encode(role)
	require.NoError(t, err)
	source := writePack(t, flowPackFiles(f, string(encoded)))
	plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
	require.NoError(t, err)
	assert.True(t, hasBlocker(plan, packinstall.CodeFlowValidationFailed), blockerCodes(plan))
	f.requireNoPackState()
}

func TestFlowPackValidationUsesCheckAllowlist(t *testing.T) {
	f := newFixture(t)
	f.service.CheckAllowlist = []string{"go"}
	source := writePack(t, flowPackFiles(f, roleFile(t, f.modelID, "qc-reviewer", "QC Reviewer")))
	plan, err := f.service.Plan(context.Background(), packinstall.Request{SourcePath: source})
	require.NoError(t, err)
	assert.True(t, hasBlocker(plan, packinstall.CodeFlowValidationFailed), blockerCodes(plan))
	assert.Contains(t, plan.Blockers[0].Message, "check_command_not_allowed")
}

func TestRemoveFlowPackRefusesEnabledBinding(t *testing.T) {
	f := newFixture(t)
	f.service.CheckAllowlistAllowsAll = true
	f.service.Projects = f.projects
	source := writePack(t, flowPackFiles(f, roleFile(t, f.modelID, "qc-reviewer", "QC Reviewer")))
	_, err := f.service.Install(context.Background(), packinstall.Request{SourcePath: source})
	require.NoError(t, err)
	project, _, err := f.projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "Project", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	_, err = f.projects.UpdateGraphBindings(context.Background(), project.ID, func(bindings *[]projectstore.GraphBinding) error {
		*bindings = append(*bindings, projectstore.GraphBinding{
			SourceKind: projectstore.SourceKindFlow, GraphID: "qc-flow", Revision: "v000001", Enabled: true,
		})
		return nil
	})
	require.NoError(t, err)

	err = f.service.Remove(context.Background(), "seqyuan/flow-pack", false)
	require.Error(t, err)
	assert.Equal(t, packinstall.CodeInstallBlocked, packinstall.Code(err))
	assert.Contains(t, err.Error(), "qc-flow")

	require.NoError(t, f.service.Remove(context.Background(), "seqyuan/flow-pack", true))
	_, _, err = f.sources.ReadFlow("qc-flow")
	require.Error(t, err)
}

func TestFlowPackRollbackRemovesTheFlowDraft(t *testing.T) {
	f := newFixture(t)
	f.service.CheckAllowlistAllowsAll = true
	manifestCopy := f.manifestCopyPath("seqyuan/flow-pack")
	require.NoError(t, os.MkdirAll(filepath.Join(manifestCopy, "blocker"), 0o700))
	source := writePack(t, flowPackFiles(f, roleFile(t, f.modelID, "qc-reviewer", "QC Reviewer")))
	_, err := f.service.Install(context.Background(), packinstall.Request{SourcePath: source})
	require.Error(t, err)
	_, _, err = f.sources.ReadFlow("qc-flow")
	require.Error(t, err)
	_, err = os.Lstat(f.statePath("seqyuan/flow-pack"))
	require.True(t, os.IsNotExist(err))
}
