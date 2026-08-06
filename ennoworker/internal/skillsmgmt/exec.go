package skillsmgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	checkTimeoutMS  = 15_000
	execTimeout     = 60 * time.Second
	searchAPIDefault = "https://skills.sh"
	gitCheckTimeout = 30 * time.Second
)

var ansiRE = regexp.MustCompile(`\x1B\[[0-9;]*m`)

// SearchResult mirrors pi-web's SkillSearchResult.
type SearchResult struct {
	Package  string `json:"package"`
	Installs string `json:"installs"`
	URL      string `json:"url"`
}

// UpdateState is the lifecycle state of an update check.
type UpdateState string

const (
	UpdateUpToDate       UpdateState = "up-to-date"
	UpdateAvailable      UpdateState = "update-available"
	UpdateError          UpdateState = "error"
	UpdateUnsupported    UpdateState = "unsupported"
)

// UpdateResult mirrors pi-web's SkillUpdateResult.
type UpdateResult struct {
	Package        string      `json:"package"`
	Scope          string      `json:"scope"`
	State          UpdateState `json:"state"`
	CurrentVersion string      `json:"currentVersion"`
	LatestVersion  string      `json:"latestVersion,omitempty"`
	Message        string      `json:"message,omitempty"`
}

type skillsAPIResponse struct {
	Skills []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Source   string `json:"source"`
		Installs int    `json:"installs"`
	} `json:"skills"`
}

// ——— search ———

// Search queries the skills.sh registry API, falling back to `npx skills find`
// (matching pi-web's search route behavior).
func Search(ctx context.Context, query string, limit int, apiBase string) ([]SearchResult, error) {
	if query = strings.TrimSpace(query); query == "" {
		return nil, fmt.Errorf("query required")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 50 {
		limit = 50
	}
	if apiBase == "" {
		apiBase = searchAPIDefault
	}
	if results, err := searchAPI(ctx, query, limit, apiBase); err == nil {
		return results, nil
	}
	output, err := runNpx(ctx, []string{"skills", "find", query}, 20*time.Second, "", nil)
	if err != nil {
		return nil, fmt.Errorf("skills.sh search failed: %w", err)
	}
	results := parseFindOutput(output)
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func searchAPI(ctx context.Context, query string, limit int, apiBase string) ([]SearchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		apiBase+"/api/search?q="+url.QueryEscape(query)+"&limit="+fmt.Sprint(limit), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: checkTimeoutMS * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var parsed skillsAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	var results []SearchResult
	for _, skill := range parsed.Skills {
		name := strings.TrimSpace(skill.Name)
		source := strings.TrimSpace(skill.Source)
		slug := strings.TrimSpace(skill.ID)
		if name == "" || (source == "" && slug == "") {
			continue
		}
		pkg := source
		if pkg == "" {
			pkg = slug
		}
		results = append(results, SearchResult{
			Package:  pkg + "@" + name,
			Installs: formatInstalls(skill.Installs),
			URL:      optionalString(slug != "", apiBase+"/"+urlPathEscape(slug)),
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		return parseInstallCount(results[i].Installs) > parseInstallCount(results[j].Installs)
	})
	return results, nil
}

func formatInstalls(count int) string {
	switch {
	case count >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(count)/1_000_000), ".0") + "M installs"
	case count >= 1_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(count)/1_000), ".0") + "K installs"
	case count > 0:
		return fmt.Sprintf("%d installs", count)
	default:
		return ""
	}
}

func parseInstallCount(installs string) int {
	var value float64
	var suffix string
	if _, err := fmt.Sscanf(installs, "%f%s", &value, &suffix); err != nil {
		return 0
	}
	switch {
	case strings.HasPrefix(suffix, "B"):
		return int(value * 1_000_000_000)
	case strings.HasPrefix(suffix, "M"):
		return int(value * 1_000_000)
	case strings.HasPrefix(suffix, "K"):
		return int(value * 1_000)
	default:
		return int(value)
	}
}

// parseFindOutput parses `npx skills find` text output into search results.
func parseFindOutput(raw string) []SearchResult {
	clean := ansiRE.ReplaceAllString(raw, "")
	var results []SearchResult
	lines := strings.Split(clean, "\n")
	pkgRE := regexp.MustCompile(`^([\w.\-]+\/[\w.\-@:]+)\s+([\d.,]+[KMB]?\s+installs)$`)
	for i, line := range lines {
		line = strings.TrimSpace(line)
		match := pkgRE.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		urlLine := ""
		if i+1 < len(lines) {
			urlLine = strings.TrimPrefix(strings.TrimSpace(lines[i+1]), "└")
			urlLine = strings.TrimSpace(urlLine)
		}
		result := SearchResult{Package: match[1], Installs: match[2]}
		if strings.HasPrefix(urlLine, "https://") {
			result.URL = urlLine
		}
		results = append(results, result)
	}
	return results
}

// ——— install / update / remove ———

// Install runs `npx skills add <pkg> -y --agent pi [-g]`. cwd is required for
// project scope. Returns the trimmed ANSI-free combined output.
func Install(ctx context.Context, pkg, scope, cwd string) (string, error) {
	args := []string{"skills", "add", strings.TrimSpace(pkg), "-y", "--agent", "pi"}
	if scope == "global" {
		args = append(args, "-g")
	}
	output, err := runNpx(ctx, args, execTimeout, cwd, nil)
	if err != nil {
		return output, fmt.Errorf("install failed: %s", lastLines(output, 300))
	}
	if !installSucceeded(output) {
		return output, fmt.Errorf("install failed: %s", lastLines(output, 300))
	}
	return output, nil
}

var installDoneRE = regexp.MustCompile(`(?i)Installation complete|Installed \d+ skill`)

func installSucceeded(output string) bool {
	return installDoneRE.MatchString(ansiRE.ReplaceAllString(output, ""))
}

// Remove runs `npx skills remove --global <name> -y` (or project-scoped).
func Remove(ctx context.Context, name string, global bool) (string, error) {
	args := []string{"skills", "remove"}
	if global {
		args = append(args, "--global")
	}
	args = append(args, name, "-y", "--agent", "pi")
	return runNpx(ctx, args, execTimeout, "", nil)
}

// ——— update check ———

// CheckUpdates compares each install's recorded version hash against the
// remote (GitHub tree API for global, skills.sh download hash for project).
func CheckUpdates(ctx context.Context, installs []InstallInfo, githubToken, skillsAPIDefaultBase string) []UpdateResult {
	results := make([]UpdateResult, 0, len(installs))
	for _, install := range installs {
		results = append(results, checkOne(ctx, install, githubToken, skillsAPIDefaultBase))
	}
	return results
}

func checkOne(ctx context.Context, install InstallInfo, githubToken, apiBase string) UpdateResult {
	if !install.CanCheckForUpdates || install.VersionHash == "" || install.SkillPath == "" {
		return UpdateResult{Package: install.Package, Scope: install.Scope, State: UpdateUnsupported,
			Message: "This lock entry cannot be checked automatically."}
	}
	if apiBase == "" {
		apiBase = searchAPIDefault
	}
	if install.Scope == "global" {
		return checkGlobal(ctx, install, githubToken)
	}
	return checkProject(ctx, install, apiBase)
}

func checkGlobal(ctx context.Context, install InstallInfo, githubToken string) UpdateResult {
	ref := install.Ref
	if ref == "" {
		ref = "HEAD"
	}
	apiURL := "https://api.github.com/repos/" + install.Source + "/git/trees/" + url.PathEscape(ref) + "?recursive=1"
	folder := skillFolder(install.SkillPath)
	latest, err := fetchGitHubTreeHash(ctx, apiURL, githubToken, folder)
	if err != nil && isRateLimited(err) {
		latest, err = resolveGitTreeHash(ctx, install, ref)
	}
	if err != nil {
		return UpdateResult{Package: install.Package, Scope: install.Scope, State: UpdateError, Message: err.Error()}
	}
	if latest == "" {
		return UpdateResult{Package: install.Package, Scope: install.Scope, State: UpdateError,
			Message: "Remote skill path was not found."}
	}
	state := UpdateUpToDate
	if latest != install.VersionHash {
		state = UpdateAvailable
	}
	return UpdateResult{Package: install.Package, Scope: install.Scope, State: state,
		CurrentVersion: install.VersionHash, LatestVersion: latest}
}

var fetchGitHubTreeHash = func(ctx context.Context, apiURL, token, folder string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "ennote")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: checkTimeoutMS * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		SHA  string `json:"sha"`
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if folder == "" {
		return parsed.SHA, nil
	}
	for _, entry := range parsed.Tree {
		if entry.Type == "tree" && entry.Path == folder {
			return entry.SHA, nil
		}
	}
	return "", nil
}

func isRateLimited(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403") || strings.Contains(msg, "HTTP 429")
}

// resolveGitTreeHash fetches a bare repo depth-1 and computes the tree hash of
// the skill folder (pi-web resolveGitTreeHash port).
func resolveGitTreeHash(ctx context.Context, install InstallInfo, ref string) (string, error) {
	repository := "https://github.com/" + install.Source + ".git"
	folder := skillFolder(install.SkillPath)
	dir, err := osMkdirTemp("", "ennote-skill-check-")
	if err != nil {
		return "", err
	}
	defer osRemoveAll(dir)
	if err := runGit(ctx, dir, "init", "--bare", dir); err != nil {
		return "", err
	}
	if err := runGit(ctx, dir, "fetch", "--depth=1", "--filter=blob:none", "--no-tags", repository, ref); err != nil {
		return "", err
	}
	revision := "FETCH_HEAD^{tree}"
	if folder != "" {
		revision = "FETCH_HEAD:" + folder
	}
	hash, err := runGitOutput(ctx, dir, "rev-parse", revision)
	if err != nil {
		return "", err
	}
	hash = strings.TrimSpace(hash)
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(hash) {
		return "", fmt.Errorf("invalid git tree hash")
	}
	return hash, nil
}

func checkProject(ctx context.Context, install InstallInfo, apiBase string) UpdateResult {
	parts := strings.Split(install.Source, "/")
	if len(parts) != 2 {
		return UpdateResult{Package: install.Package, Scope: install.Scope, State: UpdateError,
			Message: "Invalid project source."}
	}
	name := skillSlug(skillNameFromPackage(install.Package))
	u := fmt.Sprintf("%s/api/download/%s/%s/%s", apiBase, url.PathEscape(parts[0]), url.PathEscape(parts[1]), url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return UpdateResult{Package: install.Package, Scope: install.Scope, State: UpdateError, Message: err.Error()}
	}
	client := &http.Client{Timeout: checkTimeoutMS * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return UpdateResult{Package: install.Package, Scope: install.Scope, State: UpdateError, Message: err.Error()}
	}
	defer resp.Body.Close()
	var parsed struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil || parsed.Hash == "" {
		return UpdateResult{Package: install.Package, Scope: install.Scope, State: UpdateError,
			Message: "skills.sh did not return a version hash."}
	}
	state := UpdateUpToDate
	if parsed.Hash != install.VersionHash {
		state = UpdateAvailable
	}
	return UpdateResult{Package: install.Package, Scope: install.Scope, State: state,
		CurrentVersion: install.VersionHash, LatestVersion: parsed.Hash}
}

// ——— update (re-install) ———

// Update re-runs the install command for an existing install (pi-web
// buildSkillUpdateArgs port).
func Update(ctx context.Context, install InstallInfo, cwd string) (string, error) {
	folder := skillFolder(install.SkillPath)
	source := install.Source
	if folder != "" {
		source = source + "/" + folder
	}
	ref := ""
	if install.Ref != "" {
		ref = "#" + url.QueryEscape(install.Ref)
	}
	args := []string{"skills", "add", source + ref, "--skill", skillNameFromPackage(install.Package), "-y", "--agent", "pi"}
	if install.Scope == "global" {
		args = append(args, "-g")
	}
	output, err := runNpx(ctx, args, execTimeout, cwd, nil)
	if err != nil {
		return output, fmt.Errorf("update failed: %s", lastLines(output, 300))
	}
	if !installSucceeded(output) {
		return output, fmt.Errorf("update failed: %s", lastLines(output, 300))
	}
	return output, nil
}

// ——— shared helpers ———

func realRunNpx(ctx context.Context, args []string, timeout time.Duration, cwd string, env []string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "npx", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if env == nil {
		env = osEnvironWith("FORCE_COLOR=0")
	}
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	output := ansiRE.ReplaceAllString(out.String(), "")
	output = strings.TrimSpace(output)
	if cctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("timed out after %s", timeout)
	}
	return output, err
}

// runNpx is swappable for tests.
var runNpx = realRunNpx

func runGit(ctx context.Context, gitDir string, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, gitCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", append([]string{"--git-dir=" + gitDir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, lastLines(out.String(), 200))
	}
	return nil
}

func runGitOutput(ctx context.Context, gitDir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, gitCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", append([]string{"--git-dir=" + gitDir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, lastLines(out.String(), 200))
	}
	return out.String(), nil
}

func skillSlug(name string) string {
	lower := strings.ToLower(name)
	lower = regexp.MustCompile(`[\s_]+`).ReplaceAllString(lower, "-")
	lower = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(lower, "")
	lower = regexp.MustCompile(`-+`).ReplaceAllString(lower, "-")
	return strings.Trim(lower, "-")
}

func skillNameFromPackage(pkg string) string {
	if at := strings.LastIndex(pkg, "@"); at >= 0 {
		return pkg[at+1:]
	}
	return pkg
}

func skillFolder(skillPath string) string {
	folder := strings.ReplaceAll(skillPath, "\\", "/")
	lower := strings.ToLower(folder)
	if strings.HasSuffix(lower, "/skill.md") {
		folder = folder[:len(folder)-9]
	} else if strings.HasSuffix(lower, "skill.md") {
		folder = folder[:len(folder)-8]
	}
	return strings.TrimSuffix(folder, "/")
}

func lastLines(s string, maxBytes int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBytes {
		return s
	}
	return s[len(s)-maxBytes:]
}

// Thin wrappers for testability (httptest + fake bins swap these).
var (
	osMkdirTemp   = os.MkdirTemp
	osRemoveAll   = os.RemoveAll
	osEnvironWith = func(extra string) []string { return append(os.Environ(), extra) }
)
