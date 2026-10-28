package packinstall_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/packinstall"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/require"
)

// fixture is a real Worker home: file-native model catalog, source store, user
// skills root, and a Session manager rooted in ENNOTE_HOME.
type fixture struct {
	t          *testing.T
	home       string
	sources    *globalsource.Store
	models     *fileconfig.ModelStore
	skillsRoot string
	projects   *projectstore.Store
	sessions   *sessionstore.Manager
	service    *packinstall.Service
	modelID    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-pack-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "pack-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)

	sources := &globalsource.Store{HomeDir: home}
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { _ = sessions.Close() })
	skillsRoot := filepath.Join(home, "skills")
	service := &packinstall.Service{
		HomeDir: home, Sources: sources, Models: &store.ModelRepo{Files: models},
		SkillsRoot: skillsRoot, Sessions: sessions,
		Now: func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) },
	}
	return &fixture{
		t: t, home: home, sources: sources, models: models, skillsRoot: skillsRoot,
		projects: projects, sessions: sessions, service: service, modelID: model.ID,
	}
}

func (f *fixture) graphPath(id string) string {
	return filepath.Join(f.home, "agents", "graphs", id, "graph.yaml")
}

func (f *fixture) graphRolePath(graphID, handle string) string {
	return filepath.Join(f.home, "agents", "graphs", graphID, "roles", handle, "role.md")
}

func (f *fixture) graphSkillPath(graphID, id string) string {
	return filepath.Join(f.home, "agents", "graphs", graphID, "skills", id)
}

func (f *fixture) rolePath(handle string) string {
	return filepath.Join(f.home, "agents", "roles", handle, "role.md")
}

func (f *fixture) skillPath(id string) string {
	return filepath.Join(f.skillsRoot, id)
}

func (f *fixture) statePath(packID string) string {
	publisher, name, found := cutPackID(packID)
	require.True(f.t, found)
	return filepath.Join(f.home, "packs", "state", publisher, name+".json")
}

func (f *fixture) manifestCopyPath(packID string) string {
	publisher, name, found := cutPackID(packID)
	require.True(f.t, found)
	return filepath.Join(f.home, "packs", "state", publisher, name, "manifest.yaml")
}

func (f *fixture) requireNoPackState() {
	f.t.Helper()
	_, err := os.Lstat(filepath.Join(f.home, "packs"))
	require.True(f.t, os.IsNotExist(err), "expected no packs state, got %v", err)
}

func (f *fixture) createSession() domain.Session {
	f.t.Helper()
	ctx := context.Background()
	project, _, err := f.projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "Project", HostPath: f.t.TempDir(),
	})
	require.NoError(f.t, err)
	session, err := f.sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(f.t, err)
	return *session
}

// installExternalSkill writes a prompt-only Skill into the user skills root so an
// external skill ref can resolve.
func (f *fixture) installExternalSkill(id string) {
	f.t.Helper()
	_, err := skillsmgmt.Write(f.skillsRoot, skillsmgmt.CreateInput{
		ID: id, Name: id, Description: "External skill", Prompt: "Use the external skill.",
	})
	require.NoError(f.t, err)
}

type packFile struct {
	path string
	body string
}

func writePack(t *testing.T, files []packFile) string {
	t.Helper()
	root := t.TempDir()
	for _, file := range files {
		target := filepath.Join(root, filepath.FromSlash(file.path))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, []byte(file.body), 0o644))
	}
	return root
}

func cutPackID(packID string) (string, string, bool) {
	for index := 0; index < len(packID); index++ {
		if packID[index] == '/' {
			return packID[:index], packID[index+1:], true
		}
	}
	return "", "", false
}

// roleDocument builds a valid Role source document under modelID.
func roleDocument(modelID, handle, name string, skills ...rolesource.SkillBinding) *rolesource.Document {
	return &rolesource.Document{
		SchemaVersion: 1, Handle: handle, Name: name, Description: name + " role",
		Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:     rolesource.ModelBinding{Ref: modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills:    append([]rolesource.SkillBinding{}, skills...),
		Authority: rolesource.AuthorityReadOnly, PermissionCeiling: domain.PermissionDiscuss,
		AllowedTools: []string{"read", "grep"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 4, MaxConcurrentInstances: 1,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Follow the " + name + " instructions.",
	}
}

func roleFile(t *testing.T, modelID, handle, name string, skills ...rolesource.SkillBinding) string {
	t.Helper()
	encoded, err := rolesource.Encode(roleDocument(modelID, handle, name, skills...))
	require.NoError(t, err)
	return string(encoded)
}

func findAction(plan *packinstall.Plan, kind, storeID string) (packinstall.Action, bool) {
	for _, action := range plan.Actions {
		if action.Kind == kind && action.StoreID == storeID {
			return action, true
		}
	}
	return packinstall.Action{}, false
}

func hasBlocker(plan *packinstall.Plan, code string) bool {
	for _, blocker := range plan.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func blockerCodes(plan *packinstall.Plan) []string {
	codes := make([]string, 0, len(plan.Blockers))
	for _, blocker := range plan.Blockers {
		codes = append(codes, blocker.Code)
	}
	return codes
}
