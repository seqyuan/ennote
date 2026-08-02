package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
)

type SkillSnapshotRepo struct{ DB *sql.DB }

type SkillSnapshot struct {
	ID             string
	RunID          string
	SkillID        string
	RelPath        string
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

// SaveCatalog saves all materialized skill records in a single transaction.
func (r *SkillSnapshotRepo) SaveCatalog(
	ctx context.Context,
	runID string,
	records []skills.MaterializedSkillRecord,
) error {
	if len(records) == 0 {
		return nil
	}

	// Validate records
	seen := map[string]bool{}
	for _, rec := range records {
		if rec.RelPath == "" {
			return fmt.Errorf("save catalog: RelPath must not be empty")
		}
		if rec.SkillID == "" {
			return fmt.Errorf("save catalog: SkillID must not be empty for %s", rec.RelPath)
		}
		if rec.ContentDigest == "" {
			return fmt.Errorf("save catalog: ContentDigest must not be empty for %s", rec.RelPath)
		}
		if seen[rec.RelPath] {
			return fmt.Errorf("save catalog: duplicate RelPath %q in batch", rec.RelPath)
		}
		seen[rec.RelPath] = true

		// Validate snapshot_path suffix matches rel_path
		normalized := strings.ReplaceAll(rec.SnapshotPath, "\\", "/")
		if !strings.HasSuffix(normalized, "/"+rec.RelPath) {
			return fmt.Errorf("save catalog: snapshot_path %q must end with /%s", rec.SnapshotPath, rec.RelPath)
		}
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save catalog: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Delete existing rows for this run
	if _, err := tx.ExecContext(ctx, `DELETE FROM skill_snapshots WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("save catalog: delete existing: %w", err)
	}

	// Sort records by RelPath for deterministic insertion
	sorted := make([]skills.MaterializedSkillRecord, len(records))
	copy(sorted, records)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].RelPath < sorted[j].RelPath
	})

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, rec := range sorted {
		id := uuid.NewString()
		_, err := tx.ExecContext(ctx,
			`INSERT INTO skill_snapshots (id, run_id, skill_id, rel_path, version, manifest_digest, content_digest, snapshot_path, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, runID, rec.SkillID, rec.RelPath, rec.Version,
			rec.ManifestHash, rec.ContentDigest, rec.SnapshotPath, now,
		)
		if err != nil {
			return fmt.Errorf("save catalog: insert %s: %w", rec.RelPath, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save catalog: commit: %w", err)
	}
	return nil
}
