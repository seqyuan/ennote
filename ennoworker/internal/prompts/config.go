package prompts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// maxConfigBytes is the read limit: 1 MiB + 1 sentinel byte.
	maxConfigBytes  = 1*1024*1024 + 1
	maxSettingsPath = 4 * 1024 // 4 KiB UTF-8 bytes
	maxSettingsN    = 32
)

// LoadConfigPaths reads $ENNOTE_HOME/config.json with a bounded read
// (1 MiB + 1), extracts prompts.paths, and normalises relative paths against
// homeDir. Returns nil when the file does not exist or has no prompts.paths.
func LoadConfigPaths(homeDir string) ([]string, error) {
	configPath := filepath.Join(homeDir, "config.json")

	f, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open config.json: %w", err)
	}
	defer f.Close()

	data, err := readBounded(int(f.Fd()), maxConfigBytes-1)
	if err != nil {
		return nil, fmt.Errorf("config.json: %v", err)
	}

	// Decode top-level as raw map to ignore unknown keys.
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("config.json: invalid JSON: %v", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("config.json: multiple JSON values")
	}

	promptsRaw, ok := raw["prompts"]
	if !ok {
		return nil, nil
	}

	var promptSection struct {
		Paths []string `json:"paths"`
	}
	dec2 := json.NewDecoder(bytes.NewReader(promptsRaw))
	dec2.DisallowUnknownFields()
	if err := dec2.Decode(&promptSection); err != nil {
		return nil, fmt.Errorf("config.json: prompts: invalid: %v", err)
	}
	if dec2.More() {
		return nil, fmt.Errorf("config.json: prompts: multiple JSON values")
	}

	paths := promptSection.Paths
	if paths == nil {
		return nil, nil
	}
	if len(paths) > maxSettingsN {
		return nil, fmt.Errorf("config.json: prompts.paths has %d entries (max %d)", len(paths), maxSettingsN)
	}

	// Validate individual paths.
	for i, p := range paths {
		if len(p) > maxSettingsPath {
			return nil, fmt.Errorf("config.json: prompts.paths[%d] exceeds 4 KiB limit", i)
		}
		if !filepath.IsAbs(p) {
			paths[i] = filepath.Join(homeDir, p)
		}
	}

	return paths, nil
}
