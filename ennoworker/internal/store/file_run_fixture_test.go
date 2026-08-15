package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/require"
)

// fileConfigStack is the V2 file-native model/provider/policy/source stack
// shared by tests that freeze effective Run configs. All ids are stable:
// the provider key is "provider" and the default model portable ref is
// "provider/model".
type fileConfigStack struct {
	Home       string
	Models     *fileconfig.ModelStore
	Policies   *fileconfig.PolicyStore
	Sources    *globalsource.Store
	Providers  *store.ProviderRepo
	ModelRepo  *store.ModelRepo
	DefaultRef string // portable ref of the default model ("provider/model")
}

// newFileConfigStack creates the file store + one default thinking-capable
// model. Tests can add more models via stack.Models.CreateModel.
func newFileConfigStack(t *testing.T) *fileConfigStack {
	t.Helper()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(context.Background(), fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-file-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(context.Background(), fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "model", ContextWindow: 64000, MaxOutputTokens: 4096,
		SupportsToolUse: true, SupportsThinking: true,
		ThinkingDialect:          domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium, domain.ThinkingHigh},
		IsDefault:                true,
	})
	require.NoError(t, err)
	return &fileConfigStack{
		Home:       home,
		Models:     models,
		Policies:   &fileconfig.PolicyStore{Path: filepath.Join(home, "config", "policies.json")},
		Sources:    &globalsource.Store{HomeDir: home},
		Providers:  &store.ProviderRepo{Files: models},
		ModelRepo:  &store.ModelRepo{Files: models},
		DefaultRef: model.ID,
	}
}

// fileRunFixture is a fixture-DB (MigrateFixtureSchema) project + session with
// a file-native RunRepo so effective-config freezing never touches the removed
// global provider/model/policy SQL tables. The DB still carries the session
// tables plus the fixture snapshot (settings, role tables) so delegation tests
// can keep using raw SQL against the fixture role identity.
type fileRunFixture struct {
	Stack     *fileConfigStack
	DB        *sql.DB
	ProjectID string
	SessionID string
	Runs      *store.RunRepo
	// SessionDefaultModel points at stack.DefaultRef by default.
	SessionDefaultModel *string
}

// newFileRunFixture seeds a Session (per-Session SQLite file) with the
// file-backed RunRepo wired over the opened Session database. Default model is
// stack.DefaultRef unless overridden.
func newFileRunFixture(t *testing.T, requestID string) *fileRunFixture {
	t.Helper()
	stack := newFileConfigStack(t)
	ctx := context.Background()
	projects := &projectstore.Store{Root: filepath.Join(stack.Home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: requestID, HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: requestID, DefaultModelProfileID: &stack.DefaultRef,
	})
	require.NoError(t, err)
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	return &fileRunFixture{
		Stack: stack, DB: db, ProjectID: project.ID, SessionID: session.ID,
		Runs: &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo,
			Policies: stack.Policies, RoleSources: stack.Sources},
		SessionDefaultModel: &stack.DefaultRef,
	}
}

// Delegations returns a DelegationRepo over the fixture DB with the file-backed
// policy + role sources wired (V2).
func (f *fileRunFixture) Delegations() *store.DelegationRepo {
	return &store.DelegationRepo{DB: f.DB, RoleSources: f.Stack.Sources,
		Models: f.Stack.ModelRepo, Policies: f.Stack.Policies}
}

// fileRunSubmission submits a Host turn and claims it on the file-backed RunRepo.
func (f *fileRunFixture) SubmitAndClaim(t *testing.T, requestID string) *domain.AgentRun {
	t.Helper()
	ctx := context.Background()
	submission, err := f.Runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: f.SessionID, ClientRequestID: requestID, Text: "run",
	})
	require.NoError(t, err)
	claimed, err := f.Runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	return claimed
}

// sessionstoreFileRunFixture wires the full file-native stack through the
// sessionstore Manager (real file-per-session DBs), used by tests that need
// the V2 session file layout.
type sessionstoreFileRunFixture struct {
	Stack    *fileConfigStack
	Projects *projectstore.Store
	Sessions *sessionstore.Manager
	DB       *sql.DB
	Session  domain.Session
	Runs     *store.RunRepo
}

func newSessionstoreFileRunFixture(t *testing.T, title string) *sessionstoreFileRunFixture {
	t.Helper()
	stack := newFileConfigStack(t)
	projects := &projectstore.Store{Root: filepath.Join(stack.Home, "projects")}
	project, _, err := projects.CreateWithWorkspace(context.Background(),
		domain.CreateProjectInput{Name: title, HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(context.Background(), domain.CreateSessionInput{
		ProjectID: project.ID, Title: title, DefaultModelProfileID: &stack.DefaultRef,
	})
	require.NoError(t, err)
	db, err := sessions.OpenSession(context.Background(), session.ID)
	require.NoError(t, err)
	return &sessionstoreFileRunFixture{
		Stack: stack, Projects: projects, Sessions: sessions, DB: db, Session: *session,
		Runs: &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo,
			Policies: stack.Policies, RoleSources: stack.Sources},
	}
}

// newFileProjects returns a file-native ProjectRepo (V2). The legacy global
// projects SQL table was removed.
func newFileProjects(t testing.TB) *store.ProjectRepo {
	t.Helper()
	return &store.ProjectRepo{Files: &projectstore.Store{Root: t.TempDir()}}
}

// newSessionDB creates a project + Session in the file-native layout and
// opens the per-Session SQLite database. It returns the opened db (for
// Run/Message/Session-db repos and raw SQL), the manager (for top-level
// SessionRepo operations), and the created Session.
func newSessionDB(t *testing.T) (*sql.DB, *sessionstore.Manager, domain.Session) {
	t.Helper()
	ctx := context.Background()
	projects := &projectstore.Store{Root: t.TempDir()}
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "session", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "session"})
	require.NoError(t, err)
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	return db, sessions, *session
}

func sqlCreateSession(t *testing.T, db *sql.DB, projectID string) domain.Session {
	return sqlCreateSessionWithModel(t, db, projectID, nil)
}

// sqlCreateSessionWithModel is sqlCreateSession with an optional default model.
func sqlCreateSessionWithModel(t *testing.T, db *sql.DB, projectID string, defaultModel *string) domain.Session {
	t.Helper()
	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	id, branchID := uuid.NewString(), uuid.NewString()
	_, err := db.Exec(`INSERT INTO sessions (id, project_id, title, status, mode, default_model_profile_id, active_branch_id, created_at, updated_at)
		VALUES (?,?,?, 'active','hosted',?,NULL,?,?)`, id, projectID, "session", defaultModel, timestamp, timestamp)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO session_branches (id,session_id,label,created_at,updated_at) VALUES(?,?,'Main',?,?)`,
		branchID, id, timestamp, timestamp)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sessions SET active_branch_id=? WHERE id=?`, branchID, id)
	require.NoError(t, err)
	session := domain.Session{ID: id, ProjectID: projectID, Title: "session", Status: "active",
		Mode: domain.SessionModeHosted, DefaultModelProfileID: defaultModel,
		ActiveBranchID: &branchID, CreatedAt: now, UpdatedAt: now}
	return session
}
