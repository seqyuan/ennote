package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
)

type SkillSnapshotRepo struct{ DB *sql.DB }

type SkillSnapshot struct {
	ID             string
	RunID          string
	SkillID        string
	Version        string
	ManifestDigest string
	ContentDigest  string
	SnapshotPath   string
	CreatedAt      time.Time
}

func (r *SkillSnapshotRepo) Save(ctx context.Context, runID string, skill *skills.LoadedSkill, snapshotPath string) (*SkillSnapshot, error) {
	now := time.Now().UTC()
	snap := &SkillSnapshot{
		ID: uuid.NewString(), RunID: runID, SkillID: skill.Manifest.ID,
		Version: skill.Manifest.Version, ManifestDigest: skill.ManifestHash,
		ContentDigest: skill.ContentHash, SnapshotPath: snapshotPath, CreatedAt: now,
	}
	_, err := r.DB.ExecContext(ctx,
		`INSERT INTO skill_snapshots (id, run_id, skill_id, version, manifest_digest, content_digest, snapshot_path, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.ID, snap.RunID, snap.SkillID, snap.Version,
		snap.ManifestDigest, snap.ContentDigest, snap.SnapshotPath,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("save skill snapshot: %w", err)
	}
	return snap, nil
}
