package agentflow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// DependencyKind is the kind of a shared flow dependency.
type DependencyKind string

const (
	DependencyRole  DependencyKind = "role"
	DependencySkill DependencyKind = "skill"
)

// DependencyRef is one role@version or skill reference declared by a flow.
type DependencyRef struct {
	Kind    DependencyKind `json:"kind"`
	Name    string         `json:"name"`
	Version int            `json:"version,omitempty"` // role only
}

// DependencyStatus is the environment resolution of one dependency: whether
// the referenced role version / skill exists in the target environment, and
// why not when it is missing. Resolution NEVER installs or authorizes.
type DependencyStatus struct {
	Kind    DependencyKind `json:"kind"`
	Name    string         `json:"name"`
	Version int            `json:"version,omitempty"`
	Present bool           `json:"present"`
	Reason  string         `json:"reason,omitempty"`
}

// DependencyResolver resolves role references and skill names at import
// pre-check time. It mirrors the publish resolver seam.
type DependencyResolver interface {
	ResolveRole(ctx context.Context, roleRef string) (*RoleInfo, error)
	KnownSkill(ctx context.Context, name string) bool
}

// DependencyManifest extracts the dependency set from a flow definition:
// role@version references of every role task and every skill reference.
// Check and terminal tasks contribute no dependencies.
func DependencyManifest(def *domain.FlowDefinition) []DependencyRef {
	seen := map[string]DependencyRef{}
	refs := []DependencyRef{}
	add := func(ref DependencyRef) {
		key := string(ref.Kind) + "\x00" + ref.Name + "\x00" + fmt.Sprint(ref.Version)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = ref
		refs = append(refs, ref)
	}
	for _, task := range def.Tasks {
		if task.Type != domain.FlowTaskRole || task.Terminal != nil {
			continue
		}
		handle, versionText, ok := strings.Cut(strings.TrimSpace(task.Role), "@")
		if !ok {
			continue
		}
		var version int
		_, _ = fmt.Sscanf(versionText, "%d", &version)
		add(DependencyRef{Kind: DependencyRole, Name: strings.TrimSpace(handle), Version: version})
		for _, skill := range task.Skills {
			add(DependencyRef{Kind: DependencySkill, Name: skill})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind == refs[j].Kind {
			if refs[i].Name == refs[j].Name {
				return refs[i].Version < refs[j].Version
			}
			return refs[i].Name < refs[j].Name
		}
		return refs[i].Kind < refs[j].Kind
	})
	return refs
}

// CheckDependencies resolves every dependency of a flow definition against the
// target environment. Missing dependencies are REPORTED with reasons and
// never installed.
func CheckDependencies(ctx context.Context, resolver DependencyResolver, def *domain.FlowDefinition) []DependencyStatus {
	statuses := make([]DependencyStatus, 0, len(def.Tasks))
	for _, ref := range DependencyManifest(def) {
		status := DependencyStatus{Kind: ref.Kind, Name: ref.Name, Version: ref.Version, Present: true}
		switch ref.Kind {
		case DependencyRole:
			if _, err := resolver.ResolveRole(ctx, fmt.Sprintf("%s@%d", ref.Name, ref.Version)); err != nil {
				status.Present = false
				status.Reason = err.Error()
			}
		case DependencySkill:
			if !resolver.KnownSkill(ctx, ref.Name) {
				status.Present = false
				status.Reason = "skill is not in the catalog"
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}
