package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// NodeKind distinguishes category directories from skill leaf directories.
type NodeKind string

const (
	NodeCategory NodeKind = "category"
	NodeSkill    NodeKind = "skill"
)

// SourceRoot identifies a skills source tree with its merge priority.
type SourceRoot struct {
	Name     string // "user" | "builtin"
	Path     string // canonical absolute path
	Priority int    // lower wins; default user=0, builtin=1
}

// Diagnostic records a non-fatal issue encountered during catalog discovery.
type Diagnostic struct {
	Level   string // "warn" | "skip"
	Message string
	RelPath string
	Source  string
}

// CatalogNode is a single node in the merged catalog tree.
type CatalogNode struct {
	Kind        NodeKind
	RelPath     string
	Name        string
	Description string
	SourceRoot  string
	SourcePath  string // canonical absolute source directory path
	Priority    int    // source priority at discovery time; lower wins
	Category    *Category
	Skill       *LoadedSkill
	Children    []*CatalogNode
}

// Catalog is the fully merged, deterministic skill tree.
type Catalog struct {
	Roots       []*CatalogNode
	Skills      []*LoadedSkill
	Diagnostics []Diagnostic
}

// BuildCatalog discovers all skills and categories from the given source roots,
// merges them deterministically, and returns the merged catalog tree.
func BuildCatalog(sources []SourceRoot) *Catalog {
	catalog := &Catalog{}

	if len(sources) == 0 {
		return catalog
	}

	// Sort sources by (Priority, Name, Path)
	sortedSources := make([]SourceRoot, len(sources))
	copy(sortedSources, sources)
	sort.Slice(sortedSources, func(i, j int) bool {
		if sortedSources[i].Priority != sortedSources[j].Priority {
			return sortedSources[i].Priority < sortedSources[j].Priority
		}
		if sortedSources[i].Name != sortedSources[j].Name {
			return sortedSources[i].Name < sortedSources[j].Name
		}
		return sortedSources[i].Path < sortedSources[j].Path
	})

	// Deduplicate canonical roots
	seenRoots := map[string]bool{}
	uniqueSources := make([]SourceRoot, 0, len(sortedSources))
	for _, src := range sortedSources {
		canonical, err := canonicalizeRoot(src.Path)
		if err != nil {
			catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
				Level:   "warn",
				Message: fmt.Sprintf("skip source root %q: %v", src.Name, err),
				Source:  src.Name,
			})
			continue
		}
		src.Path = canonical
		if seenRoots[canonical] {
			catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
				Level:   "warn",
				Message: "duplicate canonical source root",
				Source:  src.Name,
			})
			continue
		}
		seenRoots[canonical] = true
		uniqueSources = append(uniqueSources, src)
	}

	if len(uniqueSources) == 0 {
		return catalog
	}

	// Discover from each source and merge
	type sourceTree struct {
		source SourceRoot
		nodes  map[string]*CatalogNode // relPath → node
	}

	var trees []sourceTree
	for _, src := range uniqueSources {
		nodes := map[string]*CatalogNode{}
		discoverTree(src.Path, "", src, nodes, catalog)
		trees = append(trees, sourceTree{source: src, nodes: nodes})
	}

	// Merge trees: process in forward order (highest priority first).
	// The first occurrence wins; later (lower-priority) entries are diagnosed.
	merged := map[string]*CatalogNode{}
	for i := 0; i < len(trees); i++ {
		t := trees[i]
		for relPath, node := range t.nodes {
			existing, exists := merged[relPath]
			if !exists {
				merged[relPath] = node
				continue
			}
			// Conflict resolution: existing has higher priority (from earlier source).
			if existing.Kind != node.Kind {
				catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
					Level: "skip",
					Message: fmt.Sprintf("node_kind_shadowed: %s (kind=%s from %s) shadows %s (kind=%s from %s)",
						relPath, existing.Kind, existing.SourceRoot, relPath, node.Kind, node.SourceRoot),
					RelPath: relPath,
					Source:  node.SourceRoot,
				})
				continue
			}
			// Same kind: higher priority wins (existing).
			// For categories: merge children from lower-priority source into existing.
			if existing.Kind == NodeCategory {
				existing.Children = mergeChildren(existing.Children, node.Children)
			}
			catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
				Level: "warn",
				Message: fmt.Sprintf("path %s exists in both %s and %s, using %s",
					relPath, existing.SourceRoot, node.SourceRoot, existing.SourceRoot),
				RelPath: relPath,
				Source:  node.SourceRoot,
			})
		}
	}

	// Check for duplicate Manifest IDs across different RelPaths.
	// Resolution must be deterministic: collect all skills, sort by the
	// full ordering key (Priority, SourceRoot.Name, canonical Path, RelPath),
	// and keep the first occurrence of each ID. Never rely on map iteration order.
	type idCandidate struct {
		node *CatalogNode
		key  string // full ordering key (Priority, SourceRoot.Name, SourcePath, RelPath)
	}
	var candidates []idCandidate
	for relPath, node := range merged {
		if node.Kind == NodeSkill && node.Skill != nil {
			key := fmt.Sprintf("%d\x00%s\x00%s\x00%s",
				node.Priority, node.SourceRoot, node.SourcePath, relPath)
			candidates = append(candidates, idCandidate{node: node, key: key})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].key < candidates[j].key })

	idMap := map[string]*CatalogNode{}
	for _, c := range candidates {
		node := c.node
		mid := node.Skill.Manifest.ID
		if existing, exists := idMap[mid]; exists {
			catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
				Level: "skip",
				Message: fmt.Sprintf("duplicate_skill_id: %s at %s (from %s) conflicts with %s (from %s)",
					mid, node.RelPath, node.SourceRoot, existing.RelPath, existing.SourceRoot),
				RelPath: node.RelPath,
				Source:  node.SourceRoot,
			})
			delete(merged, node.RelPath)
			continue
		}
		idMap[mid] = node
	}

	// Build the tree structure
	roots := buildTree(merged)

	// Collect skills
	var skills []*LoadedSkill
	for _, node := range merged {
		if node.Kind == NodeSkill && node.Skill != nil {
			skills = append(skills, node.Skill)
		}
	}

	// Sort roots, children, skills
	sortNodes(roots)
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].RelPath < skills[j].RelPath
	})

	catalog.Roots = roots
	catalog.Skills = skills

	// Sort diagnostics
	sort.Slice(catalog.Diagnostics, func(i, j int) bool {
		di, dj := catalog.Diagnostics[i], catalog.Diagnostics[j]
		if di.RelPath != dj.RelPath {
			return di.RelPath < dj.RelPath
		}
		return di.Message < dj.Message
	})

	return catalog
}

// discoverTree recursively discovers categories and skills from a source root.
func discoverTree(baseDir, relPath string, src SourceRoot, nodes map[string]*CatalogNode, catalog *Catalog) {
	dir := baseDir
	if relPath != "" {
		dir = filepath.Join(baseDir, filepath.FromSlash(relPath))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == ".git" {
			continue
		}
		if !entry.IsDir() {
			continue
		}

		childDir := filepath.Join(dir, name)
		childRel := name
		if relPath != "" {
			childRel = relPath + "/" + name
		}

		// Check for symlinks
		fi, err := os.Lstat(childDir)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
				Level:   "skip",
				Message: "symlink not allowed in skill/category tree",
				RelPath: childRel,
				Source:  src.Name,
			})
			continue
		}

		// Determine if this is a skill leaf or category
		isSkill, skillErr := IsSkillLeaf(childDir)
		if skillErr != nil {
			catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
				Level:   "skip",
				Message: skillErr.Error(),
				RelPath: childRel,
				Source:  src.Name,
			})
			continue
		}

		if isSkill {
			skill, err := Load(childDir)
			if err != nil {
				catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
					Level:   "skip",
					Message: fmt.Sprintf("failed to load skill: %v", err),
					RelPath: childRel,
					Source:  src.Name,
				})
				slog.Warn("skill load failed", "dir", childDir, "error", err)
				continue
			}
			skill.RelPath = childRel
			skill.SourceRoot = src.Name

			node := &CatalogNode{
				Kind:        NodeSkill,
				RelPath:     childRel,
				Name:        skill.Manifest.ID,
				Description: skill.Manifest.Description,
				SourceRoot:  src.Name,
				SourcePath:  childDir,
				Priority:    src.Priority,
				Skill:       skill,
			}
			nodes[childRel] = node
			continue
		}

		// Check if it's a category
		isCat, catErr := IsCategoryDir(childDir)
		if catErr != nil {
			catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
				Level:   "skip",
				Message: catErr.Error(),
				RelPath: childRel,
				Source:  src.Name,
			})
			continue
		}

		if isCat {
			catData, err := os.ReadFile(filepath.Join(childDir, "category.md"))
			if err != nil {
				catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
					Level:   "skip",
					Message: fmt.Sprintf("failed to read category.md: %v", err),
					RelPath: childRel,
					Source:  src.Name,
				})
				continue
			}
			cat, _, err := ParseCategory(catData, name)
			if err != nil {
				catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
					Level:   "skip",
					Message: fmt.Sprintf("failed to parse category.md: %v", err),
					RelPath: childRel,
					Source:  src.Name,
				})
				continue
			}

			node := &CatalogNode{
				Kind:        NodeCategory,
				RelPath:     childRel,
				Name:        cat.Name,
				Description: cat.Description,
				SourceRoot:  src.Name,
				SourcePath:  childDir,
				Priority:    src.Priority,
				Category:    cat,
				Children:    []*CatalogNode{},
			}
			nodes[childRel] = node

			// Recurse into category
			discoverTree(baseDir, childRel, src, nodes, catalog)
			continue
		}

		// Not a skill or category - could be an intermediate directory without category.md
		// Check if there are subdirectories that might be skills/categories
		hasChildren := false
		subEntries, subErr := os.ReadDir(childDir)
		if subErr == nil {
			for _, sub := range subEntries {
				if sub.IsDir() && !strings.HasPrefix(sub.Name(), ".") {
					hasChildren = true
					break
				}
			}
		}
		if hasChildren {
			catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{
				Level:   "skip",
				Message: "intermediate directory missing category.md",
				RelPath: childRel,
				Source:  src.Name,
			})
			// Still recurse to find deeper skills/categories
			discoverTree(baseDir, childRel, src, nodes, catalog)
		}
	}
}

// buildTree builds a hierarchical tree from flat node map.
func buildTree(nodes map[string]*CatalogNode) []*CatalogNode {
	var roots []*CatalogNode

	// Group nodes by parent
	children := map[string][]*CatalogNode{}
	for _, node := range nodes {
		parent := parentPath(node.RelPath)
		if parent == "" {
			roots = append(roots, node)
		} else {
			children[parent] = append(children[parent], node)
		}
	}

	// Attach children
	for _, node := range nodes {
		if childList, ok := children[node.RelPath]; ok {
			node.Children = childList
		}
	}

	return roots
}

// parentPath returns the parent RelPath or "" for root.
func parentPath(relPath string) string {
	idx := strings.LastIndex(relPath, "/")
	if idx < 0 {
		return ""
	}
	return relPath[:idx]
}

// mergeChildren merges two child lists, preferring higher-priority entries.
// existing has higher priority than incoming.
func mergeChildren(existing, incoming []*CatalogNode) []*CatalogNode {
	merged := map[string]*CatalogNode{}
	// Add incoming first (lower priority)
	for _, child := range incoming {
		merged[child.RelPath] = child
	}
	// Override with existing (higher priority) on same RelPath
	for _, child := range existing {
		merged[child.RelPath] = child
	}
	result := make([]*CatalogNode, 0, len(merged))
	for _, child := range merged {
		result = append(result, child)
	}
	sortNodes(result)
	return result
}

// sortNodes sorts nodes by RelPath.
func sortNodes(nodes []*CatalogNode) {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].RelPath < nodes[j].RelPath
	})
	for _, node := range nodes {
		sortNodes(node.Children)
	}
}

// canonicalizeRoot returns the canonical absolute path of a source root.
func canonicalizeRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("source root is not a directory")
	}
	return filepath.Clean(resolved), nil
}
