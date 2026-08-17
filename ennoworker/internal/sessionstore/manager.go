package sessionstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/sessionmigrations"
	_ "modernc.org/sqlite"
)

type handle struct {
	projectID string
	path      string
	db        *sql.DB
	lastUsed  time.Time
}

type Manager struct {
	ProjectsRoot string
	Projects     *projectstore.Store
	Now          func() time.Time

	mu        sync.Mutex
	handles   map[string]*handle
	index     map[string]string
	owners    map[string]string
	listCache map[string][]domain.Session
}

func NewManager(projectsRoot string, projects *projectstore.Store) *Manager {
	return &Manager{
		ProjectsRoot: projectsRoot, Projects: projects,
		handles: map[string]*handle{}, index: map[string]string{}, owners: map[string]string{},
		listCache: map[string][]domain.Session{},
	}
}

func (m *Manager) Create(ctx context.Context, input domain.CreateSessionInput) (*domain.Session, error) {
	if _, err := uuid.Parse(input.ProjectID); err != nil {
		return nil, fmt.Errorf("invalid project id %q", input.ProjectID)
	}
	if m.Projects != nil {
		project, err := m.Projects.FindByID(ctx, input.ProjectID)
		if err != nil {
			return nil, err
		}
		if project == nil {
			return nil, fmt.Errorf("project not found: %s", input.ProjectID)
		}
	}
	now := m.now()
	sessionID := uuid.NewString()
	branchID := uuid.NewString()
	title := input.Title
	if title == "" {
		title = "New Session"
	}
	sessionsDir := filepath.Join(m.ProjectsRoot, input.ProjectID, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create sessions directory: %w", err)
	}
	temporary, err := os.MkdirTemp(sessionsDir, ".session-*")
	if err != nil {
		return nil, fmt.Errorf("create session temp directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return nil, err
	}
	for _, directory := range []string{
		filepath.Join(temporary, "artifacts"),
		filepath.Join(temporary, "snapshots"),
		filepath.Join(temporary, "snapshots", "skills"),
	} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return nil, err
		}
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, SessionID: sessionID,
		ProjectID: input.ProjectID, CreatedAt: now,
	}
	if err := writeManifest(filepath.Join(temporary, "session.json"), manifest); err != nil {
		return nil, fmt.Errorf("write session manifest: %w", err)
	}
	databasePath := filepath.Join(temporary, "session.db")
	db, err := openDatabase(databasePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := migrate(db); err != nil {
		return nil, err
	}
	timestamp := now.Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_store_metadata
		(singleton,session_id,project_id,schema_version,created_at) VALUES(1,?,?,?,?)`,
		sessionID, input.ProjectID, ManifestSchemaVersion, timestamp); err != nil {
		return nil, fmt.Errorf("write session metadata: %w", err)
	}
	var defaultAgent, defaultModel, compactionPolicy any
	if input.DefaultAgentProfileID != nil {
		defaultAgent = *input.DefaultAgentProfileID
	}
	if input.DefaultModelProfileID != nil {
		defaultModel = *input.DefaultModelProfileID
	}
	if input.CompactionPolicyProfileID != nil {
		compactionPolicy = *input.CompactionPolicyProfileID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions
		(id,project_id,title,status,mode,active_leaf_message_id,active_branch_id,
		 default_agent_profile_id,default_model_profile_id,compaction_policy_profile_id,
		 source_session_id,source_message_id,created_at,updated_at)
		 VALUES(?,?,?,'active','hosted',NULL,NULL,?,?,?,?,?,?,?)`,
		sessionID, input.ProjectID, title, defaultAgent, defaultModel, compactionPolicy,
		input.SourceSessionID, input.SourceMessageID, timestamp, timestamp); err != nil {
		return nil, fmt.Errorf("create session row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_branches
		(id,session_id,label,created_at,updated_at) VALUES(?,?,'Main',?,?)`,
		branchID, sessionID, timestamp, timestamp); err != nil {
		return nil, fmt.Errorf("create main branch: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET active_branch_id=? WHERE id=?`, branchID, sessionID); err != nil {
		return nil, fmt.Errorf("activate main branch: %w", err)
	}
	projectionPayload, err := json.Marshal(map[string]string{
		"sessionId": sessionID, "projectId": input.ProjectID, "title": title,
		"status": "active", "createdAt": timestamp, "updatedAt": timestamp,
	})
	if err != nil {
		return nil, fmt.Errorf("encode session projection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projection_outbox
		(event_id,event_type,payload_json,created_at) VALUES(?,?,?,?)`, uuid.NewString(),
		"session.upsert", string(projectionPayload), timestamp); err != nil {
		return nil, fmt.Errorf("queue session projection: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session creation: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return nil, fmt.Errorf("checkpoint new session: %w", err)
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	if err := syncDirectory(temporary); err != nil {
		return nil, err
	}
	final := filepath.Join(sessionsDir, sessionID)
	if err := os.Rename(temporary, final); err != nil {
		return nil, fmt.Errorf("publish session directory: %w", err)
	}
	if err := syncDirectory(sessionsDir); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.index[sessionID] = input.ProjectID
	m.mu.Unlock()
	m.InvalidateProject(input.ProjectID)
	return &domain.Session{
		ID: sessionID, ProjectID: input.ProjectID, Title: title, Status: "active",
		Mode: domain.SessionModeHosted, ActiveBranchID: &branchID,
		DefaultAgentProfileID:     input.DefaultAgentProfileID,
		DefaultModelProfileID:     input.DefaultModelProfileID,
		CompactionPolicyProfileID: input.CompactionPolicyProfileID,
		SourceSessionID:           input.SourceSessionID, SourceMessageID: input.SourceMessageID,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (m *Manager) SessionPath(sessionID string) (string, error) {
	if _, err := uuid.Parse(sessionID); err != nil {
		return "", fmt.Errorf("invalid session id %q", sessionID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, path, err := m.locateLocked(sessionID)
	return path, err
}

func (m *Manager) OpenSession(ctx context.Context, sessionID string) (*sql.DB, error) {
	if _, err := uuid.Parse(sessionID); err != nil {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.handles[sessionID]; existing != nil {
		existing.lastUsed = m.now()
		return existing.db, nil
	}
	projectID, path, err := m.locateLocked(sessionID)
	if err != nil {
		return nil, err
	}
	manifest, err := readManifest(filepath.Join(path, "session.json"))
	if err != nil {
		return nil, err
	}
	if manifest.SessionID != sessionID || manifest.ProjectID != projectID {
		return nil, fmt.Errorf("session manifest identity does not match directory")
	}
	db, err := openDatabase(filepath.Join(path, "session.db"))
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := scanTail(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	var storedSessionID, storedProjectID string
	if err := db.QueryRowContext(ctx, `SELECT session_id,project_id FROM session_store_metadata WHERE singleton=1`).Scan(&storedSessionID, &storedProjectID); err != nil {
		db.Close()
		return nil, fmt.Errorf("read session store metadata: %w", err)
	}
	if storedSessionID != sessionID || storedProjectID != projectID {
		db.Close()
		return nil, fmt.Errorf("session database identity does not match directory")
	}
	m.handles[sessionID] = &handle{projectID: projectID, path: path, db: db, lastUsed: m.now()}
	return db, nil
}

func (m *Manager) FindByID(ctx context.Context, sessionID string) (*domain.Session, error) {
	db, err := m.OpenSession(ctx, sessionID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return findSession(ctx, db, sessionID)
}

// ContextUsage returns the latest context-occupancy projection reported for a
// Session (the newest durable context_usage run event), or nil before the
// first report. It reads the projection from the Session's own database, so it
// is the same authority the live run stream replays.
func (m *Manager) ContextUsage(ctx context.Context, sessionID string) (*domain.SessionContextUsage, error) {
	db, err := m.OpenSession(ctx, sessionID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return readContextUsage(ctx, db, sessionID)
}

// readContextUsage returns the newest durable context_usage run event for a
// Session, or nil before the first report.
func readContextUsage(ctx context.Context, db *sql.DB, sessionID string) (*domain.SessionContextUsage, error) {
	var payloadJSON string
	err := db.QueryRowContext(ctx, `
		SELECT re.payload_json
		FROM run_events re
		JOIN agent_runs ar ON ar.id = re.run_id
		WHERE ar.session_id = ? AND re.event_type = 'context_usage'
		ORDER BY re.created_at DESC, re.event_id DESC
		LIMIT 1`, sessionID).Scan(&payloadJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var usage domain.SessionContextUsage
	if err := json.Unmarshal([]byte(payloadJSON), &usage); err != nil {
		return nil, fmt.Errorf("decode session context usage: %w", err)
	}
	return &usage, nil
}

func (m *Manager) ListByProject(ctx context.Context, projectID string, status string) ([]domain.Session, error) {
	cacheKey := projectID + "\x00" + status
	m.mu.Lock()
	if cached, ok := m.listCache[cacheKey]; ok {
		m.mu.Unlock()
		return append([]domain.Session(nil), cached...), nil
	}
	m.mu.Unlock()

	sessionsDir := filepath.Join(m.ProjectsRoot, projectID, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.Session{}, nil
	}
	if err != nil {
		return nil, err
	}
	sessions := make([]domain.Session, 0, len(entries))
	for _, entry := range entries {
		if _, err := uuid.Parse(entry.Name()); err != nil {
			continue
		}
		m.mu.Lock()
		m.index[entry.Name()] = projectID
		m.mu.Unlock()
		session, err := m.FindByID(ctx, entry.Name())
		if err != nil {
			return nil, err
		}
		if session != nil && (status == "" || session.Status == status) {
			sessions = append(sessions, *session)
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
			return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
		}
		return sessions[i].ID < sessions[j].ID
	})
	m.mu.Lock()
	m.listCache[cacheKey] = append([]domain.Session(nil), sessions...)
	m.mu.Unlock()
	return sessions, nil
}

// InvalidateProject drops every cached session list for a project, forcing the
// next ListByProject to re-read the filesystem. Writers call this after a
// successful mutation.
func (m *Manager) InvalidateProject(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := projectID + "\x00"
	for key := range m.listCache {
		if strings.HasPrefix(key, prefix) {
			delete(m.listCache, key)
		}
	}
}

// InvalidateSession drops the cached lists for the project that owns a session.
func (m *Manager) InvalidateSession(sessionID string) {
	m.mu.Lock()
	projectID := m.index[sessionID]
	m.mu.Unlock()
	if projectID != "" {
		m.InvalidateProject(projectID)
	}
}

func (m *Manager) RegisterOwner(kind, resourceID, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.owners[kind+"\x00"+resourceID] = sessionID
}

func (m *Manager) OpenByResource(ctx context.Context, kind, resourceID string) (*sql.DB, string, error) {
	sessionID, err := m.SessionIDForResource(ctx, kind, resourceID)
	if err != nil {
		return nil, "", err
	}
	db, err := m.OpenSession(ctx, sessionID)
	return db, sessionID, err
}

func (m *Manager) SessionIDForResource(ctx context.Context, kind, resourceID string) (string, error) {
	source, ok := resourceSources[kind]
	if !ok {
		return "", fmt.Errorf("unsupported Session resource kind %q", kind)
	}
	key := kind + "\x00" + resourceID
	m.mu.Lock()
	if sessionID := m.owners[key]; sessionID != "" {
		m.mu.Unlock()
		return sessionID, nil
	}
	m.mu.Unlock()
	sessionIDs, err := m.AllSessionIDs()
	if err != nil {
		return "", err
	}
	for _, sessionID := range sessionIDs {
		db, err := m.OpenSession(ctx, sessionID)
		if err != nil {
			return "", err
		}
		var exists int
		query := "SELECT COUNT(*) FROM " + source.table + " WHERE " + source.column + "=?"
		if err := db.QueryRowContext(ctx, query, resourceID).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 1 {
			m.RegisterOwner(kind, resourceID, sessionID)
			return sessionID, nil
		}
	}
	return "", sql.ErrNoRows
}

func (m *Manager) AllSessionIDs() ([]string, error) {
	projects, err := os.ReadDir(m.ProjectsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for _, project := range projects {
		if _, err := uuid.Parse(project.Name()); err != nil {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(m.ProjectsRoot, project.Name(), "sessions"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if _, err := uuid.Parse(entry.Name()); err != nil {
				continue
			}
			m.mu.Lock()
			m.index[entry.Name()] = project.Name()
			m.mu.Unlock()
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

type resourceSource struct {
	table  string
	column string
}

var resourceSources = map[string]resourceSource{
	"run":                 {"agent_runs", "id"},
	"message":             {"messages", "id"},
	"compaction":          {"context_compactions", "id"},
	"approval":            {"tool_approval_requests", "id"},
	"standing_approval":   {"standing_approvals", "id"},
	"delegation_group":    {"delegation_groups", "id"},
	"delegation_item":     {"delegation_items", "id"},
	"delegation_handle":   {"delegation_handles", "id"},
	"delegation_approval": {"delegation_approval_requests", "id"},
	"attention":           {"attention_items", "id"},
	"artifact":            {"artifacts", "id"},
	"flow_run":            {"run_agent_flow", "run_id"},
	"mcp_request":         {"mcp_requests", "id"},
}

func (m *Manager) CloseIdle(before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for sessionID, current := range m.handles {
		if !current.lastUsed.Before(before) {
			continue
		}
		if err := current.db.Close(); err != nil && first == nil {
			first = err
		}
		delete(m.handles, sessionID)
	}
	return first
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for sessionID, current := range m.handles {
		if err := current.db.Close(); err != nil && first == nil {
			first = err
		}
		delete(m.handles, sessionID)
	}
	return first
}

func (m *Manager) locateLocked(sessionID string) (string, string, error) {
	if projectID := m.index[sessionID]; projectID != "" {
		path := filepath.Join(m.ProjectsRoot, projectID, "sessions", sessionID)
		return projectID, path, nil
	}
	projects, err := os.ReadDir(m.ProjectsRoot)
	if err != nil {
		return "", "", err
	}
	for _, project := range projects {
		if _, err := uuid.Parse(project.Name()); err != nil {
			continue
		}
		path := filepath.Join(m.ProjectsRoot, project.Name(), "sessions", sessionID)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", "", fmt.Errorf("session must be a regular directory")
		}
		m.index[sessionID] = project.Name()
		return project.Name(), path, nil
	}
	return "", "", os.ErrNotExist
}

func openDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open session database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping session database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("protect session database: %w", err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	for _, migration := range sessionmigrations.Sorted() {
		if migration.Version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migration.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("session migration %d: %w", migration.Version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, migration.Version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if err := backfillMessageSeq(db); err != nil {
		return err
	}
	return nil
}

// backfillMessageSeq assigns session-monotonic seq values to messages created
// before the seq column existed (seq = 0). It runs once per session database
// (guarded by the seq=0 probe) and orders by created_at + id, which is
// monotonic along every lineage (a parent is always inserted before its
// child), so the client's consecutive-assertion over seq holds. New inserts
// continue from MAX(seq).
func backfillMessageSeq(db *sql.DB) error {
	var unsequenced int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE seq = 0`).Scan(&unsequenced); err != nil {
		return fmt.Errorf("probe unsequenced messages: %w", err)
	}
	if unsequenced == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM messages ORDER BY created_at, id`)
	if err != nil {
		return fmt.Errorf("enumerate messages for seq backfill: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan message id for backfill: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close backfill rows: %w", err)
	}
	for index, id := range ids {
		if _, err := tx.Exec(`UPDATE messages SET seq = ? WHERE id = ?`, index+1, id); err != nil {
			return fmt.Errorf("backfill message seq: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seq backfill: %w", err)
	}
	return nil
}

func findSession(ctx context.Context, db *sql.DB, id string) (*domain.Session, error) {
	var session domain.Session
	var createdAt, updatedAt string
	err := db.QueryRowContext(ctx, `SELECT id,project_id,title,status,mode,active_leaf_message_id,active_branch_id,
		default_agent_profile_id,default_model_profile_id,compaction_policy_profile_id,
		source_session_id,source_message_id,created_at,updated_at FROM sessions WHERE id=?`, id).Scan(
		&session.ID, &session.ProjectID, &session.Title, &session.Status, &session.Mode,
		&session.ActiveLeafMessageID, &session.ActiveBranchID, &session.DefaultAgentProfileID,
		&session.DefaultModelProfileID, &session.CompactionPolicyProfileID,
		&session.SourceSessionID, &session.SourceMessageID, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}
