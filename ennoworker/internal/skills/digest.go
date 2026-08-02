package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const (
	digestSchemaVersion = 1
)

type entryType byte

const (
	entryFile entryType = 'f'
	entryDir  entryType = 'd'
)

// treeDigest computes a deterministic, normalized digest over all files and
// directories in a tree. Each entry commits: schema version, relative path,
// type ('f' or 'd'), octal mode permissions, content length, and file bytes.
// Directories have zero-length content. Symlinks, devices, FIFOs, and
// non-regular files cause an error.
//
// File contents are hashed in streaming fashion (one file at a time) to keep
// memory bounded even when attachments are large. Entries are processed in
// deterministic relative-path order.
func treeDigest(root string) (string, error) {
	type entry struct {
		relPath string
		isDir   bool
		mode    os.FileMode
		size    int64
	}

	var entries []entry
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Skip the root "." entry to make digest independent of
		// the parent directory's mode (temp dir vs materialized dir).
		if rel == "." {
			return nil
		}

		if d.IsDir() {
			entries = append(entries, entry{
				relPath: rel,
				isDir:   true,
				mode:    info.Mode().Perm(),
			})
			return nil
		}

		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file in tree: %s (mode=%o)", rel, info.Mode())
		}

		entries = append(entries, entry{
			relPath: rel,
			isDir:   false,
			mode:    info.Mode().Perm(),
			size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("tree digest: walk %s: %w", root, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].relPath < entries[j].relPath
	})

	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%d\x00%s\x00", digestSchemaVersion, filepath.ToSlash(e.relPath))
		if e.isDir {
			h.Write([]byte{byte(entryDir), 0})
			fmt.Fprintf(h, "%o\x00", e.mode)
			h.Write([]byte{0}) // zero-length content
			continue
		}
		h.Write([]byte{byte(entryFile), 0})
		fmt.Fprintf(h, "%o\x00", e.mode)
		fmt.Fprintf(h, "%d\x00", e.size)

		// Stream the file content into the hash without buffering it whole.
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(e.relPath)))
		if err != nil {
			return "", fmt.Errorf("tree digest: open %s: %w", e.relPath, err)
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", fmt.Errorf("tree digest: read %s: %w", e.relPath, err)
		}
		f.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeLeafDigest computes the normalized tree digest for a leaf skill
// directory. It reports the hash in hex.
func ComputeLeafDigest(dir string) (string, error) {
	return treeDigest(dir)
}

// ComputeCategoryDigest computes the SHA-256 digest of the given content bytes.
func ComputeCategoryDigest(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

const (
	sourceSchemaTag   = "SRC-CATALOG:"
	snapshotSchemaTag = "SNAP-CATALOG:"
	catalogSchemaTag  = "CATALOG:"
)

// SourceCatalogDigest commits the sorted kind, RelPath, source name, and
// source digest of all nodes in the catalog.
func SourceCatalogDigest(entries []CatalogManifestEntry) string {
	h := sha256.New()
	h.Write([]byte(sourceSchemaTag))
	fmt.Fprintf(h, "%d\x00", digestSchemaVersion)
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00",
			e.Kind, e.RelPath, e.SourceName, e.SourceDigest)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SnapshotCatalogDigest commits the sorted kind, RelPath, sandbox mode, and
// snapshot digest of all nodes in the catalog.
func SnapshotCatalogDigest(entries []CatalogManifestEntry, mode string) string {
	h := sha256.New()
	h.Write([]byte(snapshotSchemaTag))
	fmt.Fprintf(h, "%d\x00%s\x00", digestSchemaVersion, mode)
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00",
			e.Kind, e.RelPath, e.SnapshotDigest)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CatalogDigest commits everything in the canonical manifest EXCEPT the
// CatalogDigest field itself: schema version, mode, entries with both
// source and snapshot fields, SourceCatalogDigest, and SnapshotCatalogDigest.
func CatalogDigest(entries []CatalogManifestEntry, mode, sourceDigest, snapDigest string) string {
	h := sha256.New()
	h.Write([]byte(catalogSchemaTag))
	fmt.Fprintf(h, "%d\x00%s\x00", digestSchemaVersion, mode)
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00",
			e.Kind, e.RelPath, e.SourceName, e.SourceDigest, e.SnapshotDigest, e.SnapshotMode)
	}
	h.Write([]byte(sourceDigest))
	h.Write([]byte{0})
	h.Write([]byte(snapDigest))
	return hex.EncodeToString(h.Sum(nil))
}
