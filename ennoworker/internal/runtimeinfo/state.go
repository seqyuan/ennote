package runtimeinfo

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const StateVersion = 1

// WorkerState is the owner-only handshake record shared by ennogate and ennoworker.
type WorkerState struct {
	Version        int       `json:"version"`
	URL            string    `json:"url"`
	PID            int       `json:"pid"`
	InstanceID     string    `json:"instanceId"`
	BootstrapToken string    `json:"bootstrapToken"`
	StartedAt      time.Time `json:"startedAt"`
}

func NewInstanceID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate runtime instance ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func Load(path string) (*WorkerState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state WorkerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode worker state: %w", err)
	}
	if state.Version != StateVersion || state.URL == "" || state.PID <= 0 || state.InstanceID == "" || state.BootstrapToken == "" {
		return nil, errors.New("worker state is incomplete or unsupported")
	}
	return &state, nil
}

func WriteAtomic(path string, state WorkerState) error {
	if state.Version != StateVersion || state.URL == "" || state.PID <= 0 || state.InstanceID == "" || state.BootstrapToken == "" {
		return errors.New("worker state is incomplete")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create worker state directory: %w", err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode worker state: %w", err)
	}
	temporary := path + ".tmp-" + state.InstanceID
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary worker state: %w", err)
	}
	keepTemporary := true
	defer func() {
		_ = file.Close()
		if keepTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(encoded); err != nil {
		return fmt.Errorf("write worker state: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync worker state: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close worker state: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish worker state: %w", err)
	}
	keepTemporary = false
	return nil
}

func RemoveIfOwner(path string, pid int, instanceID string) error {
	state, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if state.PID != pid || state.InstanceID != instanceID {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove worker state: %w", err)
	}
	return nil
}
