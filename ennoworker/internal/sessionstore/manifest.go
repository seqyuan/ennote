package sessionstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

const ManifestSchemaVersion = 1

type Manifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	SessionID     string    `json:"sessionId"`
	ProjectID     string    `json:"projectId"`
	CreatedAt     time.Time `json:"createdAt"`
}

func readManifest(path string) (Manifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Manifest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Manifest{}, fmt.Errorf("session manifest must be a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode session manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fmt.Errorf("session manifest must contain one JSON value")
	}
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.SessionID == "" || manifest.ProjectID == "" || manifest.CreatedAt.IsZero() {
		return Manifest{}, fmt.Errorf("session manifest is incomplete or unsupported")
	}
	return manifest, nil
}

func writeManifest(path string, manifest Manifest) error {
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
