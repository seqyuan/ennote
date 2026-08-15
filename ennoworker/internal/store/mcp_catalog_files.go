package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (r *MCPCatalogRepo) cachePath(row MCPCatalogCacheRow) string {
	key := strings.Join([]string{row.BindingID, strconv.Itoa(row.BindingRevision), strconv.Itoa(row.AuthGeneration),
		row.ProfileVersionID, row.ProtocolVersion, row.CredentialDigest}, "\x00")
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(r.CacheDir, hex.EncodeToString(digest[:])+".json")
}

func (r *MCPCatalogRepo) putCatalogFile(row MCPCatalogCacheRow) error {
	if err := os.MkdirAll(r.CacheDir, 0o700); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	temporary, err := os.CreateTemp(r.CacheDir, ".catalog-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
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
	return os.Rename(name, r.cachePath(row))
}

func (r *MCPCatalogRepo) getCatalogFile(bindingID string, bindingRevision, authGeneration int,
	profileVersionID, protocolVersion, credentialDigest string) (*MCPCatalogCacheRow, error) {
	probe := MCPCatalogCacheRow{BindingID: bindingID, BindingRevision: bindingRevision, AuthGeneration: authGeneration,
		ProfileVersionID: profileVersionID, ProtocolVersion: protocolVersion, CredentialDigest: credentialDigest}
	contents, err := os.ReadFile(r.cachePath(probe))
	if os.IsNotExist(err) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	var row MCPCatalogCacheRow
	if err := json.Unmarshal(contents, &row); err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *MCPCatalogRepo) markCatalogFilesStale(bindingID string, authGeneration int) error {
	entries, err := os.ReadDir(r.CacheDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(r.CacheDir, entry.Name())
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var row MCPCatalogCacheRow
		if json.Unmarshal(contents, &row) != nil || row.BindingID != bindingID || row.AuthGeneration != authGeneration {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
