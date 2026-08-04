package prompts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Store error sentinels.
var (
	ErrPromptTemplateExists   = errors.New("prompt template already exists")
	ErrPromptTemplateLimit    = errors.New("global prompt template limit reached")
	ErrPromptTemplateInvalid  = errors.New("prompt template file is invalid")
	ErrPromptTemplateNotFound = errors.New("prompt template not found")
	ErrRecoveryMode           = errors.New("global store in recovery mode")
)

// GlobalStore manages CRUD on $ENNOTE_HOME/prompts/*.md using POSIX dir-FD
// no-follow primitives.
type GlobalStore struct {
	mu     sync.RWMutex
	rootFD int
	dir    string
}

// OpenGlobalStore opens homeDir/prompts and cleans stale temp files.
func OpenGlobalStore(homeDir string) (*GlobalStore, error) {
	dir := filepath.Join(homeDir, "prompts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create prompts dir: %w", err)
	}

	fd, err := openDirAt(posixATFDCWD, dir)
	if err != nil {
		return nil, fmt.Errorf("open prompts dir: %w", err)
	}

	store := &GlobalStore{rootFD: fd, dir: dir}
	if err := store.cleanupTempFiles(); err != nil {
		closeFD(fd)
		return nil, fmt.Errorf("cleanup temp files: %w", err)
	}
	return store, nil
}

func (s *GlobalStore) Close() error { closeFD(s.rootFD); return nil }
func closeFd(fd int)                { closeFD(fd) }

// cleanupTempFiles removes stale .ennote-prompt-tmp-* entries.
func (s *GlobalStore) cleanupTempFiles() error {
	entries, _, err := readDirAt(s.rootFD, 2000)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		fd, err := openFileAt(s.rootFD, e.Name())
		if err != nil {
			continue
		}
		ok, err := fstatIsRegular(fd)
		closeFd(fd)
		if err != nil || !ok {
			continue
		}
		_ = unlinkAt(s.rootFD, e.Name())
	}
	return nil
}

const tempPrefix = ".ennote-prompt-tmp-"

// ——— helpers ———

func statExists(rootFD int, name string) bool {
	fd, err := openFileAt(rootFD, name)
	if err != nil {
		return false
	}
	closeFd(fd)
	return true
}

func openAndValidate(rootFD int, name string) (fd int, err error) {
	fd, err = openFileAt(rootFD, name)
	if err != nil {
		if isNoSuchFile(err) {
			return -1, fmt.Errorf("%w: %s", ErrPromptTemplateNotFound, name)
		}
		return -1, err
	}
	ok, err := fstatIsRegular(fd)
	if err != nil || !ok {
		closeFd(fd)
		return -1, fmt.Errorf("%w: not a regular file", ErrPromptTemplateInvalid)
	}
	owned, nlink, err := fstatOwner(fd)
	if err != nil || !owned || nlink != 1 {
		closeFd(fd)
		return -1, fmt.Errorf("%w: not owned or link count != 1", ErrPromptTemplateInvalid)
	}
	return fd, nil
}

// ——— CRUD ———

func (s *GlobalStore) Create(name, description, argumentHint, body string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	basename := name + ".md"

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, atLimit, err := readDirAt(s.rootFD, 2000)
	if err != nil {
		return fmt.Errorf("read dir: %w", err)
	}
	if len(entries) >= 2000 || atLimit {
		return fmt.Errorf("%w: %d entries", ErrPromptTemplateLimit, len(entries))
	}
	if statExists(s.rootFD, basename) {
		return fmt.Errorf("%w: %s", ErrPromptTemplateExists, basename)
	}

	serialized, err := serializeTemplate(name, description, argumentHint, body)
	if err != nil {
		return err
	}
	if len(serialized) > maxTemplateBytes {
		return fmt.Errorf("%w", ErrPromptTemplateTooLarge)
	}

	tmpFD, tmpName, err := createTempAt(s.rootFD, tempPrefix, 0600)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	writeOK := false
	defer func() {
		if !writeOK {
			closeFd(tmpFD)
			unlinkAt(s.rootFD, tmpName)
		}
	}()

	if err := writeFull(tmpFD, serialized); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := fsyncFD(tmpFD); err != nil {
		return fmt.Errorf("fsync temp: %w", err)
	}
	closeFd(tmpFD)

	if err := linkAt(s.rootFD, tmpName, basename); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return fmt.Errorf("%w: %s", ErrPromptTemplateExists, basename)
		}
		return fmt.Errorf("link: %w", err)
	}
	writeOK = true
	unlinkAt(s.rootFD, tmpName)
	syncDir(s.rootFD)
	return nil
}

func (s *GlobalStore) Get(name string) (Template, error) {
	if err := ValidateName(name); err != nil {
		return Template{}, err
	}
	basename := name + ".md"

	s.mu.RLock()
	defer s.mu.RUnlock()

	fd, err := openAndValidate(s.rootFD, basename)
	if err != nil {
		return Template{}, err
	}
	defer closeFd(fd)

	data, err := readBounded(fd, maxTemplateBytes)
	if err != nil {
		return Template{}, fmt.Errorf("read: %w", err)
	}

	tmpl, err := ParseTemplate(data, basename)
	if err != nil {
		return Template{}, fmt.Errorf("%w: %w", ErrPromptTemplateInvalid, err)
	}
	tmpl.Tier = TierGlobal
	tmpl.Source = "global"
	tmpl.Path = filepath.Join(s.dir, basename)
	return tmpl, nil
}

func (s *GlobalStore) Update(name, description, argumentHint, body string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	basename := name + ".md"

	s.mu.Lock()
	defer s.mu.Unlock()

	fd, err := openAndValidate(s.rootFD, basename)
	if err != nil {
		return err
	}
	closeFd(fd)

	serialized, err := serializeTemplate(name, description, argumentHint, body)
	if err != nil {
		return err
	}
	if len(serialized) > maxTemplateBytes {
		return fmt.Errorf("%w", ErrPromptTemplateTooLarge)
	}

	tmpFD, tmpName, err := createTempAt(s.rootFD, tempPrefix, 0600)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	writeOK := false
	defer func() {
		if !writeOK {
			closeFd(tmpFD)
			unlinkAt(s.rootFD, tmpName)
		}
	}()

	if err := writeFull(tmpFD, serialized); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := fsyncFD(tmpFD); err != nil {
		return fmt.Errorf("fsync temp: %w", err)
	}
	closeFd(tmpFD)

	if err := renameAt(s.rootFD, tmpName, basename); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	writeOK = true
	unlinkAt(s.rootFD, tmpName)
	syncDir(s.rootFD)
	return nil
}

func (s *GlobalStore) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	basename := name + ".md"

	s.mu.Lock()
	defer s.mu.Unlock()

	// Stat to confirm existence; unlink removes the entry itself.
	if !statExists(s.rootFD, basename) {
		return fmt.Errorf("%w: %s", ErrPromptTemplateNotFound, name)
	}
	return unlinkAt(s.rootFD, basename)
}

// ——— listing ———

type ListResult struct {
	Templates     []TemplateSummary
	GlobalEntries []GlobalPromptTemplateEntry
	RecoveryMode  bool
	Diagnostics   []Diagnostic
}

func (s *GlobalStore) List() ListResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, atLimit, err := readDirAt(s.rootFD, 2000)
	if err != nil {
		return ListResult{
			Diagnostics: []Diagnostic{{
				Level: "warning", Code: "prompt_storage_unavailable",
				Source: "global", Message: fmt.Sprintf("cannot enumerate: %v", err),
			}},
		}
	}

	recoveryMode := len(entries) > 2000 || atLimit

	if recoveryMode {
		return s.recoveryList(entries)
	}
	return s.normalList(entries)
}

func (s *GlobalStore) recoveryList(entries []os.DirEntry) ListResult {
	result := ListResult{RecoveryMode: true}
	seen := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".md")
		if !isSafeBasename(base) || seen[base] {
			continue
		}
		seen[base] = true
		if len(result.GlobalEntries) >= 2000 {
			break
		}
		editable := s.isEditableEntry(e.Name())
		result.GlobalEntries = append(result.GlobalEntries, GlobalPromptTemplateEntry{
			Name:     base,
			Valid:    false,
			Editable: editable,
			Diagnostic: &Diagnostic{
				Level: "warning", Code: "global_recovery_required",
				Source: "global", Name: base,
				Message: "global store in recovery mode",
			},
		})
	}
	sort.Slice(result.GlobalEntries, func(i, j int) bool {
		return result.GlobalEntries[i].Name < result.GlobalEntries[j].Name
	})
	result.Diagnostics = append(result.Diagnostics, Diagnostic{
		Level:   "warning",
		Code:    "global_recovery_required",
		Source:  "global",
		Message: fmt.Sprintf("global prompts directory has %d+ entries; recovery mode", len(entries)),
	})
	return result
}

func (s *GlobalStore) normalList(entries []os.DirEntry) ListResult {
	var templates []TemplateSummary
	var globalEntries []GlobalPromptTemplateEntry
	var diags []Diagnostic
	seen := map[string]bool{}
	parseCount := 0

	sorted := make([]os.DirEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name() < sorted[j].Name() })

	for _, e := range sorted {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".md")
		if !isSafeBasename(base) {
			diags = append(diags, Diagnostic{
				Level:   "warning",
				Code:    "template_parse_error",
				Source:  "global",
				Name:    e.Name(),
				Message: "file name is not a valid template name",
			})
			continue
		}
		if seen[base] {
			continue
		}
		seen[base] = true

		editable := s.isEditableEntry(e.Name())

		if parseCount >= 500 {
			diags = append(diags, Diagnostic{
				Level: "warning", Code: "template_scan_limit",
				Source: "global", Name: base,
				Message: "exceeded 500 parse limit",
			})
			globalEntries = append(globalEntries, GlobalPromptTemplateEntry{
				Name: base, Valid: false, Editable: editable,
				Diagnostic: &Diagnostic{
					Level: "warning", Code: "template_scan_limit",
					Source: "global", Name: base,
					Message: "not parsed: exceeded 500 limit",
				},
			})
			continue
		}
		parseCount++

		data, err := readTemplateEntry(s.rootFD, e.Name())
		if err != nil {
			diags = append(diags, Diagnostic{
				Level: "warning", Code: "template_size_exceeded",
				Source: "global", Name: base,
				Message: fmt.Sprintf("read error: %v", err),
			})
			globalEntries = append(globalEntries, GlobalPromptTemplateEntry{
				Name: base, Valid: false, Editable: editable,
				Diagnostic: &Diagnostic{
					Level: "warning", Code: "template_size_exceeded",
					Source: "global", Name: base,
					Message: fmt.Sprintf("read error: %v", err),
				},
			})
			continue
		}

		tmpl, err := ParseTemplate(data, e.Name())
		if err != nil {
			diags = append(diags, Diagnostic{
				Level: "warning", Code: "template_parse_error",
				Source: "global", Name: base,
				Message: fmt.Sprintf("parse: %v", err),
			})
			globalEntries = append(globalEntries, GlobalPromptTemplateEntry{
				Name: base, Valid: false, Editable: editable,
				Diagnostic: &Diagnostic{
					Level: "warning", Code: "template_parse_error",
					Source: "global", Name: base,
					Message: fmt.Sprintf("parse: %v", err),
				},
			})
			continue
		}

		templates = append(templates, TemplateSummary{
			Name:         tmpl.Name,
			Description:  tmpl.Description,
			ArgumentHint: tmpl.ArgumentHint,
			Source:       "global",
			Editable:     true,
		})
		globalEntries = append(globalEntries, GlobalPromptTemplateEntry{
			Name:         tmpl.Name,
			Description:  tmpl.Description,
			ArgumentHint: tmpl.ArgumentHint,
			Valid:        true,
			Editable:     true,
		})
	}

	return ListResult{
		Templates:     templates,
		GlobalEntries: globalEntries,
		Diagnostics:   diags,
	}
}

func readTemplateEntry(rootFD int, name string) ([]byte, error) {
	fd, err := openFileAt(rootFD, name)
	if err != nil {
		return nil, err
	}
	defer closeFd(fd)
	return readBounded(fd, maxTemplateBytes)
}

func (s *GlobalStore) isEditableEntry(name string) bool {
	fd, err := openFileAt(s.rootFD, name)
	if err != nil {
		return false
	}
	defer closeFd(fd)
	ok, err := fstatIsRegular(fd)
	if err != nil || !ok {
		return false
	}
	owned, nlink, err := fstatOwner(fd)
	return owned && nlink == 1
}

func isSafeBasename(base string) bool {
	if len(base) == 0 || len(base) > 32 {
		return false
	}
	for i, r := range base {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				return false
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
				return false
			}
		}
	}
	return true
}

// ——— serialization ———

func serializeTemplate(name, description, argumentHint, body string) ([]byte, error) {
	fm := map[string]string{"name": name}
	if description != "" {
		fm["description"] = description
	}
	if argumentHint != "" {
		fm["argument-hint"] = argumentHint
	}
	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("marshal frontmatter: %w", err)
	}
	var out strings.Builder
	out.WriteString("---\n")
	out.Write(fmBytes)
	out.WriteString("---\n")
	out.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		out.WriteByte('\n')
	}
	return []byte(out.String()), nil
}
