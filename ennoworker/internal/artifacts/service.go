package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var (
	ErrArtifactNotFound   = errors.New("artifact not found")
	ErrArtifactInvalid    = errors.New("artifact is invalid")
	ErrArtifactTooLarge   = errors.New("artifact exceeds size limit")
	ErrArtifactQuota      = errors.New("project artifact quota exceeded")
	ErrArtifactCorrupt    = errors.New("artifact is missing or corrupt")
	ErrPreviewUnsupported = errors.New("artifact preview is unsupported")
)

const (
	defaultMaxArtifactBytes = int64(50 << 20)
	defaultMaxProjectBytes  = int64(2 << 30)
	defaultMaxImagePixels   = int64(40_000_000)
)

type Service struct {
	mu               sync.Mutex
	DB               *sql.DB
	Root             string
	MaxImageBytes    int64
	MaxArtifactBytes int64
	MaxProjectBytes  int64
	MaxPixels        int64
}

type PublishInput struct {
	ProjectID           string
	SessionID           string
	RunID               string
	ToolCallID          string
	Name                string
	SourceKind          string
	SourceWorkspacePath string
	RetentionClass      string
}

func (s *Service) StoreImage(ctx context.Context, projectID, sessionID, name string, data []byte) (*domain.Artifact, error) {
	maxBytes := s.MaxImageBytes
	if maxBytes <= 0 {
		maxBytes = 10 << 20
	}
	if len(data) == 0 || int64(len(data)) > maxBytes {
		return nil, domain.NewCodedError(domain.ErrorImageInvalid,
			fmt.Errorf("image size must be between 1 and %d bytes", maxBytes))
	}
	artifact, err := s.Store(ctx, PublishInput{ProjectID: projectID, SessionID: sessionID, Name: name,
		SourceKind: "upload", RetentionClass: "project"}, bytes.NewReader(data))
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorImageInvalid, err)
	}
	if artifact.Kind != domain.ArtifactKindImage {
		_ = s.removeArtifact(ctx, *artifact)
		return nil, domain.NewCodedError(domain.ErrorImageInvalid, fmt.Errorf("unsupported image type %s", artifact.MIMEType))
	}
	return artifact, nil
}

func (s *Service) Store(ctx context.Context, input PublishInput, source io.Reader) (*domain.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if source == nil {
		return nil, fmt.Errorf("%w: source is required", ErrArtifactInvalid)
	}
	if err := s.validateOwnership(ctx, input.ProjectID, input.SessionID); err != nil {
		return nil, err
	}
	name, err := safeArtifactName(input.Name)
	if err != nil {
		return nil, err
	}
	root, err := s.managedRoot()
	if err != nil {
		return nil, err
	}
	pendingDir := filepath.Join(root, ".pending")
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp(pendingDir, ".artifact-*")
	if err != nil {
		return nil, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return nil, err
	}

	limit := s.maxArtifactBytes()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(source, limit+1))
	if copyErr != nil {
		_ = temp.Close()
		return nil, copyErr
	}
	if written == 0 {
		_ = temp.Close()
		return nil, fmt.Errorf("%w: artifact is empty", ErrArtifactInvalid)
	}
	if written > limit {
		_ = temp.Close()
		return nil, fmt.Errorf("%w: maximum is %d bytes", ErrArtifactTooLarge, limit)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}

	kind, mimeType, width, height, metadata, err := classifyArtifact(tempPath, name, written, s.maxPixels())
	if err != nil {
		return nil, err
	}
	if err := s.checkProjectQuota(ctx, input.ProjectID, written); err != nil {
		return nil, err
	}

	id := uuid.NewString()
	storageKey := filepath.Join("blobs", id[:2], id)
	finalPath := filepath.Join(root, storageKey)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return nil, err
	}
	if err := os.Chmod(finalPath, 0o600); err != nil {
		_ = os.Remove(finalPath)
		return nil, err
	}

	now := time.Now().UTC()
	retention := strings.TrimSpace(input.RetentionClass)
	if retention == "" {
		retention = "project"
	}
	sourceKind := strings.TrimSpace(input.SourceKind)
	if sourceKind == "" {
		sourceKind = "published"
	}
	artifact := &domain.Artifact{
		ID: id, ProjectID: input.ProjectID, SessionID: input.SessionID, RunID: input.RunID,
		Name: name, Kind: kind, MIMEType: mimeType, StoragePath: storageKey, SizeBytes: written,
		SHA256: hex.EncodeToString(hash.Sum(nil)), Width: width, Height: height,
		SourceToolCallID: input.ToolCallID, SourceKind: sourceKind,
		SourceWorkspacePath: input.SourceWorkspacePath, RetentionClass: retention, CreatedAt: now,
	}
	metadataJSON, _ := json.Marshal(metadata)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO artifacts
		(id,project_id,session_id,message_id,run_id,name,kind,mime_type,storage_path,size_bytes,sha256,
		 metadata_json,created_at,source_tool_call_id,source_kind,source_workspace_path,retention_class)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, artifact.ID, artifact.ProjectID, nullable(input.SessionID), nil,
		nullable(input.RunID), artifact.Name, artifact.Kind, artifact.MIMEType, artifact.StoragePath,
		artifact.SizeBytes, artifact.SHA256, string(metadataJSON), now.Format(time.RFC3339Nano),
		artifact.SourceToolCallID, artifact.SourceKind, artifact.SourceWorkspacePath, artifact.RetentionClass)
	if err != nil {
		_ = os.Remove(finalPath)
		return nil, err
	}
	return artifact, nil
}

func (s *Service) GetForSession(ctx context.Context, artifactID, sessionID string) (*domain.Artifact, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT a.id,a.project_id,COALESCE(a.session_id,''),COALESCE(a.message_id,''),
		COALESCE(a.run_id,''),a.name,a.kind,a.mime_type,a.storage_path,a.size_bytes,a.sha256,a.metadata_json,
		a.created_at,a.source_tool_call_id,a.source_kind,a.source_workspace_path,a.retention_class
		FROM artifacts a JOIN sessions s ON s.id=?
		WHERE a.id=? AND a.project_id=s.project_id AND (a.session_id IS NULL OR a.session_id=s.id)`, sessionID, artifactID)
	var artifact domain.Artifact
	var metadataJSON, createdAt string
	if err := row.Scan(&artifact.ID, &artifact.ProjectID, &artifact.SessionID, &artifact.MessageID,
		&artifact.RunID, &artifact.Name, &artifact.Kind, &artifact.MIMEType, &artifact.StoragePath,
		&artifact.SizeBytes, &artifact.SHA256, &metadataJSON, &createdAt, &artifact.SourceToolCallID,
		&artifact.SourceKind, &artifact.SourceWorkspacePath, &artifact.RetentionClass); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrArtifactNotFound
		}
		return nil, err
	}
	artifact.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	var metadata struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	_ = json.Unmarshal([]byte(metadataJSON), &metadata)
	artifact.Width, artifact.Height = metadata.Width, metadata.Height
	return &artifact, nil
}

func (s *Service) ReadForSession(ctx context.Context, artifactID, sessionID string) (*domain.Artifact, []byte, error) {
	artifact, err := s.GetForSession(ctx, artifactID, sessionID)
	if err != nil {
		return nil, nil, err
	}
	path, err := s.resolveStoragePath(artifact.StoragePath)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrArtifactCorrupt
		}
		return nil, nil, err
	}
	if int64(len(data)) != artifact.SizeBytes {
		return nil, nil, ErrArtifactCorrupt
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, nil, ErrArtifactCorrupt
	}
	return artifact, data, nil
}

func (s *Service) LoadImage(ctx context.Context, artifactID string) (domain.ImageRef, error) {
	var sessionID string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(session_id,'') FROM artifacts WHERE id=?`, artifactID).Scan(&sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ImageRef{}, ErrArtifactNotFound
		}
		return domain.ImageRef{}, err
	}
	if sessionID == "" {
		return domain.ImageRef{}, ErrArtifactNotFound
	}
	artifact, data, err := s.ReadForSession(ctx, artifactID, sessionID)
	if err != nil {
		return domain.ImageRef{}, err
	}
	if artifact.Kind != domain.ArtifactKindImage && artifact.Kind != "image_attachment" {
		return domain.ImageRef{}, domain.NewCodedError(domain.ErrorImageInvalid, fmt.Errorf("artifact is not an image"))
	}
	return domain.ImageRef{ArtifactID: artifact.ID, MIMEType: artifact.MIMEType, SHA256: artifact.SHA256,
		Width: artifact.Width, Height: artifact.Height, Data: data}, nil
}

func (s *Service) ValidateForSession(ctx context.Context, artifactID, sessionID string) (domain.ImageRef, error) {
	artifact, data, err := s.ReadForSession(ctx, artifactID, sessionID)
	if err != nil {
		return domain.ImageRef{}, err
	}
	if artifact.Kind != domain.ArtifactKindImage && artifact.Kind != "image_attachment" {
		return domain.ImageRef{}, domain.NewCodedError(domain.ErrorImageInvalid, fmt.Errorf("artifact is not an image"))
	}
	return domain.ImageRef{ArtifactID: artifact.ID, MIMEType: artifact.MIMEType, SHA256: artifact.SHA256,
		Width: artifact.Width, Height: artifact.Height, Data: data}, nil
}

func classifyArtifact(path, name string, size, maxPixels int64) (string, string, int, int, map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", 0, 0, nil, err
	}
	defer file.Close()
	head := make([]byte, 512)
	read, err := file.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", 0, 0, nil, err
	}
	head = head[:read]
	detected := strings.Split(http.DetectContentType(head), ";")[0]
	ext := strings.ToLower(filepath.Ext(name))
	metadata := map[string]any{}

	if detected == "image/png" || detected == "image/jpeg" || detected == "image/gif" {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", "", 0, 0, nil, err
		}
		config, _, err := image.DecodeConfig(file)
		if err != nil {
			return "", "", 0, 0, nil, fmt.Errorf("%w: decode image: %v", ErrArtifactInvalid, err)
		}
		if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxPixels {
			return "", "", 0, 0, nil, fmt.Errorf("%w: image dimensions exceed policy limit", ErrArtifactInvalid)
		}
		metadata["width"], metadata["height"] = config.Width, config.Height
		return domain.ArtifactKindImage, detected, config.Width, config.Height, metadata, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, 0, nil, err
	}
	switch ext {
	case ".csv", ".tsv":
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return "", "", 0, 0, nil, fmt.Errorf("%w: table must be UTF-8 text", ErrArtifactInvalid)
		}
		delimiter := ','
		mimeType := "text/csv; charset=utf-8"
		if ext == ".tsv" {
			delimiter = '\t'
			mimeType = "text/tab-separated-values; charset=utf-8"
		}
		if _, err := parseDelimitedPreview(bytes.NewReader(data), rune(delimiter), 1, 1); err != nil {
			return "", "", 0, 0, nil, fmt.Errorf("%w: %v", ErrArtifactInvalid, err)
		}
		metadata["format"] = strings.TrimPrefix(ext, ".")
		return domain.ArtifactKindTable, mimeType, 0, 0, metadata, nil
	case ".xlsx":
		if size > maxXLSXSourceBytes {
			return "", "", 0, 0, nil, fmt.Errorf("%w: XLSX source exceeds %d bytes", ErrArtifactTooLarge, maxXLSXSourceBytes)
		}
		if err := validateXLSXArchive(data); err != nil {
			return "", "", 0, 0, nil, fmt.Errorf("%w: %v", ErrArtifactInvalid, err)
		}
		metadata["format"] = "xlsx"
		return domain.ArtifactKindTable, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", 0, 0, metadata, nil
	case ".html", ".htm":
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return "", "", 0, 0, nil, fmt.Errorf("%w: HTML must be UTF-8 text", ErrArtifactInvalid)
		}
		return domain.ArtifactKindStaticHTML, "text/html; charset=utf-8", 0, 0, metadata, nil
	case ".txt", ".log", ".out", ".err":
		if utf8.Valid(data) && bytes.IndexByte(data, 0) < 0 {
			return domain.ArtifactKindText, "text/plain; charset=utf-8", 0, 0, metadata, nil
		}
	}
	if strings.HasPrefix(detected, "text/") && utf8.Valid(data) && bytes.IndexByte(data, 0) < 0 {
		return domain.ArtifactKindText, "text/plain; charset=utf-8", 0, 0, metadata, nil
	}
	return domain.ArtifactKindFile, detected, 0, 0, metadata, nil
}

func (s *Service) validateOwnership(ctx context.Context, projectID, sessionID string) error {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id=? AND project_id=? AND status='active'`,
		sessionID, projectID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("session does not belong to project")
	}
	return nil
}

func (s *Service) checkProjectQuota(ctx context.Context, projectID string, incoming int64) error {
	var used int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM artifacts WHERE project_id=?`, projectID).Scan(&used); err != nil {
		return err
	}
	if incoming > s.maxProjectBytes()-used {
		return fmt.Errorf("%w: maximum is %d bytes", ErrArtifactQuota, s.maxProjectBytes())
	}
	return nil
}

func (s *Service) managedRoot() (string, error) {
	if strings.TrimSpace(s.Root) == "" {
		return "", fmt.Errorf("artifact root is required")
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

func (s *Service) resolveStoragePath(storagePath string) (string, error) {
	root, err := s.managedRoot()
	if err != nil {
		return "", err
	}
	candidate := storagePath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	if !withinRoot(root, candidate) {
		return "", ErrArtifactCorrupt
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", ErrArtifactCorrupt
	}
	canonicalCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil || !withinRoot(canonicalRoot, canonicalCandidate) {
		return "", ErrArtifactCorrupt
	}
	return canonicalCandidate, nil
}

func (s *Service) maxArtifactBytes() int64 {
	if s.MaxArtifactBytes > 0 {
		return s.MaxArtifactBytes
	}
	return defaultMaxArtifactBytes
}

func (s *Service) maxProjectBytes() int64 {
	if s.MaxProjectBytes > 0 {
		return s.MaxProjectBytes
	}
	return defaultMaxProjectBytes
}

func (s *Service) maxPixels() int64 {
	if s.MaxPixels > 0 {
		return s.MaxPixels
	}
	return defaultMaxImagePixels
}

func (s *Service) removeArtifact(ctx context.Context, artifact domain.Artifact) error {
	path, err := s.resolveStoragePath(artifact.StoragePath)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM artifacts WHERE id=?`, artifact.ID)
	return err
}

func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.managedRoot()
	if err != nil {
		return err
	}
	pendingDir := filepath.Join(root, ".pending")
	entries, err := os.ReadDir(pendingDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr == nil && !info.IsDir() && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(pendingDir, entry.Name()))
		}
	}

	rows, err := s.DB.QueryContext(ctx, `SELECT storage_path FROM artifacts`)
	if err != nil {
		return err
	}
	known := make(map[string]struct{})
	for rows.Next() {
		var storagePath string
		if err := rows.Scan(&storagePath); err != nil {
			rows.Close()
			return err
		}
		if !filepath.IsAbs(storagePath) {
			known[filepath.Clean(storagePath)] = struct{}{}
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	blobsDir := filepath.Join(root, "blobs")
	return filepath.WalkDir(blobsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, exists := known[filepath.Clean(relative)]; !exists {
			return os.Remove(path)
		}
		return nil
	})
}

func safeArtifactName(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	value = filepath.Base(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if value == "" || value == "." || value == ".." || len(value) > 255 {
		return "", fmt.Errorf("%w: invalid artifact name", ErrArtifactInvalid)
	}
	return value, nil
}

func withinRoot(root, candidate string) bool {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absolutePath, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteRoot, absolutePath)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
