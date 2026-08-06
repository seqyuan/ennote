// Package skillsmgmt implements the skills management surface for the Web UI:
// listing the merged skill catalog with install annotations (pi-ecosystem lock
// files), toggling disable-model-invocation, and (in exec.go) driving the
// skills.sh CLI for search/install/update. It is loopback-only and never
// touches cloud credentials beyond optional read-only env (GITHUB_TOKEN).
package skillsmgmt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/skills"
)

// SourceInfo mirrors pi-web's SkillInfo.sourceInfo.
type SourceInfo struct {
	Source string `json:"source,omitempty"`
	Scope  string `json:"scope,omitempty"`
}

// InstallInfo mirrors pi-web's SkillInstallInfo.
type InstallInfo struct {
	Package            string `json:"package"`
	Scope              string `json:"scope"` // global | project
	Source             string `json:"source"`
	SourceType         string `json:"sourceType"`
	SkillsShURL        string `json:"skillsShUrl,omitempty"`
	SkillPath          string `json:"skillPath,omitempty"`
	Ref                string `json:"ref,omitempty"`
	VersionHash        string `json:"versionHash,omitempty"`
	CanCheckForUpdates bool   `json:"canCheckForUpdates"`
}

// AnnotatedSkill is a catalog skill plus pi-ecosystem install annotation.
type AnnotatedSkill struct {
	Name                   string       `json:"name"`
	Description            string       `json:"description"`
	FilePath               string       `json:"filePath"`
	BaseDir                string       `json:"baseDir"`
	DisableModelInvocation bool         `json:"disableModelInvocation"`
	SourceInfo             SourceInfo   `json:"sourceInfo"`
	SkillID                string       `json:"skillId"`
	RelPath                string       `json:"relPath"`
	Install                *InstallInfo `json:"install,omitempty"`
}

// Diagnostic mirrors the catalog diagnostic shape.
type Diagnostic struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	RelPath string `json:"relPath,omitempty"`
	Source  string `json:"source,omitempty"`
}

// ListResult is the skills list response.
type ListResult struct {
	Skills                 []AnnotatedSkill `json:"skills"`
	Diagnostics            []Diagnostic     `json:"diagnostics"`
	ProjectResourcesLoaded bool             `json:"projectResourcesLoaded"`
}

// Root is a user-configured additional skills directory (pi/claude/codex/...
// ecosystems or a custom path). Lower Priority wins on path conflicts.
type Root struct {
	Name     string
	Path     string
	Priority int
}

// Service exposes skills listing and frontmatter toggling. Roots are resolved
// by the caller: the ennote default root (priority 0), user-configured
// additional roots, and the builtin root (highest priority).
type Service struct {
	UserRoot        string // ennote default user skills root (priority 0)
	BuiltinRoot     string // builtin skills root (may be "")
	AdditionalRoots []Root // sorted by priority ascending; lower wins
	HomeDir         string // used to locate the global lock (~/.agents/.skill-lock.json)
}

// catalogSources flattens the configured roots into skills.SourceRoot entries.
func (s *Service) catalogSources() []skills.SourceRoot {
	sources := []skills.SourceRoot{{Name: "user", Path: s.UserRoot, Priority: 0}}
	used := map[string]bool{"user": true}
	for i, root := range s.AdditionalRoots {
		name := root.Name
		if name == "" {
			name = "root"
		}
		if used[name] {
			name = fmt.Sprintf("%s-%d", name, i)
		}
		used[name] = true
		priority := root.Priority
		if priority <= 0 {
			priority = 10 + i
		}
		sources = append(sources, skills.SourceRoot{Name: name, Path: root.Path, Priority: priority})
	}
	if s.BuiltinRoot != "" {
		sources = append(sources, skills.SourceRoot{Name: "builtin", Path: s.BuiltinRoot, Priority: 1000})
	}
	return sources
}

// List builds the merged catalog and annotates skills with install info from
// the global lock and (when workspaceDir is non-empty) the project lock.
func (s *Service) List(workspaceDir string) (*ListResult, error) {
	catalog := skills.BuildCatalog(s.catalogSources())

	result := &ListResult{
		Skills:      make([]AnnotatedSkill, 0, len(catalog.Skills)),
		Diagnostics: make([]Diagnostic, 0, len(catalog.Diagnostics)),
	}
	for _, diag := range catalog.Diagnostics {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Level:   diag.Level,
			Message: diag.Message,
			RelPath: diag.RelPath,
			Source:  diag.Source,
		})
	}

	globalEntries := readSkillLock(globalLockPath(s.HomeDir))
	var projectEntries map[string]skillLockEntry
	if workspaceDir != "" {
		projectEntries = readSkillLock(filepath.Join(workspaceDir, "skills-lock.json"))
	}

	globalRoot := filepath.Join(piAgentDir(s.HomeDir), "skills")
	projectRoot := filepath.Join(workspaceDir, ".pi", "skills")

	for _, loaded := range catalog.Skills {
		result.Skills = append(result.Skills, annotate(loaded, globalEntries, projectEntries, globalRoot, projectRoot))
	}
	// Project-scope skills live outside the catalog sources; enumerate them so
	// the management list (and project-scope install annotations) stays in sync
	// with pi-web's loader behavior.
	if workspaceDir != "" {
		for _, loaded := range skills.Discover(projectRoot) {
			loaded.SourceRoot = "project"
			loaded.RelPath = filepath.Base(loaded.BaseDir)
			result.Skills = append(result.Skills, annotate(loaded, globalEntries, projectEntries, globalRoot, projectRoot))
		}
	}
	sort.Slice(result.Skills, func(i, j int) bool {
		if result.Skills[i].RelPath != result.Skills[j].RelPath {
			return result.Skills[i].RelPath < result.Skills[j].RelPath
		}
		return result.Skills[i].SkillID < result.Skills[j].SkillID
	})
	return result, nil
}

func annotate(loaded *skills.LoadedSkill, globalEntries, projectEntries map[string]skillLockEntry, globalRoot, projectRoot string) AnnotatedSkill {
	disabled, _ := hasDisableModelInvocation(loaded.BaseDir)
	item := AnnotatedSkill{
		Name:                   loaded.Manifest.ID,
		Description:            loaded.Manifest.Description,
		FilePath:               filepath.Join(loaded.BaseDir, "SKILL.md"),
		BaseDir:                loaded.BaseDir,
		DisableModelInvocation: disabled,
		SourceInfo:             SourceInfo{Source: loaded.SourceRoot, Scope: loaded.SourceRoot},
		SkillID:                loaded.Manifest.ID,
		RelPath:                loaded.RelPath,
	}
	if install := annotateInstall(globalEntries, projectEntries, loaded, globalRoot, projectRoot); install != nil {
		item.Install = install
	}
	return item
}

// piAgentDir returns the pi agent directory under the given home.
func piAgentDir(home string) string {
	return filepath.Join(home, ".pi", "agent")
}

// globalLockPath matches the skills.sh CLI global lock location.
func globalLockPath(home string) string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "skills", ".skill-lock.json")
	}
	return filepath.Join(home, ".agents", ".skill-lock.json")
}

// ——— pi-web skill-lock.ts port ———

type skillLockEntry struct {
	Source          any `json:"source"`
	SourceType      any `json:"sourceType"`
	SkillPath       any `json:"skillPath"`
	Ref             any `json:"ref"`
	SkillFolderHash any `json:"skillFolderHash"`
	ComputedHash    any `json:"computedHash"`
}

type skillLockFile struct {
	Skills map[string]skillLockEntry `json:"skills"`
}

func readSkillLock(path string) map[string]skillLockEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]skillLockEntry{}
	}
	var parsed skillLockFile
	if err := json.Unmarshal(data, &parsed); err != nil || parsed.Skills == nil {
		return map[string]skillLockEntry{}
	}
	return parsed.Skills
}

func isWithin(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func findLockEntry(entries map[string]skillLockEntry, skillName string) (skillLockEntry, bool) {
	if entry, ok := entries[skillName]; ok {
		return entry, true
	}
	lower := strings.ToLower(skillName)
	for name, entry := range entries {
		if strings.ToLower(name) == lower {
			return entry, true
		}
	}
	return skillLockEntry{}, false
}

func normalizeSource(source, sourceType string) string {
	if sourceType != "github" {
		return strings.TrimSuffix(source, "/")
	}
	s := source
	for _, prefix := range []string{"git+", "https://github.com/", "git@github.com:"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	s = strings.TrimSuffix(s, ".git")
	return strings.TrimSuffix(s, "/")
}

func buildSkillsShURL(source, skillName string) string {
	if source == "" || strings.Contains(source, "://") || strings.HasPrefix(source, "git@") {
		return ""
	}
	parts := make([]string, 0, len(strings.Split(source, "/")))
	for _, part := range strings.Split(source, "/") {
		if part != "" {
			parts = append(parts, urlPathEscape(part))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "https://skills.sh/" + strings.Join(parts, "/") + "/" + urlPathEscape(skillName)
}

func urlPathEscape(s string) string {
	return strings.ReplaceAll(urlEncode(s), "%2F", "/")
}

func urlEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

func getInstallInfo(entries map[string]skillLockEntry, skillName, scope string) *InstallInfo {
	entry, ok := findLockEntry(entries, skillName)
	if !ok {
		return nil
	}
	source, ok := entry.Source.(string)
	if !ok || strings.TrimSpace(source) == "" {
		return nil
	}
	sourceType, _ := entry.SourceType.(string)
	normalized := normalizeSource(strings.TrimSpace(source), sourceType)
	if normalized == "" {
		return nil
	}
	skillPath, _ := entry.SkillPath.(string)
	ref, _ := entry.Ref.(string)
	var versionHash string
	if scope == "global" {
		if v, ok := entry.SkillFolderHash.(string); ok && v != "" {
			versionHash = v
		}
	} else {
		if v, ok := entry.ComputedHash.(string); ok && v != "" {
			versionHash = v
		}
	}
	isGitHub := sourceType == "github" && matchesOwnerRepo(normalized)
	hasComparable := scope == "global" || ref == ""
	return &InstallInfo{
		Package:            normalized + "@" + skillName,
		Scope:              scope,
		Source:             normalized,
		SourceType:         sourceType,
		SkillsShURL:        optionalString(sourceType != "local", buildSkillsShURL(normalized, skillName)),
		SkillPath:          optionalString(skillPath != "", skillPath),
		Ref:                optionalString(ref != "", ref),
		VersionHash:        optionalString(versionHash != "", versionHash),
		CanCheckForUpdates: isGitHub && skillPath != "" && versionHash != "" && hasComparable,
	}
}

func matchesOwnerRepo(source string) bool {
	if strings.Count(source, "/") != 1 {
		return false
	}
	for _, part := range strings.Split(source, "/") {
		if part == "" {
			return false
		}
	}
	return true
}

func optionalString(ok bool, value string) string {
	if !ok {
		return ""
	}
	return value
}

func annotateInstall(globalEntries, projectEntries map[string]skillLockEntry, loaded *skills.LoadedSkill, globalRoot, projectRoot string) *InstallInfo {
	base := loaded.BaseDir
	scope := ""
	var entries map[string]skillLockEntry
	switch {
	case globalRoot != "" && isWithin(base, globalRoot):
		scope, entries = "global", globalEntries
	case projectRoot != "" && isWithin(base, projectRoot):
		scope, entries = "project", projectEntries
	}
	if scope == "" {
		return nil
	}
	return getInstallInfo(entries, loaded.Manifest.ID, scope)
}

// ResolveDir maps a catalog relPath to the winning directory across the
// managed (default + additional) roots, mirroring catalog precedence: the
// lowest-priority root that actually contains the skill wins. Builtin roots
// are excluded (management operations never touch builtin skills).
func (s *Service) ResolveDir(relPath string) (string, bool) {
	clean := filepath.Clean(relPath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	roots := make([]Root, 0, 1+len(s.AdditionalRoots))
	roots = append(roots, Root{Name: "user", Path: s.UserRoot, Priority: 0})
	roots = append(roots, s.AdditionalRoots...)
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Priority < roots[j].Priority })
	for _, root := range roots {
		if root.Path == "" {
			continue
		}
		dir := filepath.Join(root.Path, filepath.FromSlash(clean))
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir, true
		}
	}
	return "", false
}

// PiGlobalRoot returns the pi-ecosystem global skills root managed by the
// skills.sh CLI (also where the global lock lives).
func (s *Service) PiGlobalRoot() string {
	return filepath.Join(piAgentDir(s.HomeDir), "skills")
}

// ——— disable-model-invocation ———

// skillMDPath resolves SKILL.md (case-insensitive) under dir.
func skillMDPath(dir string) (string, error) {
	for _, name := range []string{"SKILL.md", "skill.md"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no SKILL.md under %s", dir)
}

func hasDisableModelInvocation(dir string) (bool, error) {
	p, err := skillMDPath(dir)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	return frontmatterKeyTrue(string(data), "disable-model-invocation"), nil
}

// ToggleDisabled surgically adds/removes the disable-model-invocation key in
// the SKILL.md frontmatter, preserving all other formatting (pi-web PATCH).
func (s *Service) ToggleDisabled(dir string, disabled bool) (bool, error) {
	p, err := skillMDPath(dir)
	if err != nil {
		return false, err
	}
	content, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	text := string(content)
	already := frontmatterKeyTrue(text, "disable-model-invocation")

	var updated string
	switch {
	case disabled && !already:
		newline := strings.Index(text, "\n")
		if newline >= 0 && (text[:newline] == "---" || text[:newline] == "---\r") {
			// Insert after the opening delimiter line, preserving its EOL style.
			updated = text[:newline+1] + "disable-model-invocation: true\n" + text[newline+1:]
		} else {
			updated = "---\ndisable-model-invocation: true\n---\n" + text
		}
	case !disabled && already:
		updated = removeFrontmatterKey(text, "disable-model-invocation")
	default:
		return already, nil
	}
	if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
		return already, err
	}
	return disabled, nil
}

// frontmatterKeyTrue reports whether the YAML frontmatter block sets key: true.
func frontmatterKeyTrue(doc, key string) bool {
	_, front := frontmatterBlock(doc)
	if front == "" {
		return false
	}
	for _, line := range strings.Split(front, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if strings.ToLower(strings.TrimSpace(k)) == strings.ToLower(key) {
			return strings.TrimSpace(v) == "true"
		}
	}
	return false
}

// removeFrontmatterKey strips the key line from the frontmatter block.
func removeFrontmatterKey(doc, key string) string {
	body, front := frontmatterBlock(doc)
	if front == "" {
		return doc
	}
	var kept []string
	for _, line := range strings.Split(front, "\n") {
		k, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && strings.ToLower(strings.TrimSpace(k)) == strings.ToLower(key) {
			continue
		}
		kept = append(kept, line)
	}
	return "---\n" + strings.Join(kept, "\n") + "\n---\n" + body
}

// frontmatterBlock splits "---\n...front...\n---\nbody" into (body, front).
func frontmatterBlock(doc string) (string, string) {
	const delim = "---"
	if !strings.HasPrefix(doc, delim) {
		return doc, ""
	}
	rest := doc[len(delim):]
	if !strings.HasPrefix(rest, "\n") && !strings.HasPrefix(rest, "\r\n") {
		return doc, ""
	}
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n"+delim)
	if end < 0 {
		return doc, ""
	}
	front := rest[:end]
	body := rest[end+len("\n"+delim):]
	if strings.HasPrefix(body, "\n") {
		body = body[1:]
	}
	return body, front
}
