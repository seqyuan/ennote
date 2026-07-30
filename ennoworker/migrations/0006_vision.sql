CREATE TABLE IF NOT EXISTS image_descriptions (
    id                          TEXT PRIMARY KEY,
    artifact_id                 TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    artifact_sha256             TEXT NOT NULL,
    descriptor_model_profile_id TEXT NOT NULL REFERENCES model_profiles(id),
    actual_model                TEXT NOT NULL DEFAULT '',
    prompt_version              TEXT NOT NULL,
    description                 TEXT NOT NULL,
    description_sha256          TEXT NOT NULL,
    model_call_id               TEXT REFERENCES model_calls(id),
    created_at                  TEXT NOT NULL,
    UNIQUE(artifact_sha256, descriptor_model_profile_id, prompt_version)
);

CREATE INDEX IF NOT EXISTS ix_artifacts_kind_sha256
    ON artifacts(kind, sha256);
CREATE INDEX IF NOT EXISTS ix_image_descriptions_artifact
    ON image_descriptions(artifact_id, created_at);
