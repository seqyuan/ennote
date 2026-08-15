package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const LayoutVersion = 2

type Layout struct {
	Home         string
	Marker       string
	Config       string
	Models       string
	ProviderAuth string
	Settings     string
	Policies     string
	MCP          string
	Agents       string
	Roles        string
	Graphs       string
	Skills       string
	Projects     string
	Data         string
	CatalogDB    string
	UsageDB      string
	Cache        string
	MCPCache     string
	Runtime      string
	WorkerState  string
	LegacyDB     string
}

type marker struct {
	SchemaVersion int       `json:"schemaVersion"`
	InitializedAt time.Time `json:"initializedAt"`
}

func ForHome(home string) Layout {
	home = filepath.Clean(home)
	configDir := filepath.Join(home, "config")
	agentsDir := filepath.Join(home, "agents")
	dataDir := filepath.Join(home, "data")
	cacheDir := filepath.Join(home, "cache")
	runtimeDir := filepath.Join(home, "runtime")
	return Layout{
		Home:         home,
		Marker:       filepath.Join(home, "storage-layout.json"),
		Config:       configDir,
		Models:       filepath.Join(configDir, "models.json"),
		ProviderAuth: filepath.Join(configDir, "provider-auth.json"),
		Settings:     filepath.Join(configDir, "settings.json"),
		Policies:     filepath.Join(configDir, "policies.json"),
		MCP:          filepath.Join(configDir, "mcp.json"),
		Agents:       agentsDir,
		Roles:        filepath.Join(agentsDir, "roles"),
		Graphs:       filepath.Join(agentsDir, "graphs"),
		Skills:       filepath.Join(home, "skills"),
		Projects:     filepath.Join(home, "projects"),
		Data:         dataDir,
		CatalogDB:    filepath.Join(dataDir, "catalog.db"),
		UsageDB:      filepath.Join(dataDir, "usage.db"),
		Cache:        cacheDir,
		MCPCache:     filepath.Join(cacheDir, "mcp"),
		Runtime:      runtimeDir,
		WorkerState:  filepath.Join(runtimeDir, "worker-state.json"),
		LegacyDB:     filepath.Join(dataDir, "ennote.db"),
	}
}

// Bootstrap removes the unsupported legacy database, creates the V2 directory
// skeleton, runs initialize, and writes the layout marker last. The caller must
// invoke Bootstrap before opening any Worker business store.
func Bootstrap(home string, initialize func(Layout) error) (Layout, error) {
	layout := ForHome(home)
	if err := removeLegacy(layout.LegacyDB); err != nil {
		return Layout{}, err
	}
	for _, directory := range []string{
		layout.Home, layout.Config, layout.Agents, layout.Roles, layout.Graphs,
		layout.Skills, layout.Projects, layout.Data, layout.Cache, layout.MCPCache,
		layout.Runtime,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return Layout{}, fmt.Errorf("create storage directory %s: %w", directory, err)
		}
	}
	if err := validateMarker(layout.Marker); err != nil {
		return Layout{}, err
	}
	if initialize != nil {
		if err := initialize(layout); err != nil {
			return Layout{}, fmt.Errorf("initialize storage layout: %w", err)
		}
	}
	if _, err := os.Stat(layout.Marker); errors.Is(err, os.ErrNotExist) {
		contents, marshalErr := json.MarshalIndent(marker{
			SchemaVersion: LayoutVersion,
			InitializedAt: time.Now().UTC(),
		}, "", "  ")
		if marshalErr != nil {
			return Layout{}, fmt.Errorf("encode storage layout marker: %w", marshalErr)
		}
		contents = append(contents, '\n')
		if err := writeAtomic(layout.Marker, contents, 0o600); err != nil {
			return Layout{}, fmt.Errorf("write storage layout marker: %w", err)
		}
	} else if err != nil {
		return Layout{}, fmt.Errorf("stat storage layout marker: %w", err)
	}
	return layout, nil
}

func removeLegacy(databasePath string) error {
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete unsupported legacy store %s: %w", path, err)
		}
	}
	return nil
}

func validateMarker(path string) error {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read storage layout marker: %w", err)
	}
	var value marker
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode storage layout marker: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("storage layout marker must contain one JSON value")
	}
	if value.SchemaVersion != LayoutVersion {
		return fmt.Errorf("unsupported storage layout version %d", value.SchemaVersion)
	}
	if value.InitializedAt.IsZero() {
		return fmt.Errorf("storage layout marker initializedAt is required")
	}
	return nil
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
