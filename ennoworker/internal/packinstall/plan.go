package packinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentpack"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
)

// PlanVersion is the install plan schema version. Dry-run and real installs
// return the same shape; a real install additionally fills State.
const PlanVersion = 1

// Action kinds.
const (
	ActionKindGraph        = "graph"
	ActionKindGraphRole    = "graph_role"
	ActionKindRole         = "role"
	ActionKindRoleRevision = "role_revision"
	ActionKindSkill        = "skill"
)

// Scope spellings used by the installer mirror the manifest vocabulary.
const (
	skillScopeFlow   = agentpack.SkillScopeFlow
	skillScopeGlobal = agentpack.SkillScopeGlobal
	roleScopeFlow    = agentpack.RoleScopeFlow
	roleScopeGlobal  = agentpack.RoleScopeGlobal
)

// Action states.
const (
	ActionCreate          = "create"
	ActionExistsIdentical = "exists_identical"
	ActionConflict        = "conflict"
	// ActionPublish is the role_revision action for a Role revision that must be
	// published; an already-published digest reports exists_identical.
	ActionPublish = "publish"
)

// Issue severities.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Installer warning codes.
const (
	// WarningRenamedFlow reports a flow installed under a different Graph id.
	WarningRenamedFlow = "pack_flow_renamed"
	// WarningRenamedReference reports a Role installed under a different handle.
	WarningRenamedReference = "pack_role_renamed"
	// WarningSkillKept reports a locally edited Skill kept on remove.
	WarningSkillKept = "pack_skill_kept"
)

// Renames changes the installed store ids only. Pack-internal references are
// never rewritten: a rename is for avoiding collisions with what is already on
// the Worker, not for restructuring a pack.
type Renames struct {
	Flows map[string]string `json:"flows,omitempty"`
	Roles map[string]string `json:"roles,omitempty"`
}

// Request is one plan or install request.
type Request struct {
	SourcePath     string
	ExpectedDigest string
	DryRun         bool
	Renames        Renames
}

// Action is one planned store write.
type Action struct {
	Kind    string `json:"kind"`
	StoreID string `json:"storeId"`
	Action  string `json:"action"`
	Scope   string `json:"scope,omitempty"`
}

// Issue is one blocker or warning.
type Issue struct {
	Code     string `json:"code"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

// Plan is the install verdict. Blockers are non-empty when the pack cannot be
// installed; every blocker is non-retryable until the pack or the Worker store
// state changes.
type Plan struct {
	PlanVersion             int          `json:"planVersion"`
	Pack                    PackIdentity `json:"pack"`
	ManifestDigest          string       `json:"manifestDigest"`
	PackDigest              string       `json:"packDigest"`
	Actions                 []Action     `json:"actions"`
	Blockers                []Issue      `json:"blockers"`
	Warnings                []Issue      `json:"warnings"`
	CommitPoint             string       `json:"commitPoint"`
	WillPublishFlowRevision bool         `json:"willPublishFlowRevision"`
	Reinstall               string       `json:"reinstall,omitempty"`
	State                   *State       `json:"state,omitempty"`

	// install carries the parsed documents the writer needs. It is never
	// serialized: clients receive the plan, not the pack payload.
	install *installPlan
}

// installPlan is the internal, write-ready form of one approved plan.
type installPlan struct {
	manifest          []byte
	flows             []*flowTarget
	roles             []*roleTarget
	skills            []*skillTarget
	externalRefs      ExternalRefs
	modelRequirements agentpack.ModelEnvelope
}

type flowTarget struct {
	packFlowID      string
	storeID         string
	sourceDigest    string
	installedDigest string
	// document is the installed form: its ID is storeID.
	document *graphsource.Document
	action   string
}

type roleTarget struct {
	packHandle string
	graphID    string // "" for a global Role
	storeID    string // "<handle>" or "<flow>/<handle>"
	scope      string
	digest     string
	// document is the installed form: its Handle is the store handle.
	document       *rolesource.Document
	action         string
	revisionAction string
	revision       string
}

type skillTarget struct {
	packID  string
	graphID string // "" for a global Skill
	storeID string // "<id>" or "<flow>/<id>"
	scope   string
	root    string
	input   skillsmgmt.CreateInput
	digest  string
	action  string
}

// Plan reads the pack, validates it against S2a limits, and reports exactly what
// an install would write. It never writes anything, including staging.
func (s *Service) Plan(ctx context.Context, request Request) (*Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plan(ctx, request)
}

func (s *Service) plan(ctx context.Context, request Request) (*Plan, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	sourcePath := strings.TrimSpace(request.SourcePath)
	result, err := agentpack.Inspect(agentpack.Request{
		Source:         agentpack.Source{Kind: agentpack.SourceKindPath, Path: sourcePath},
		ExpectedDigest: request.ExpectedDigest,
	})
	if err != nil {
		return nil, err
	}
	pkg, err := agentpack.ReadPath(sourcePath)
	if err != nil {
		return nil, err
	}
	manifestFile, ok := pkg.Lookup(agentpack.ManifestName)
	if !ok {
		return nil, newError(agentpack.CodePackManifestMissing, "pack root is missing pack.yaml")
	}
	manifest, err := agentpack.ParseManifest(manifestFile.Data)
	if err != nil {
		return nil, err
	}

	planner := &planner{
		service: s, ctx: ctx, request: request,
		inspect: result, manifest: manifest, pkg: pkg,
		flowStoreIDs:  map[string]string{},
		roleStoreIDs:  map[string]string{},
		flowDocs:      map[string]*graphsource.Document{},
		roleDocs:      map[string]*rolesource.Document{},
		flowRoleRefs:  map[string]map[string]bool{},
		flowSkillRefs: map[string]map[string]bool{},
		skillEntries:  map[string]agentpack.SkillEntry{},
		skillInputs:   map[string]skillsmgmt.CreateInput{},
	}
	planner.plan = &Plan{
		PlanVersion:             PlanVersion,
		Pack:                    PackIdentity{ID: manifest.ID, Name: manifest.Name, Version: manifest.Version},
		ManifestDigest:          result.ManifestDigest,
		PackDigest:              result.PackDigest,
		Actions:                 []Action{},
		Blockers:                []Issue{},
		Warnings:                []Issue{},
		CommitPoint:             CommitPointStateFile,
		WillPublishFlowRevision: false,
	}
	for _, warning := range result.Warnings {
		planner.warn(warning.Code, warning.Severity, warning.Message)
	}
	planner.plan.install = &installPlan{
		manifest:          manifestFile.Data,
		externalRefs:      ExternalRefs{Skills: []string{}, MCP: []string{}},
		modelRequirements: normalizeModelEnvelope(result.CapabilityEnvelope.ModelRequirements),
	}

	planner.checkLimits()
	planner.resolveStoreIDs()
	if err := planner.loadDocuments(); err != nil {
		return nil, err
	}
	planner.checkModels()
	if err := planner.loadSkills(); err != nil {
		return nil, err
	}
	planner.resolveReferences()
	planner.checkStoreIDCollisions()
	if err := planner.planFlows(); err != nil {
		return nil, err
	}
	if err := planner.planRoles(); err != nil {
		return nil, err
	}
	if err := planner.planSkills(); err != nil {
		return nil, err
	}
	planner.sortActions()
	if err := planner.checkExistingInstall(); err != nil {
		return nil, err
	}
	return planner.plan, nil
}

type planner struct {
	service  *Service
	ctx      context.Context
	request  Request
	inspect  *agentpack.Result
	manifest *agentpack.Manifest
	pkg      *agentpack.Package

	plan *Plan

	// flowStoreIDs / roleStoreIDs map pack ids onto installed store ids after
	// renames.
	flowStoreIDs map[string]string
	roleStoreIDs map[string]string

	flowDocs map[string]*graphsource.Document
	roleDocs map[string]*rolesource.Document

	// flowRoleRefs and flowSkillRefs record which Graph-private object each flow
	// pulls in, so a shared object is installed below every referencing flow.
	flowRoleRefs  map[string]map[string]bool
	flowSkillRefs map[string]map[string]bool

	skillEntries map[string]agentpack.SkillEntry
	skillInputs  map[string]skillsmgmt.CreateInput
}

func (p *planner) blocker(code, message string) {
	p.plan.Blockers = append(p.plan.Blockers, Issue{Code: code, Severity: SeverityError, Message: message})
}

func (p *planner) warn(code, severity, message string) {
	if severity == "" {
		severity = SeverityWarning
	}
	p.plan.Warnings = append(p.plan.Warnings, Issue{Code: code, Severity: severity, Message: message})
}

// checkLimits rejects everything S2a cannot install. Each rejection names the
// reason instead of silently degrading the pack.
func (p *planner) checkLimits() {
	for _, flow := range p.inspect.Flows {
		if flow.Format != agentpack.FlowFormatFlow {
			continue
		}
		p.blocker(CodeFlowFormatUnsupported, fmt.Sprintf(
			"flow %q uses format: flow (%d check task(s), %d fan-out task(s)); installing a full Agent Flow definition needs the S2b Flow source, CheckTaskRunner, and check approval path, so S2a refuses it instead of downgrading",
			flow.ID, flow.CheckTasks, flow.FanOutTasks))
	}
	if len(p.inspect.MCP) > 0 {
		refs := make([]string, 0, len(p.inspect.MCP))
		for _, entry := range p.inspect.MCP {
			refs = append(refs, entry.Ref)
		}
		p.blocker(CodeMCPUnsupported, fmt.Sprintf(
			"pack declares external MCP profile reference(s) %s; S2a installs no MCP profiles",
			strings.Join(refs, ", ")))
	}
	if len(p.inspect.Requires) > 0 {
		p.blocker(CodeDependencyUnsupported, fmt.Sprintf(
			"pack requires other pack(s) %s; S2a installs no pack-to-pack dependencies",
			strings.Join(p.inspect.Requires, ", ")))
	}
}

// resolveStoreIDs applies renames and validates that every rename names a
// declared pack object (a rename for an unknown id is a caller mistake).
func (p *planner) resolveStoreIDs() {
	flowIDs := make(map[string]bool, len(p.manifest.Flows))
	for _, entry := range p.manifest.Flows {
		flowIDs[entry.ID] = true
	}
	roleHandles := make(map[string]bool, len(p.manifest.Roles))
	for _, entry := range p.manifest.Roles {
		roleHandles[entry.Handle] = true
	}
	for _, packID := range sortedKeys(p.request.Renames.Flows) {
		storeID := strings.TrimSpace(p.request.Renames.Flows[packID])
		if !flowIDs[packID] {
			p.blocker(CodeFlowIDConflict, fmt.Sprintf("rename names flow %q, which the pack does not declare", packID))
			continue
		}
		if !validFlowStoreID(storeID) {
			p.blocker(CodeFlowIDConflict, fmt.Sprintf(
				"rename %q -> %q is not a valid Graph id (lowercase letter, then letters, digits or '-')", packID, storeID))
			continue
		}
		p.flowStoreIDs[packID] = storeID
	}
	for _, packHandle := range sortedKeys(p.request.Renames.Roles) {
		storeID := strings.TrimSpace(p.request.Renames.Roles[packHandle])
		if !roleHandles[packHandle] {
			p.blocker(CodeRoleHandleConflict, fmt.Sprintf("rename names role %q, which the pack does not declare", packHandle))
			continue
		}
		if !validRoleStoreHandle(storeID) {
			p.blocker(CodeRoleHandleConflict, fmt.Sprintf(
				"rename %q -> %q is not a valid Role handle (lowercase letter, then up to 31 letters, digits, '-' or '_')", packHandle, storeID))
			continue
		}
		p.roleStoreIDs[packHandle] = storeID
	}
	for _, entry := range p.manifest.Flows {
		if _, ok := p.flowStoreIDs[entry.ID]; !ok {
			p.flowStoreIDs[entry.ID] = entry.ID
		}
	}
	for _, entry := range p.manifest.Roles {
		if _, ok := p.roleStoreIDs[entry.Handle]; !ok {
			p.roleStoreIDs[entry.Handle] = entry.Handle
		}
	}

}

// checkStoreIDCollisions rejects renames that map two pack objects onto one store
// id: within one transaction the second write would collide with the first.
func (p *planner) checkStoreIDCollisions() {
	flowOwners := map[string]string{}
	for _, entry := range p.manifest.Flows {
		storeID := p.flowStoreIDs[entry.ID]
		if owner, taken := flowOwners[storeID]; taken && owner != entry.ID {
			p.blocker(CodeFlowIDConflict, fmt.Sprintf(
				"flows %q and %q both install as Graph %q; renames must keep store ids distinct", owner, entry.ID, storeID))
			continue
		}
		flowOwners[storeID] = entry.ID
	}
	roleOwners := map[string]string{}
	for _, entry := range p.manifest.Roles {
		storeID := p.roleStoreIDs[entry.Handle]
		key := storeID
		if entry.Scope == agentpack.RoleScopeFlow {
			// Graph-private Roles live under their flow, so the identity is
			// per-flow: <flow>/<handle>.
			flows := p.flowsReferencingRole(entry.Handle)
			if len(flows) == 0 {
				continue
			}
			for _, flowStoreID := range flows {
				key = flowStoreID + "/" + storeID
				if owner, taken := roleOwners[key]; taken && owner != entry.Handle {
					p.blocker(CodeRoleHandleConflict, fmt.Sprintf(
						"roles %q and %q both install as %q; renames must keep store ids distinct", owner, entry.Handle, key))
					continue
				}
				roleOwners[key] = entry.Handle
			}
			continue
		}
		if owner, taken := roleOwners[key]; taken && owner != entry.Handle {
			p.blocker(CodeRoleHandleConflict, fmt.Sprintf(
				"roles %q and %q both install as global Role %q; renames must keep store ids distinct", owner, entry.Handle, key))
			continue
		}
		roleOwners[key] = entry.Handle
	}
}

// loadDocuments parses every flow and role source a second time from the same
// in-memory package, so the installer writes exactly the bytes inspect reviewed.
func (p *planner) loadDocuments() error {
	for _, entry := range p.manifest.Flows {
		file, ok := p.pkg.Lookup(canonicalPackPath(entry.Path))
		if !ok {
			continue // inspect already reported the missing source
		}
		switch entry.Format {
		case agentpack.FlowFormatGraph:
			document, err := graphsource.Parse(file.Data)
			if err != nil {
				return newError(agentpack.CodePackFlowInvalid,
					fmt.Sprintf("parse Graph source %s: %v", entry.Path, err))
			}
			p.flowDocs[entry.ID] = document
		}
	}
	for _, entry := range p.manifest.Roles {
		file, ok := p.pkg.Lookup(canonicalPackPath(entry.Path))
		if !ok {
			continue
		}
		document, err := rolesource.Parse(file.Data)
		if err != nil {
			return newError(agentpack.CodePackRoleInvalid,
				fmt.Sprintf("parse Role source %s: %v", entry.Path, err))
		}
		p.roleDocs[entry.Handle] = document
	}
	return nil
}

// checkModels resolves every Role model ref and every inline Task model ref
// against the local model catalog. A pack never names a concrete model, so an
// unresolvable ref is a hard install blocker rather than a run-time surprise.
func (p *planner) checkModels() {
	for _, entry := range p.manifest.Roles {
		document, ok := p.roleDocs[entry.Handle]
		if !ok {
			continue
		}
		refs := append([]string{document.Model.Ref}, document.Model.Fallbacks...)
		for _, ref := range refs {
			if _, err := p.service.Models.ResolvePortableRef(p.ctx, ref); err != nil {
				p.blocker(CodeModelUnresolved, fmt.Sprintf(
					"role %q model ref %q: %v", entry.Handle, ref, err))
			}
		}
	}
	for _, packFlowID := range sortedKeys(p.flowDocs) {
		document := p.flowDocs[packFlowID]
		for _, taskID := range sortedKeys(document.Tasks) {
			task := document.Tasks[taskID]
			if task.Role != "" {
				continue
			}
			if _, err := p.service.Models.ResolvePortableRef(p.ctx, task.Model); err != nil {
				p.blocker(CodeModelUnresolved, fmt.Sprintf(
					"flow %q task %q model ref %q: %v", packFlowID, taskID, task.Model, err))
			}
		}
	}
}

// loadSkills validates every vendored skill and resolves every external skill
// ref. Vendored skills must be prompt-only: S2a installs a Skill, not an
// executable payload.
func (p *planner) loadSkills() error {
	for _, entry := range p.manifest.Skills {
		if entry.Ref != "" {
			p.plan.install.externalRefs.Skills = append(p.plan.install.externalRefs.Skills, entry.Ref)
			if err := p.checkExternalSkill(entry.Ref); err != nil {
				p.blocker(CodeSkillUnresolved, err.Error())
			}
			continue
		}
		p.skillEntries[entry.ID] = entry
		directory := canonicalPackPath(entry.Path)
		for _, file := range p.pkg.FilesUnder(directory) {
			relative := strings.TrimPrefix(file.Path, directory+"/")
			switch relative {
			case "skill.json", "SKILL.md", "skill.md":
			default:
				p.blocker(CodeSkillNotPromptOnly, fmt.Sprintf(
					"vendored skill %q contains %q; S2a installs prompt-only skills (skill.json and SKILL.md only)",
					entry.ID, relative))
			}
		}
		input, err := p.skillInput(entry, directory)
		if err != nil {
			p.blocker(CodeSkillInvalid, err.Error())
			continue
		}
		p.skillInputs[entry.ID] = input
	}
	p.plan.install.externalRefs.Skills = sortedUniqueStrings(p.plan.install.externalRefs.Skills)
	p.plan.install.externalRefs.MCP = make([]string, 0, len(p.inspect.MCP))
	for _, entry := range p.inspect.MCP {
		p.plan.install.externalRefs.MCP = append(p.plan.install.externalRefs.MCP, entry.Ref)
	}
	return nil
}

func (p *planner) checkExternalSkill(ref string) error {
	id, _, found := strings.Cut(ref, "@")
	if !found || id == "" {
		return fmt.Errorf("external skill ref %q must use name@version", ref)
	}
	if _, err := skills.Load(filepath.Join(p.service.SkillsRoot, id)); err != nil {
		return fmt.Errorf("external skill ref %q: %v", ref, err)
	}
	return nil
}

// skillInput converts a vendored prompt-only skill into the canonical
// skillsmgmt.CreateInput. The installed Skill is re-rendered by the same writer
// the Skills UI uses, so it validates with the real loader.
func (p *planner) skillInput(entry agentpack.SkillEntry, directory string) (skillsmgmt.CreateInput, error) {
	files := map[string]agentpack.File{}
	for _, file := range p.pkg.FilesUnder(directory) {
		files[strings.TrimPrefix(file.Path, directory+"/")] = file
	}
	var manifest skills.Manifest
	if file, ok := files["skill.json"]; ok {
		if err := json.Unmarshal(file.Data, &manifest); err != nil {
			return skillsmgmt.CreateInput{}, fmt.Errorf("vendored skill %q skill.json: %v", entry.ID, err)
		}
		if manifest.Prompt != "" && manifest.Prompt != "SKILL.md" {
			return skillsmgmt.CreateInput{}, fmt.Errorf(
				"vendored skill %q skill.json prompt must be empty or SKILL.md, got %q", entry.ID, manifest.Prompt)
		}
	}
	promptFile, ok := files["SKILL.md"]
	if !ok {
		promptFile, ok = files["skill.md"]
	}
	if !ok {
		return skillsmgmt.CreateInput{}, fmt.Errorf(
			"vendored skill %q has no SKILL.md prompt; S2a installs prompt-only skills", entry.ID)
	}
	body := stripSkillFrontmatter(promptFile.Data)
	if strings.TrimSpace(body) == "" {
		return skillsmgmt.CreateInput{}, fmt.Errorf("vendored skill %q prompt is empty", entry.ID)
	}
	name := skillFrontmatterValue(promptFile.Data, "name")
	if name == "" {
		name = manifest.ID
	}
	if name == "" {
		name = entry.ID
	}
	description := manifest.Description
	if description == "" {
		description = skillFrontmatterValue(promptFile.Data, "description")
	}
	return skillsmgmt.CreateInput{
		ID:           entry.ID,
		Name:         name,
		Description:  description,
		Prompt:       body,
		AllowedTools: manifest.AllowedTools,
	}, nil
}

// resolveReferences links flows to the Graph-private roles and skills they pull
// in, and rejects references whose declared scope contradicts the reference.
func (p *planner) resolveReferences() {
	for _, entry := range p.manifest.Flows {
		if entry.Format != agentpack.FlowFormatGraph {
			continue
		}
		refs := map[string]bool{}
		for _, handle := range entry.Roles {
			refs[handle] = true
		}
		document := p.flowDocs[entry.ID]
		if document != nil {
			for _, taskID := range sortedKeys(document.Tasks) {
				task := document.Tasks[taskID]
				if scope, handle, ok := strings.Cut(task.Role, "/"); ok && scope == "local" {
					refs[handle] = true
					if _, declared := p.roleDocs[handle]; !declared {
						p.blocker(CodeRoleHandleConflict, fmt.Sprintf(
							"flow %q task %q references local Role %q, which the pack does not declare", entry.ID, taskID, handle))
					}
				} else if !strings.Contains(task.Role, "/") && task.Role != "" {
					// Bare handle: flow-local when the pack declares it.
					if _, declared := p.roleDocs[task.Role]; declared {
						refs[task.Role] = true
					}
				}
				for _, ref := range task.Skills {
					scope, id, ok := strings.Cut(ref, "/")
					if !ok {
						p.checkSkillRef(entry.ID, "flow task "+taskID, "global", ref)
						continue
					}
					p.checkSkillRef(entry.ID, "flow task "+taskID, scope, id)
				}
			}
		}
		p.flowRoleRefs[entry.ID] = refs
	}
	// Role preloads are rendered from the Worker user skills root, not from a
	// Graph-private directory. A local/ binding would therefore install but never
	// resolve; require a global vendored or already-installed Skill instead.
	for _, entry := range p.manifest.Roles {
		document, ok := p.roleDocs[entry.Handle]
		if !ok {
			continue
		}
		for _, binding := range document.Skills {
			scope, id, scoped := strings.Cut(binding.ID, "/")
			if !scoped {
				scope, id = "global", binding.ID
			}
			switch scope {
			case "local":
				p.blocker(CodeSkillUnresolved, fmt.Sprintf(
					"role %q preloads local/%s; Role preloads resolve from the Worker user skills root, so only global/ (or a bare) Skill reference is supported", entry.Handle, id))
			case "global":
				if skillEntry, declared := p.skillEntries[id]; declared {
					if skillEntry.Scope != agentpack.SkillScopeGlobal {
						p.blocker(CodeSkillUnresolved, fmt.Sprintf(
							"role %q preloads global/%s but the pack declares it with scope: flow; Role preloads need a global Skill", entry.Handle, id))
					}
					continue
				}
				if _, err := skills.Load(filepath.Join(p.service.SkillsRoot, id)); err != nil {
					p.blocker(CodeSkillUnresolved, fmt.Sprintf(
						"role %q preloads global/%s, which is not installed in the Worker user skills root", entry.Handle, id))
				}
			}
		}
	}
	// A flow-scoped Role's local/ Skill bindings are Graph-local Skills too: the
	// Role is installed below the flow, so its Skill must be there as well.
	for _, entry := range p.manifest.Flows {
		if entry.Format != agentpack.FlowFormatGraph {
			continue
		}
		for _, handle := range sortedKeys(p.flowRoleRefs[entry.ID]) {
			roleEntry, declared := p.roleEntry(handle)
			if !declared || roleEntry.Scope != agentpack.RoleScopeFlow {
				continue
			}
			document, ok := p.roleDocs[handle]
			if !ok {
				continue
			}
			for _, binding := range document.Skills {
				scope, id, ok := strings.Cut(binding.ID, "/")
				if !ok || scope != "local" {
					continue
				}
				p.checkSkillRef(entry.ID, "role "+handle, scope, id)
			}
		}
	}
	// Every flow-scoped Skill must land somewhere.
	for _, entry := range p.manifest.Skills {
		if entry.Ref != "" || entry.Scope != agentpack.SkillScopeFlow {
			continue
		}
		referenced := false
		for _, refs := range p.flowSkillRefs {
			if refs[entry.ID] {
				referenced = true
			}
		}
		if !referenced {
			p.blocker(CodeSkillInvalid, fmt.Sprintf(
				"vendored skill %q is declared with scope: flow but no flow references it, so it would be installed nowhere", entry.ID))
		}
	}
}

func (p *planner) checkSkillRef(flowID, origin, scope, id string) {
	if scope != "local" && scope != "global" {
		return
	}
	entry, declared := p.skillEntries[id]
	switch {
	case !declared && scope == "local":
		p.blocker(CodeSkillUnresolved, fmt.Sprintf(
			"%s of flow %q references local Skill %q, which the pack does not vendor", origin, flowID, id))
	case !declared:
		if _, err := skills.Load(filepath.Join(p.service.SkillsRoot, id)); err != nil {
			p.blocker(CodeSkillUnresolved, fmt.Sprintf(
				"%s of flow %q references global Skill %q, which is not installed in the Worker user skills root", origin, flowID, id))
		}
	case scope == "local" && entry.Scope != agentpack.SkillScopeFlow:
		p.blocker(CodeSkillUnresolved, fmt.Sprintf(
			"%s of flow %q references local/%s but the pack declares it with scope: %s; a local/ reference needs scope: flow",
			origin, flowID, id, entry.Scope))
	case scope == "global" && entry.Scope == agentpack.SkillScopeFlow:
		p.blocker(CodeSkillUnresolved, fmt.Sprintf(
			"%s of flow %q references global/%s but the pack declares it with scope: flow; a global/ reference needs scope: global",
			origin, flowID, id))
	case scope == "local":
		refs := p.flowSkillRefs[flowID]
		if refs == nil {
			refs = map[string]bool{}
			p.flowSkillRefs[flowID] = refs
		}
		refs[id] = true
	}
}

func (p *planner) roleEntry(handle string) (agentpack.RoleEntry, bool) {
	for _, entry := range p.manifest.Roles {
		if entry.Handle == handle {
			return entry, true
		}
	}
	return agentpack.RoleEntry{}, false
}

func (p *planner) planFlows() error {
	sourceDigests := make(map[string]string, len(p.inspect.Flows))
	for _, summary := range p.inspect.Flows {
		sourceDigests[summary.ID] = summary.SourceDigest
	}
	for _, entry := range p.manifest.Flows {
		if entry.Format != agentpack.FlowFormatGraph {
			continue
		}
		document, ok := p.flowDocs[entry.ID]
		if !ok {
			continue
		}
		storeID := p.flowStoreIDs[entry.ID]
		installed := *document
		installed.ID = storeID
		digest, err := graphsource.SourceDigest(&installed)
		if err != nil {
			return fmt.Errorf("digest installed Graph %s: %w", storeID, err)
		}
		action := p.service.graphAction(storeID, digest)
		if action == ActionConflict {
			p.blocker(CodeFlowIDConflict, fmt.Sprintf(
				"Graph %q already exists with different content; S2a never overwrites a Graph", storeID))
		}
		if storeID != entry.ID {
			p.warn(WarningRenamedFlow, SeverityInfo, fmt.Sprintf(
				"flow %q is installed as Graph %q; in-pack references are not rewritten", entry.ID, storeID))
		}
		p.plan.install.flows = append(p.plan.install.flows, &flowTarget{
			packFlowID: entry.ID, storeID: storeID,
			sourceDigest: sourceDigests[entry.ID], installedDigest: digest,
			document: &installed, action: action,
		})
		p.plan.Actions = append(p.plan.Actions, Action{
			Kind: ActionKindGraph, StoreID: storeID, Action: action,
		})
	}
	return nil
}

func (p *planner) planRoles() error {
	for _, entry := range p.manifest.Roles {
		document, ok := p.roleDocs[entry.Handle]
		if !ok {
			continue
		}
		storeID := p.roleStoreIDs[entry.Handle]
		if storeID != entry.Handle && len(p.manifest.Flows) > 0 {
			p.blocker(CodeRoleHandleConflict, fmt.Sprintf(
				"role %q cannot be renamed in a pack with flows: S2a does not rewrite flow task role references; rename it before packaging", entry.Handle))
			continue
		}
		installed := *document
		installed.Handle = storeID
		digest, err := rolesource.SourceDigest(&installed)
		if err != nil {
			return fmt.Errorf("digest installed Role %s: %w", storeID, err)
		}
		if storeID != entry.Handle {
			p.warn(WarningRenamedReference, SeverityInfo, fmt.Sprintf(
				"role %q is installed as %q; in-pack references are not rewritten", entry.Handle, storeID))
		}
		switch entry.Scope {
		case agentpack.RoleScopeFlow:
			for _, flowStoreID := range p.flowsReferencingRole(entry.Handle) {
				targetStoreID := flowStoreID + "/" + storeID
				action := p.service.graphRoleAction(flowStoreID, storeID, digest)
				if action == ActionConflict {
					p.blocker(CodeRoleHandleConflict, fmt.Sprintf(
						"Graph-private Role %q already exists with different content", targetStoreID))
				}
				p.plan.install.roles = append(p.plan.install.roles, &roleTarget{
					packHandle: entry.Handle, graphID: flowStoreID, storeID: targetStoreID,
					scope: entry.Scope, digest: digest, document: &installed, action: action,
				})
				p.plan.Actions = append(p.plan.Actions, Action{
					Kind: ActionKindGraphRole, StoreID: targetStoreID, Action: action, Scope: entry.Scope,
				})
			}
		case agentpack.RoleScopeGlobal:
			action := p.service.roleAction(storeID, digest)
			if action == ActionConflict {
				p.blocker(CodeRoleHandleConflict, fmt.Sprintf(
					"global Role %q already exists with different content", storeID))
			}
			revisionAction := p.service.roleRevisionAction(storeID, digest)
			target := &roleTarget{
				packHandle: entry.Handle, storeID: storeID, scope: entry.Scope,
				digest: digest, document: &installed, action: action, revisionAction: revisionAction,
			}
			p.plan.install.roles = append(p.plan.install.roles, target)
			p.plan.Actions = append(p.plan.Actions,
				Action{Kind: ActionKindRole, StoreID: storeID, Action: action, Scope: entry.Scope},
				Action{Kind: ActionKindRoleRevision, StoreID: storeID, Action: revisionAction, Scope: entry.Scope},
			)
		}
	}
	return nil
}

func (p *planner) flowsReferencingRole(handle string) []string {
	flows := make([]string, 0, len(p.flowRoleRefs))
	for packFlowID := range p.flowRoleRefs {
		if !p.flowRoleRefs[packFlowID][handle] {
			continue
		}
		flows = append(flows, p.flowStoreIDs[packFlowID])
	}
	sort.Strings(flows)
	return flows
}

func (p *planner) planSkills() error {
	for _, id := range sortedKeys(p.skillEntries) {
		entry := p.skillEntries[id]
		input, ok := p.skillInputs[id]
		if !ok {
			continue
		}
		if entry.Scope == agentpack.SkillScopeGlobal {
			target := &skillTarget{
				packID: id, storeID: id, scope: entry.Scope,
				root: p.service.SkillsRoot, input: input,
			}
			if err := p.assignSkillAction(target); err != nil {
				return err
			}
			p.plan.install.skills = append(p.plan.install.skills, target)
			p.plan.Actions = append(p.plan.Actions, Action{
				Kind: ActionKindSkill, StoreID: target.storeID, Action: target.action, Scope: entry.Scope,
			})
			continue
		}
		for _, packFlowID := range sortedKeys(p.flowSkillRefs) {
			if !p.flowSkillRefs[packFlowID][id] {
				continue
			}
			graphID := p.flowStoreIDs[packFlowID]
			target := &skillTarget{
				packID: id, graphID: graphID, storeID: graphID + "/" + id, scope: entry.Scope,
				root: filepath.Join(p.service.Sources.GraphsDir(), graphID, "skills"), input: input,
			}
			if err := p.assignSkillAction(target); err != nil {
				return err
			}
			p.plan.install.skills = append(p.plan.install.skills, target)
			p.plan.Actions = append(p.plan.Actions, Action{
				Kind: ActionKindSkill, StoreID: target.storeID, Action: target.action, Scope: entry.Scope,
			})
		}
	}
	return nil
}

func (p *planner) assignSkillAction(target *skillTarget) error {
	state, digest, err := skillsmgmt.InspectWrite(target.root, target.input)
	if err != nil {
		if errors.Is(err, skillsmgmt.ErrSkillExists) {
			target.action = ActionConflict
			p.blocker(CodeSkillConflict, fmt.Sprintf(
				"Skill %q already exists with different content", target.storeID))
			return nil
		}
		return err
	}
	target.digest = digest
	if state == skillsmgmt.WriteStateExistsIdentical {
		target.action = ActionExistsIdentical
	} else {
		target.action = ActionCreate
	}
	return nil
}

func (p *planner) sortActions() {
	rank := map[string]int{
		ActionKindGraph: 0, ActionKindGraphRole: 1, ActionKindRole: 2,
		ActionKindRoleRevision: 3, ActionKindSkill: 4,
	}
	sort.SliceStable(p.plan.Actions, func(i, j int) bool {
		left, right := p.plan.Actions[i], p.plan.Actions[j]
		if rank[left.Kind] != rank[right.Kind] {
			return rank[left.Kind] < rank[right.Kind]
		}
		if left.StoreID != right.StoreID {
			return left.StoreID < right.StoreID
		}
		return left.Scope < right.Scope
	})
}

// checkExistingInstall enforces the S2a no-update rule: the same pack at the same
// version and digest reinstalls as a no-op only when every object is already
// byte-identical, and every other combination is a conflict.
func (p *planner) checkExistingInstall() error {
	state, err := p.service.readState(p.manifest.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if state.Pack.Version != p.manifest.Version {
		p.blocker(CodeVersionConflict, fmt.Sprintf(
			"pack %s is installed at version %s; S2a has no update path (requested %s)",
			p.manifest.ID, state.Pack.Version, p.manifest.Version))
		return nil
	}
	if state.PackDigest != p.plan.PackDigest {
		p.blocker(CodeAlreadyInstalled, fmt.Sprintf(
			"pack %s %s is installed from different bytes (installed %s, requested %s); remove it before installing another build",
			p.manifest.ID, state.Pack.Version, state.PackDigest, p.plan.PackDigest))
		return nil
	}
	if allActionsIdentical(p.plan.Actions) {
		p.plan.Reinstall = ReinstallNoop
		p.plan.State = state
		return nil
	}
	p.blocker(CodeAlreadyInstalled, fmt.Sprintf(
		"pack %s %s is already installed; S2a has no reinstall-with-different-ids path, remove it first",
		p.manifest.ID, state.Pack.Version))
	return nil
}

func allActionsIdentical(actions []Action) bool {
	for _, action := range actions {
		if action.Action != ActionExistsIdentical {
			return false
		}
	}
	return len(actions) > 0
}

func (s *Service) graphAction(storeID, digest string) string {
	_, existing, err := s.Sources.ReadGraph(storeID)
	switch {
	case err == nil:
		if existing == digest {
			return ActionExistsIdentical
		}
		return ActionConflict
	case errors.Is(err, os.ErrNotExist):
		return ActionCreate
	default:
		return ActionConflict
	}
}

func (s *Service) graphRoleAction(graphID, handle, digest string) string {
	_, existing, err := s.Sources.ReadGraphRole(graphID, handle)
	switch {
	case err == nil:
		if existing == digest {
			return ActionExistsIdentical
		}
		return ActionConflict
	case errors.Is(err, os.ErrNotExist):
		return ActionCreate
	default:
		return ActionConflict
	}
}

func (s *Service) roleAction(handle, digest string) string {
	_, existing, err := s.Sources.ReadRole(handle)
	switch {
	case err == nil:
		if existing == digest {
			return ActionExistsIdentical
		}
		return ActionConflict
	case errors.Is(err, os.ErrNotExist):
		return ActionCreate
	default:
		return ActionConflict
	}
}

func (s *Service) roleRevisionAction(handle, digest string) string {
	revisions, err := s.Sources.ListRoleRevisions(handle)
	if err != nil {
		return ActionPublish
	}
	for _, revision := range revisions {
		if revision.Digest == digest {
			return ActionExistsIdentical
		}
	}
	return ActionPublish
}

// canonicalPackPath is the canonical lookup form of a manifest path (inspect has
// already validated and normalized it).
func canonicalPackPath(raw string) string {
	path := strings.TrimSpace(raw)
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimSuffix(path, "/")
	return path
}

func normalizeModelEnvelope(envelope agentpack.ModelEnvelope) agentpack.ModelEnvelope {
	if envelope.Capabilities == nil {
		envelope.Capabilities = []string{}
	}
	if envelope.PerRole == nil {
		envelope.PerRole = map[string][]string{}
	}
	return envelope
}

// stripSkillFrontmatter drops a leading YAML frontmatter block from a SKILL.md
// body. skillsmgmt.Write renders its own frontmatter, so keeping the pack's
// would nest two blocks.
func stripSkillFrontmatter(data []byte) string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return strings.TrimSpace(text)
	}
	rest := text[len("---\n"):]
	index := strings.Index(rest, "\n---\n")
	if index < 0 {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(rest[index+len("\n---\n"):])
}

// skillFrontmatterValue reads one scalar key from a SKILL.md frontmatter block.
func skillFrontmatterValue(data []byte, key string) string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	rest := text[len("---\n"):]
	index := strings.Index(rest, "\n---\n")
	if index < 0 {
		return ""
	}
	for _, line := range strings.Split(rest[:index], "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found || strings.TrimSpace(name) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// validFlowStoreID matches graphsource's Graph id contract.
func validFlowStoreID(value string) bool {
	return graphIDPattern.MatchString(value)
}

// validRoleStoreHandle matches rolesource's Role handle contract.
func validRoleStoreHandle(value string) bool {
	return roleHandlePattern.MatchString(value)
}

var (
	graphIDPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	roleHandlePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)
)
