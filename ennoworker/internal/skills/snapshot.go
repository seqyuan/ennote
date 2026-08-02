package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// CatalogManifestEntry is a single entry in the catalog manifest.
type CatalogManifestEntry struct {
	Kind           string `json:"kind"`
	RelPath        string `json:"relPath"`
	SourceName     string `json:"sourceName"`
	SourceDigest   string `json:"sourceDigest"`
	SnapshotDigest string `json:"snapshotDigest"`
	SnapshotMode   string `json:"snapshotMode"`
}

type catalogManifest struct {
	SchemaVersion         int                    `json:"schemaVersion"`
	Mode                  string                 `json:"mode"`
	Entries               []CatalogManifestEntry `json:"entries"`
	SourceCatalogDigest   string                 `json:"sourceCatalogDigest"`
	SnapshotCatalogDigest string                 `json:"snapshotCatalogDigest"`
	CatalogDigest         string                 `json:"catalogDigest,omitempty"`
}

// TemplateVars holds the trusted template variable values used for rendering.
type TemplateVars struct {
	Mode      string // "bwrap" | "none" — explicit, never inferred
	Workspace string
	SkillDir  string // virtual /skills/<RelPath>, or host absolute in none mode
}

// MaterializedSkillRecord is a record returned after materialization.
type MaterializedSkillRecord struct {
	SkillID       string
	RelPath       string
	Version       string
	ManifestHash  string
	ContentDigest string // snapshot tree digest
	SnapshotPath  string
}

// MaterializedCatalog holds the result of materializing a catalog.
type MaterializedCatalog struct {
	Root                  string
	SourceCatalogDigest   string
	SnapshotCatalogDigest string
	CatalogDigest         string
	Records               []MaterializedSkillRecord
}

// MaterializationPlan holds the expected state before files are written.
type MaterializationPlan struct {
	Mode                  string
	Entries               []CatalogManifestEntry
	SourceCatalogDigest   string
	SnapshotCatalogDigest string
	CatalogDigest         string

	// Internal: per-leaf data for materialization
	leafPlans []leafMaterialization
}

type leafMaterialization struct {
	relPath      string
	sourceDir    string // source skill directory
	sourceDigest string // pre-render tree digest
	snapshotDir  string // target snapshot subdirectory (relative to skills root)
	renderedMD   string // rendered SKILL.md content
}

type categoryEntry struct {
	relPath    string
	sourcePath string // source category.md path
	body       string // original body (without children index)
	cat        *Category
	children   []*CatalogNode
}

// PlanMaterialization computes the complete materialization plan from a
// catalog without writing any files. It reads source bytes with streaming
// hash for large files, renders category indexes and SKILL.md from the plan.
func PlanMaterialization(catalog *Catalog, vars TemplateVars) (*MaterializationPlan, error) {
	if catalog == nil {
		return nil, fmt.Errorf("catalog is nil")
	}
	if vars.Workspace == "" || vars.SkillDir == "" {
		return nil, fmt.Errorf("template vars require non-empty workspace and skill_dir")
	}
	if vars.Mode != "bwrap" && vars.Mode != "none" {
		return nil, fmt.Errorf("template vars require explicit mode (bwrap|none), got %q", vars.Mode)
	}

	plan := &MaterializationPlan{
		Mode: vars.Mode,
	}

	// Collect categories and skills from the tree
	var categories []categoryEntry
	collectEntries(catalog.Roots, "", &categories, &plan.leafPlans, vars)

	// Compute source entries (pre-render)
	for i := range plan.leafPlans {
		lp := &plan.leafPlans[i]
		// Compute source tree digest
		d, err := ComputeLeafDigest(lp.sourceDir)
		if err != nil {
			return nil, fmt.Errorf("source digest for %s: %w", lp.relPath, err)
		}
		lp.sourceDigest = d

		plan.Entries = append(plan.Entries, CatalogManifestEntry{
			Kind:         string(NodeSkill),
			RelPath:      lp.relPath,
			SourceName:   "", // filled below
			SourceDigest: d,
		})
	}

	// Compute category entries
	for _, cat := range categories {
		// Generate children index
		index := generateChildrenIndex(cat.children)
		fullContent := cat.body + index
		if len(fullContent) > 16*1024 {
			return nil, fmt.Errorf("category.md for %s exceeds 16 KiB (%d bytes); add subcategories",
				cat.relPath, len(fullContent))
		}
		catDigest := ComputeCategoryDigest([]byte(fullContent))

		plan.Entries = append(plan.Entries, CatalogManifestEntry{
			Kind:         string(NodeCategory),
			RelPath:      cat.relPath,
			SourceDigest: catDigest,
		})
	}

	// Sort entries by RelPath
	sort.Slice(plan.Entries, func(i, j int) bool {
		return plan.Entries[i].RelPath < plan.Entries[j].RelPath
	})

	// Fill source names from catalog skills
	for i := range plan.Entries {
		if plan.Entries[i].Kind == string(NodeSkill) {
			found := findLoadedSkill(catalog, plan.Entries[i].RelPath)
			if found != nil {
				plan.Entries[i].SourceName = found.SourceRoot
			}
		}
	}

	// Compute source catalog digest
	plan.SourceCatalogDigest = SourceCatalogDigest(plan.Entries)

	// Now compute snapshot digest: render SKILL.md and compute tree digest
	for i := range plan.leafPlans {
		lp := &plan.leafPlans[i]
		snapDigest := lp.sourceDigest // default: same as source for non-SKILL.md files
		if lp.renderedMD != "" {
			// We need the full snapshot tree digest; it would be computed after
			// materialize. For now, we estimate using the rendered SKILL.md.
			// The actual snapshot digest will be verified during materialize.
			// We'll compute it by creating a temp dir with rendered content.
			tmpDir, err := os.MkdirTemp("", "snap-plan-*")
			if err != nil {
				return nil, fmt.Errorf("plan temp dir: %w", err)
			}
			defer os.RemoveAll(tmpDir)

			if err := copyLeafForPlan(lp.sourceDir, tmpDir, lp.renderedMD); err != nil {
				return nil, fmt.Errorf("plan copy for %s: %w", lp.relPath, err)
			}
			snapDigest, err = ComputeLeafDigest(tmpDir)
			if err != nil {
				return nil, fmt.Errorf("plan snapshot digest for %s: %w", lp.relPath, err)
			}
		}

		// Update entry
		for j := range plan.Entries {
			if plan.Entries[j].RelPath == lp.relPath && plan.Entries[j].Kind == string(NodeSkill) {
				plan.Entries[j].SnapshotDigest = snapDigest
				plan.Entries[j].SnapshotMode = vars.Mode
				break
			}
		}
	}

	// For categories, snapshot digest = source digest (no rendering changes)
	for i := range plan.Entries {
		if plan.Entries[i].Kind == string(NodeCategory) {
			plan.Entries[i].SnapshotDigest = plan.Entries[i].SourceDigest
			plan.Entries[i].SnapshotMode = vars.Mode
		}
	}

	plan.SnapshotCatalogDigest = SnapshotCatalogDigest(plan.Entries, vars.Mode)
	plan.CatalogDigest = CatalogDigest(plan.Entries, vars.Mode, plan.SourceCatalogDigest, plan.SnapshotCatalogDigest)

	return plan, nil
}

// collectEntries recursively walks the catalog tree to collect categories and leaves.
func collectEntries(nodes []*CatalogNode, baseSkillDir string, categories *[]categoryEntry, leaves *[]leafMaterialization, vars TemplateVars) {
	for _, node := range nodes {
		if node.Kind == NodeSkill && node.Skill != nil {
			skill := node.Skill
			// Render SKILL.md with trusted variables
			v := map[string]string{
				"workspace":  vars.Workspace,
				"skill_dir":  vars.SkillDir + "/" + node.RelPath,
			}
			rendered, err := RenderTrustedTemplate(skill.PromptText, v)
			if err != nil {
				// Should not happen with valid vars
				rendered = skill.PromptText
			}

			*leaves = append(*leaves, leafMaterialization{
				relPath:     node.RelPath,
				sourceDir:   skill.BaseDir,
				snapshotDir: node.RelPath,
				renderedMD:  rendered,
			})
		} else if node.Kind == NodeCategory {
			cat := categoryEntry{
				relPath:    node.RelPath,
				sourcePath: node.SourcePath,
				cat:        node.Category,
				children:   node.Children,
			}
			// Read the original category.md body for 16 KiB limit check
			if node.SourcePath != "" {
				data, err := os.ReadFile(filepath.Join(node.SourcePath, "category.md"))
				if err == nil {
					_, body, _ := ParseCategory(data, filepath.Base(node.SourcePath))
					cat.body = body
				}
			}
			*categories = append(*categories, cat)
			collectEntries(node.Children, baseSkillDir, categories, leaves, vars)
		}
	}
}

func findLoadedSkill(catalog *Catalog, relPath string) *LoadedSkill {
	for _, s := range catalog.Skills {
		if s.RelPath == relPath {
			return s
		}
	}
	return nil
}

// generateChildrenIndex generates the "## Available children" section for a category.
func generateChildrenIndex(children []*CatalogNode) string {
	if len(children) == 0 {
		return ""
	}
	sorted := make([]*CatalogNode, len(children))
	copy(sorted, children)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].RelPath < sorted[j].RelPath
	})

	var buf []byte
	buf = append(buf, "\n\n## Available children\n\n"...)
	for _, child := range sorted {
		if child.Kind == NodeSkill {
			buf = append(buf, fmt.Sprintf("- skill `%s`: %s. Read `/skills/%s/SKILL.md`.\n",
				sanitizeName(child.Name), child.Description, child.RelPath)...)
		} else {
			buf = append(buf, fmt.Sprintf("- category `%s`: %s. Read `/skills/%s/category.md`.\n",
				sanitizeName(child.Name), child.Description, child.RelPath)...)
		}
	}
	return string(buf)
}

func sanitizeName(name string) string {
	for _, r := range name {
		if r == '\n' || r == '\r' || r < 0x20 {
			return " "
		}
	}
	return name
}

// copyLeafForPlan copies a source leaf directory to dest, replacing SKILL.md
// with rendered content. Used only during planning to compute expected snapshot digest.
func copyLeafForPlan(src, dst, renderedMD string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm())
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if rel == "SKILL.md" {
			return os.WriteFile(target, []byte(renderedMD), info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// MaterializeCatalog writes the catalog snapshot to disk atomically.
func MaterializeCatalog(parentDir string, plan *MaterializationPlan, catalog *Catalog) (*MaterializedCatalog, error) {
	if plan == nil {
		return nil, fmt.Errorf("plan is nil")
	}

	skillsRoot := filepath.Join(parentDir, "skills")

	// If the target snapshot already exists, never overwrite or RemoveAll it.
	// Verify against the current plan's full expected digest and reuse only
	// when the existing tree matches completely; otherwise fail with a
	// conflict error (the snapshot may be in use by a resume path).
	if _, err := os.Stat(skillsRoot); err == nil {
		if verifyErr := VerifyMaterializedCatalog(skillsRoot, plan.CatalogDigest); verifyErr == nil {
			return &MaterializedCatalog{
				Root:                  skillsRoot,
				SourceCatalogDigest:   plan.SourceCatalogDigest,
				SnapshotCatalogDigest: plan.SnapshotCatalogDigest,
				CatalogDigest:         plan.CatalogDigest,
				Records:               nil, // DB rows are already present for this run
			}, nil
		} else {
			return nil, fmt.Errorf("existing skills snapshot conflicts with current plan: %w", verifyErr)
		}
	}

	// Stage in a temp directory
	tmpDir := filepath.Join(parentDir, "skills.tmp-"+uuid.NewString()[:8])
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("create temp skills dir: %w", err)
	}

	cleanup := func() { os.RemoveAll(tmpDir) }
	success := false
	defer func() {
		if !success {
			cleanup()
		}
	}()

	// Write category files with children index
	categories := collectCategoryNodes(catalog.Roots)
	for _, cat := range categories {
		catDir := filepath.Join(tmpDir, filepath.FromSlash(cat.relPath))
		if err := os.MkdirAll(catDir, 0o755); err != nil {
			return nil, fmt.Errorf("create category dir %s: %w", cat.relPath, err)
		}

		index := generateChildrenIndex(cat.children)
		content := cat.body + index
		if len(content) > 16*1024 {
			return nil, fmt.Errorf("category.md for %s exceeds 16 KiB (%d bytes)", cat.relPath, len(content))
		}

		catFile := filepath.Join(catDir, "category.md")
		if err := os.WriteFile(catFile, []byte(content), 0o644); err != nil {
			return nil, fmt.Errorf("write category.md %s: %w", cat.relPath, err)
		}
	}

	// Copy and render skill leaves
	var records []MaterializedSkillRecord
	for _, lp := range plan.leafPlans {
		destDir := filepath.Join(tmpDir, filepath.FromSlash(lp.snapshotDir))
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return nil, fmt.Errorf("create leaf dir %s: %w", lp.relPath, err)
		}

		// Copy with rendering
		if err := copyLeafWithRender(lp.sourceDir, destDir, lp.renderedMD); err != nil {
			return nil, fmt.Errorf("copy leaf %s: %w", lp.relPath, err)
		}

		// Verify source digest before render (from source, not tmp)
		srcDigest, err := ComputeLeafDigest(lp.sourceDir)
		if err != nil {
			return nil, fmt.Errorf("verify source digest %s: %w", lp.relPath, err)
		}
		if srcDigest != lp.sourceDigest {
			return nil, fmt.Errorf("source digest mismatch for %s: expected %s, got %s",
				lp.relPath, lp.sourceDigest, srcDigest)
		}

		// Verify snapshot digest
		snapDigest, err := ComputeLeafDigest(destDir)
		if err != nil {
			return nil, fmt.Errorf("snapshot digest %s: %w", lp.relPath, err)
		}

		// The materialized snapshot must match the plan's expected snapshot
		// digest exactly; otherwise the plan and the written tree diverged.
		expectedSnap := ""
		for i := range plan.Entries {
			if plan.Entries[i].Kind == string(NodeSkill) && plan.Entries[i].RelPath == lp.relPath {
				expectedSnap = plan.Entries[i].SnapshotDigest
				break
			}
		}
		if expectedSnap != "" && snapDigest != expectedSnap {
			return nil, fmt.Errorf("snapshot digest mismatch for %s: expected %s, got %s",
				lp.relPath, expectedSnap, snapDigest)
		}

		skill := findLoadedSkill(catalog, lp.relPath)
		skillID := ""
		version := ""
		manifestHash := ""
		if skill != nil {
			skillID = skill.Manifest.ID
			version = skill.Manifest.Version
			manifestHash = skill.ManifestHash
		}

		records = append(records, MaterializedSkillRecord{
			SkillID:       skillID,
			RelPath:       lp.relPath,
			Version:       version,
			ManifestHash:  manifestHash,
			ContentDigest: snapDigest,
			SnapshotPath:  filepath.Join(skillsRoot, filepath.FromSlash(lp.relPath)),
		})
	}

	// Write .catalog.json
	manifest := catalogManifest{
		SchemaVersion:         digestSchemaVersion,
		Mode:                  plan.Mode,
		Entries:               plan.Entries,
		SourceCatalogDigest:   plan.SourceCatalogDigest,
		SnapshotCatalogDigest: plan.SnapshotCatalogDigest,
		CatalogDigest:         plan.CatalogDigest,
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal catalog manifest: %w", err)
	}
	// The manifest is written compactly with struct field order (no map keys),
	// which is the canonical byte form the verifier re-encodes and compares
	// byte-for-byte against the on-disk file.
	manifestFile := filepath.Join(tmpDir, ".catalog.json")
	if err := os.WriteFile(manifestFile, manifestData, 0o644); err != nil {
		return nil, fmt.Errorf("write .catalog.json: %w", err)
	}

	// fsync tmpDir contents
	if err := syncDir(tmpDir); err != nil {
		return nil, fmt.Errorf("fsync tmp skills: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpDir, skillsRoot); err != nil {
		return nil, fmt.Errorf("rename skills snapshot: %w", err)
	}

	// fsync parent
	if f, err := os.Open(parentDir); err == nil {
		f.Sync()
		f.Close()
	}

	success = true

	return &MaterializedCatalog{
		Root:                  skillsRoot,
		SourceCatalogDigest:   plan.SourceCatalogDigest,
		SnapshotCatalogDigest: plan.SnapshotCatalogDigest,
		CatalogDigest:         plan.CatalogDigest,
		Records:               records,
	}, nil
}

// VerifyMaterializedCatalog verifies an existing materialized catalog against
// the expected CatalogDigest by re-walking the entire tree and re-computing
// all digests from disk.
func VerifyMaterializedCatalog(root string, expectedCatalogDigest string) error {
	// Read manifest
	manifestData, err := os.ReadFile(filepath.Join(root, ".catalog.json"))
	if err != nil {
		return fmt.Errorf("read .catalog.json: %w", err)
	}

	var manifest catalogManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("parse .catalog.json: %w", err)
	}

	// Canonical re-encode and compare bytes
	reEncoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("re-encode manifest: %w", err)
	}
	if string(reEncoded) != string(manifestData) {
		return fmt.Errorf(".catalog.json is not canonical (whitespace or field order differs)")
	}

	if manifest.SchemaVersion != digestSchemaVersion {
		return fmt.Errorf("unsupported schema version: %d", manifest.SchemaVersion)
	}

	// Verify snapshot digest for each entry
	for _, entry := range manifest.Entries {
		if entry.Kind == string(NodeSkill) {
			leafDir := filepath.Join(root, filepath.FromSlash(entry.RelPath))
			d, err := ComputeLeafDigest(leafDir)
			if err != nil {
				return fmt.Errorf("verify snapshot digest for %s: %w", entry.RelPath, err)
			}
			if d != entry.SnapshotDigest {
				return fmt.Errorf("snapshot digest mismatch for %s: expected %s, got %s",
					entry.RelPath, entry.SnapshotDigest, d)
			}
		} else {
			catFile := filepath.Join(root, filepath.FromSlash(entry.RelPath), "category.md")
			data, err := os.ReadFile(catFile)
			if err != nil {
				return fmt.Errorf("read category %s: %w", entry.RelPath, err)
			}
			d := ComputeCategoryDigest(data)
			if d != entry.SnapshotDigest {
				return fmt.Errorf("category digest mismatch for %s: expected %s, got %s",
					entry.RelPath, entry.SnapshotDigest, d)
			}
		}
	}

	// Walk entire tree and check no extra files/dirs.
	// A path is allowed if it equals a manifest entry or is under one
	// (e.g. skill leaves may contain arbitrary nested attachments like
	// scripts/run.R). Symlinks and non-regular files are always rejected.
	manifestPaths := make([]string, 0, len(manifest.Entries)+1)
	manifestPaths = append(manifestPaths, ".catalog.json")
	for _, entry := range manifest.Entries {
		manifestPaths = append(manifestPaths, filepath.ToSlash(entry.RelPath))
		if entry.Kind == string(NodeCategory) {
			manifestPaths = append(manifestPaths, filepath.ToSlash(entry.RelPath)+"/category.md")
		}
	}

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		relSlash := filepath.ToSlash(rel)
		if relSlash == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink found in snapshot: %s", relSlash)
		}
		if !info.Mode().IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file found in snapshot: %s", relSlash)
		}
		// Path must be a manifest entry itself or under one of them.
		allowed := false
		for _, prefix := range manifestPaths {
			if relSlash == prefix || strings.HasPrefix(relSlash, prefix+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("extra entry in snapshot not in manifest: %s", relSlash)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("verify snapshot tree: %w", err)
	}

	// Recompute SnapshotCatalogDigest
	recomputedSnap := SnapshotCatalogDigest(manifest.Entries, manifest.Mode)
	if recomputedSnap != manifest.SnapshotCatalogDigest {
		return fmt.Errorf("snapshot catalog digest mismatch: expected %s, got %s",
			manifest.SnapshotCatalogDigest, recomputedSnap)
	}

	// Recompute final CatalogDigest using source entries from manifest (checkpoint-anchored)
	// and recomputed snapshot entries
	recomputedCatalog := CatalogDigest(manifest.Entries, manifest.Mode,
		manifest.SourceCatalogDigest, recomputedSnap)
	if recomputedCatalog != expectedCatalogDigest {
		return fmt.Errorf("catalog digest mismatch: expected %s, got %s",
			expectedCatalogDigest, recomputedCatalog)
	}

	return nil
}

// collectCategoryNodes collects all category nodes from the catalog tree.
func collectCategoryNodes(nodes []*CatalogNode) []categoryEntry {
	var cats []categoryEntry
	for _, node := range nodes {
		if node.Kind == NodeCategory {
			cat := categoryEntry{
				relPath:    node.RelPath,
				sourcePath: node.SourcePath,
				cat:        node.Category,
				children:   node.Children,
			}
			// Read the original category.md body
			if node.SourcePath != "" {
				data, err := os.ReadFile(filepath.Join(node.SourcePath, "category.md"))
				if err == nil {
					_, body, _ := ParseCategory(data, filepath.Base(node.SourcePath))
					cat.body = body
				}
			}
			cats = append(cats, cat)
			subCats := collectCategoryNodes(node.Children)
			cats = append(cats, subCats...)
		}
	}
	return cats
}

// copyLeafWithRender copies a source leaf to dest, rendering SKILL.md.
func copyLeafWithRender(src, dst, renderedMD string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Reject symlinks at copy time
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink not allowed: %s", path)
		}
		if !info.Mode().IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file not allowed: %s", path)
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}

		if rel == "SKILL.md" {
			return os.WriteFile(target, []byte(renderedMD), info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// syncDir fsyncs all files in a directory tree.
func syncDir(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			return f.Sync()
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	})
}
