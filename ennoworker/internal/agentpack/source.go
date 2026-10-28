package agentpack

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
)

const (
	// MaxPayloadBytes bounds the unpacked payload of a pack (and the archive
	// file itself).
	MaxPayloadBytes = 32 << 20
	// MaxFiles bounds the number of regular files in a pack.
	MaxFiles = 512
	// MaxArchiveEntries bounds tar entries including directories.
	MaxArchiveEntries = 1024
	// maxArchiveStreamBytes bounds the decompressed tar stream: the payload
	// ceiling plus the worst-case header/padding overhead of MaxArchiveEntries.
	maxArchiveStreamBytes = MaxPayloadBytes + (1 << 20)
	// MaxFileBytes bounds one pack file.
	MaxFileBytes = 8 << 20
	// MaxFlowSourceBytes bounds one flow source document.
	MaxFlowSourceBytes = 512 << 10
	// MaxRoleSourceBytes bounds one role source document.
	MaxRoleSourceBytes = 128 << 10
	// MaxDocumentBytes bounds one document echoed back by inspect.
	MaxDocumentBytes = 256 << 10
	// MaxDocumentsBytes bounds the total documents echoed back by inspect.
	MaxDocumentsBytes = 1 << 20
)

// Container kinds.
const (
	ContainerDirectory = "directory"
	ContainerArchive   = "archive"
)

// Container describes how the pack was delivered. Bytes is the uncompressed
// payload size for a directory pack and the archive file size for a .enpack.
type Container struct {
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes"`
}

// File is one regular file of a pack, held entirely in memory.
type File struct {
	Path string
	Data []byte
}

// Package is the in-memory, path-safe view of a pack payload. Reading never
// extracts an archive and never writes a temp directory.
type Package struct {
	Container   Container
	Files       []File
	payload     int64
	directories map[string]bool
	byPath      map[string]int
}

// Lookup returns the file at a canonical pack path.
func (p *Package) Lookup(path string) (File, bool) {
	if p == nil {
		return File{}, false
	}
	index, ok := p.byPath[path]
	if !ok {
		return File{}, false
	}
	return p.Files[index], true
}

// HasDirectory reports whether the pack declares the directory path. The pack
// root is always present.
func (p *Package) HasDirectory(path string) bool {
	if p == nil {
		return false
	}
	return path == "" || p.directories[path]
}

// FilesUnder returns every file below a directory (excluding the directory's
// implicit parent entries), ordered by path.
func (p *Package) FilesUnder(directory string) []File {
	if p == nil {
		return nil
	}
	prefix := directory + "/"
	result := make([]File, 0, len(p.Files))
	for _, file := range p.Files {
		if strings.HasPrefix(file.Path, prefix) {
			result = append(result, file)
		}
	}
	return result
}

func newPackage(kind string) *Package {
	return &Package{
		Container:   Container{Kind: kind},
		directories: map[string]bool{"": true},
		byPath:      map[string]int{},
	}
}

func (p *Package) add(path string, data []byte) error {
	if _, exists := p.byPath[path]; exists {
		return pathError(CodePackPathInvalid, path, "duplicate pack entry")
	}
	if p.directories[path] {
		return pathError(CodePackPathInvalid, path, "pack path is both a file and a directory")
	}
	if len(p.Files) >= MaxFiles {
		return errorf(CodePackTooManyFiles, "pack declares more than %d files", MaxFiles)
	}
	p.payload += int64(len(data))
	if p.payload > MaxPayloadBytes {
		return errorf(CodePackTooLarge, "pack payload exceeds %d bytes", MaxPayloadBytes)
	}
	p.byPath[path] = len(p.Files)
	p.Files = append(p.Files, File{Path: path, Data: data})
	p.markParents(path)
	return nil
}

func (p *Package) addDirectory(path string) error {
	if path == "" {
		return nil
	}
	if _, exists := p.byPath[path]; exists {
		return pathError(CodePackPathInvalid, path, "pack path is both a directory and a file")
	}
	p.directories[path] = true
	p.markParents(path)
	return nil
}

func (p *Package) markParents(path string) {
	for {
		index := strings.LastIndexByte(path, '/')
		if index < 0 {
			return
		}
		path = path[:index]
		if p.directories[path] {
			return
		}
		p.directories[path] = true
	}
}

// ReadPath loads a pack from a directory or a .enpack archive. It never follows
// symbolic links, never accepts special files, and never writes to disk.
func ReadPath(path string) (*Package, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, pathError(CodePackNotFound, path, "pack path does not exist")
		}
		return nil, pathError(CodePackNotFound, path, "stat pack path: %v", err)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, pathError(CodePackSymlinkRejected, path, "pack path must not be a symbolic link")
	case info.IsDir():
		return readDirectory(path)
	case info.Mode().IsRegular():
		if !strings.HasSuffix(path, ".enpack") {
			return nil, pathError(CodePackFormatUnsupported, path,
				"pack file must use the .enpack extension")
		}
		return readArchive(path, info.Size())
	default:
		return nil, pathError(CodePackFormatUnsupported, path, "pack path must be a directory or a .enpack file")
	}
}

func readDirectory(rootPath string) (*Package, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, pathError(CodePackNotFound, rootPath, "open pack directory: %v", err)
	}
	defer func() { _ = root.Close() }()

	pkg := newPackage(ContainerDirectory)
	directories := 0
	walkErr := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return pathError(CodePackPathKindUnsupported, name, "read pack directory: %v", err)
		}
		if name == "." {
			return nil
		}
		path, err := normalizePackPath(name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return pathError(CodePackPathKindUnsupported, path, "stat pack entry: %v", err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return pathError(CodePackSymlinkRejected, path, "pack must not contain symbolic links")
		}
		if entry.IsDir() {
			directories++
			if directories > MaxArchiveEntries {
				return errorf(CodePackTooManyFiles, "pack declares more than %d entries", MaxArchiveEntries)
			}
			return pkg.addDirectory(path)
		}
		if !info.Mode().IsRegular() {
			return pathError(CodePackPathKindUnsupported, path, "pack entry must be a regular file or directory")
		}
		if info.Size() > MaxFileBytes {
			return pathError(CodePackTooLarge, path,
				"file is %d bytes (limit %d)", info.Size(), MaxFileBytes)
		}
		data, err := readFile(root, path, MaxFileBytes)
		if err != nil {
			return err
		}
		return pkg.add(path, data)
	})
	if walkErr != nil {
		var packErr *Error
		if errors.As(walkErr, &packErr) {
			return nil, packErr
		}
		return nil, pathError(CodePackPathKindUnsupported, rootPath, "read pack directory: %v", walkErr)
	}
	pkg.Container.Bytes = pkg.payload
	return pkg, nil
}

func readFile(root *os.Root, path string, limit int64) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, pathError(CodePackPathKindUnsupported, path, "open pack file: %v", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, pathError(CodePackPathKindUnsupported, path, "read pack file: %v", err)
	}
	if int64(len(data)) > limit {
		return nil, pathError(CodePackTooLarge, path, "file exceeds %d bytes", limit)
	}
	return data, nil
}

func readArchive(path string, size int64) (*Package, error) {
	if size > MaxPayloadBytes {
		return nil, pathError(CodePackTooLarge, path,
			"archive is %d bytes (limit %d)", size, MaxPayloadBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, pathError(CodePackNotFound, path, "pack file does not exist")
		}
		return nil, pathError(CodePackUnsupportedContainer, path, "open pack file: %v", err)
	}
	defer func() { _ = file.Close() }()

	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, pathError(CodePackUnsupportedContainer, path, "pack file is not a gzip archive")
	}
	defer func() { _ = compressed.Close() }()

	pkg := newPackage(ContainerArchive)
	pkg.Container.Bytes = size
	reader := tar.NewReader(&payloadReader{reader: io.LimitReader(compressed, maxArchiveStreamBytes)})
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var packErr *Error
			if errors.As(err, &packErr) {
				return nil, packErr
			}
			return nil, pathError(CodePackUnsupportedContainer, path, "read pack archive: %v", err)
		}
		entries++
		if entries > MaxArchiveEntries {
			return nil, errorf(CodePackTooManyFiles, "pack archive declares more than %d entries", MaxArchiveEntries)
		}
		raw := strings.TrimSuffix(header.Name, "/")
		if raw == "." || raw == "./" || raw == "" {
			// Tolerate an explicit archive root marker.
			continue
		}
		entryPath, err := normalizePackPath(raw)
		if err != nil {
			return nil, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := pkg.addDirectory(entryPath); err != nil {
				return nil, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size > MaxFileBytes {
				return nil, pathError(CodePackTooLarge, entryPath,
					"file is %d bytes (limit %d)", header.Size, MaxFileBytes)
			}
			data, err := io.ReadAll(io.LimitReader(reader, MaxFileBytes+1))
			if err != nil {
				var packErr *Error
				if errors.As(err, &packErr) {
					return nil, packErr
				}
				return nil, pathError(CodePackUnsupportedContainer, entryPath, "read archive entry: %v", err)
			}
			if len(data) > MaxFileBytes {
				return nil, pathError(CodePackTooLarge, entryPath, "file exceeds %d bytes", MaxFileBytes)
			}
			if err := pkg.add(entryPath, data); err != nil {
				return nil, err
			}
		case tar.TypeSymlink, tar.TypeLink:
			return nil, pathError(CodePackSymlinkRejected, entryPath, "pack archive must not contain links")
		default:
			return nil, pathError(CodePackPathKindUnsupported, entryPath,
				"pack archive entry must be a regular file or directory")
		}
	}
	if pkg.payload > MaxPayloadBytes {
		return nil, pathError(CodePackTooLarge, path, "pack payload exceeds %d bytes", MaxPayloadBytes)
	}
	return pkg, nil
}

// payloadReader enforces the unpacked-payload ceiling while the tar reader
// streams entries; it is the archive analogue of the directory walk's total
// size accounting.
type payloadReader struct {
	reader io.Reader
	read   int64
}

func (r *payloadReader) Read(buffer []byte) (int, error) {
	read, err := r.reader.Read(buffer)
	r.read += int64(read)
	if r.read > maxArchiveStreamBytes {
		return read, errorf(CodePackTooLarge, "pack archive expands beyond %d bytes", MaxPayloadBytes)
	}
	return read, err
}
