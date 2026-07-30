package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type ImageDescriptionRepo struct{ DB *sql.DB }

func (r *ImageDescriptionRepo) Get(ctx context.Context, artifactSHA, modelID, promptVersion string) (string, bool, error) {
	var description string
	err := r.DB.QueryRowContext(ctx, `SELECT description FROM image_descriptions
		WHERE artifact_sha256=? AND descriptor_model_profile_id=? AND prompt_version=?`,
		artifactSHA, modelID, promptVersion).Scan(&description)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return description, err == nil, err
}

func (r *ImageDescriptionRepo) Put(ctx context.Context, image domain.ImageRef, modelID, actualModel, promptVersion, description, modelCallID string) error {
	digest := sha256.Sum256([]byte(description))
	_, err := r.DB.ExecContext(ctx, `INSERT INTO image_descriptions
		(id,artifact_id,artifact_sha256,descriptor_model_profile_id,actual_model,prompt_version,
		description,description_sha256,model_call_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(artifact_sha256,descriptor_model_profile_id,prompt_version) DO NOTHING`,
		uuid.NewString(), image.ArtifactID, image.SHA256, modelID, actualModel, promptVersion,
		description, hex.EncodeToString(digest[:]), nullableStr(modelCallID), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
