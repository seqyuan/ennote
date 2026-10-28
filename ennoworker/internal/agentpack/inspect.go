package agentpack

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
)

// Inspect reads a pack from a host path and reports its structure without
// installing, executing, or trusting anything.
//
// The pipeline is: read payload (no extraction, no temp writes) -> parse
// manifest strictly -> parse each flow/role/skill source -> check
// cross-references -> compute digests -> collect warnings. Any failure is fatal:
// inspect never returns a partial success.
func Inspect(request Request) (*Result, error) {
	source := request.Source
	if source.Kind == "" {
		source.Kind = SourceKindPath
	}
	source.Path = strings.TrimSpace(source.Path)
	if source.Kind != SourceKindPath {
		return nil, errorf(CodePackFormatUnsupported,
			"unsupported pack source kind %q (S1 only supports %q)", source.Kind, SourceKindPath)
	}
	if source.Path == "" {
		return nil, newError(CodePackPathInvalid, "pack path is required")
	}

	pkg, err := ReadPath(source.Path)
	if err != nil {
		return nil, err
	}
	manifestFile, ok := pkg.Lookup(ManifestName)
	if !ok {
		return nil, errorf(CodePackManifestMissing, "pack root is missing %s", ManifestName)
	}
	manifest, err := ParseManifest(manifestFile.Data)
	if err != nil {
		return nil, err
	}

	builder := &builder{
		pkg:      pkg,
		manifest: manifest,
		roleSet:  map[string]bool{},
		skillIDs: map[string]bool{},
		perRole:  map[string][]string{},
	}
	if err := builder.analyse(); err != nil {
		return nil, err
	}

	result := builder.result(source, request.IncludeDocuments)
	if expected := normalizeDigest(request.ExpectedDigest); expected != "" {
		if expected != result.PackDigest && expected != result.ManifestDigest {
			return nil, errorf(CodePackDigestMismatch,
				"expected digest %s does not match packDigest %s or manifestDigest %s",
				expected, result.PackDigest, result.ManifestDigest)
		}
	}
	return result, nil
}

type builder struct {
	pkg      *Package
	manifest *Manifest

	flows  []FlowSummary
	roles  []RoleSummary
	skills []SkillSummary
	// roleSet avoids a second pass over roles while checking references.
	roleSet map[string]bool
	// skillIDs is the vendored skill id set.
	skillIDs map[string]bool
	// flowRoleRefs is the union of handles referenced by flow entries and by
	// local/<handle> task role references.
	flowRoleRefs map[string]bool
	// perRole carries model requirement overrides keyed by role handle.
	perRole map[string][]string

	checkCommands []string
	writes        []string
	warnings      []Warning

	documents          []DocumentText
	documentsBytes     int64
	documentsTruncated bool
}

func (b *builder) analyse() error {
	b.flowRoleRefs = map[string]bool{}
	// Skills and roles are analysed first: flow task references are checked
	// against the declared sets while the flow documents are parsed.
	if err := b.analyseSkills(); err != nil {
		return err
	}
	if err := b.analyseRoles(); err != nil {
		return err
	}
	if err := b.analyseFlows(); err != nil {
		return err
	}
	if err := b.checkCrossReferences(); err != nil {
		return err
	}
	if err := b.checkSupportingFiles(); err != nil {
		return err
	}
	b.collectWarnings()
	return nil
}

func (b *builder) analyseFlows() error {
	for index := range b.manifest.Flows {
		entry := b.manifest.Flows[index]
		field := "flows[" + strconv.Itoa(index) + "]"
		file, path, err := b.requireFile(field+".path", entry.Path, MaxFlowSourceBytes)
		if err != nil {
			return err
		}
		summary := FlowSummary{
			ID:         entry.ID,
			Path:       path,
			Format:     entry.Format,
			Roles:      append([]string{}, entry.Roles...),
			PathDigest: fileDigest(file.Data),
		}
		for _, handle := range entry.Roles {
			b.flowRoleRefs[handle] = true
		}
		switch entry.Format {
		case FlowFormatGraph:
			if err := b.analyseGraphFlow(field, &summary, file); err != nil {
				return err
			}
		case FlowFormatFlow:
			if err := b.analyseAgentFlow(field, &summary, file); err != nil {
				return err
			}
		}
		b.flows = append(b.flows, summary)
		b.addDocument(DocumentFlow, entry.ID, path, file.Data)
	}
	return nil
}

func (b *builder) analyseGraphFlow(field string, summary *FlowSummary, file File) error {
	document, err := graphsource.Parse(file.Data)
	if err != nil {
		return fieldError(CodePackFlowInvalid, field+".path",
			"parse Graph source %s: %v", summary.Path, err)
	}
	digest, err := graphsource.SourceDigest(document)
	if err != nil {
		return fieldError(CodePackFlowInvalid, field+".path",
			"digest Graph source %s: %v", summary.Path, err)
	}
	summary.SourceDigest = digest
	summary.TaskCount = len(document.Tasks)
	summary.EntryTask = graphEntryTask(document)

	names := make([]string, 0, len(document.Tasks))
	for name := range document.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		task := document.Tasks[name]
		taskField := field + ".tasks." + name
		if err := b.checkScopedRoleRef(taskField+".role", task.Role); err != nil {
			return err
		}
		for _, skill := range task.Skills {
			if err := b.checkScopedSkillRef(taskField+".skills", skill); err != nil {
				return err
			}
		}
		b.writes = append(b.writes, task.Writes...)
	}
	return nil
}

func (b *builder) analyseAgentFlow(field string, summary *FlowSummary, file File) error {
	definition, err := agentflowParse(file.Data)
	if err != nil {
		return fieldError(CodePackFlowInvalid, field+".path",
			"parse Agent Flow source %s: %v", summary.Path, err)
	}
	digest, err := agentflowDigest(definition)
	if err != nil {
		return fieldError(CodePackFlowInvalid, field+".path",
			"digest Agent Flow source %s: %v", summary.Path, err)
	}
	summary.SourceDigest = digest
	summary.TaskCount = len(definition.Tasks)
	summary.MaxTotalTokens = definition.Budget.MaxTotalTokens
	summary.EntryTask = definitionEntryTask(definition)

	names := make([]string, 0, len(definition.Tasks))
	for name := range definition.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		task := definition.Tasks[name]
		taskField := field + ".tasks." + name
		switch task.Type {
		case flowTaskCheck:
			summary.CheckTasks++
			if command := strings.TrimSpace(task.Command); command != "" {
				b.checkCommands = append(b.checkCommands, command)
			}
		case flowTaskRole:
			if task.FanOut != nil {
				summary.FanOutTasks++
			}
			if err := b.checkScopedRoleRef(taskField+".role", task.Role); err != nil {
				return err
			}
		}
		for _, next := range task.Next {
			if _, ok := definition.Tasks[next]; !ok {
				return fieldError(CodePackFlowInvalid, taskField+".next",
					"task %q routes to unknown task %q", name, next)
			}
		}
		for _, skill := range task.Skills {
			if err := b.checkScopedSkillRef(taskField+".skills", skill); err != nil {
				return err
			}
		}
		b.writes = append(b.writes, task.Writes...)
	}
	return nil
}

func (b *builder) analyseRoles() error {
	for index := range b.manifest.Roles {
		entry := b.manifest.Roles[index]
		field := "roles[" + strconv.Itoa(index) + "]"
		file, path, err := b.requireFile(field+".path", entry.Path, MaxRoleSourceBytes)
		if err != nil {
			return err
		}
		document, err := rolesource.Parse(file.Data)
		if err != nil {
			return fieldError(CodePackRoleInvalid, field+".path",
				"parse Role source %s: %v", path, err)
		}
		digest, err := rolesource.SourceDigest(document)
		if err != nil {
			return fieldError(CodePackRoleInvalid, field+".path",
				"digest Role source %s: %v", path, err)
		}
		authority := string(document.Authority)
		if authority == "" {
			authority = string(rolesource.AuthorityReadOnly)
		}
		skills := make([]string, 0, len(document.Skills))
		for _, skill := range document.Skills {
			skills = append(skills, skill.ID)
		}
		b.roles = append(b.roles, RoleSummary{
			Handle:            entry.Handle,
			Path:              path,
			Scope:             entry.Scope,
			Name:              document.Name,
			Authority:         authority,
			PermissionCeiling: string(document.PermissionCeiling),
			AllowedTools:      append([]string{}, document.AllowedTools...),
			Skills:            sortedUnique(skills),
			PathDigest:        fileDigest(file.Data),
			SourceDigest:      digest,
		})
		b.roleSet[entry.Handle] = true
		b.addDocument(DocumentRole, entry.Handle, path, file.Data)
	}
	return nil
}

func (b *builder) analyseSkills() error {
	for index := range b.manifest.Skills {
		entry := b.manifest.Skills[index]
		field := "skills[" + strconv.Itoa(index) + "]"
		if entry.Ref != "" {
			b.skills = append(b.skills, SkillSummary{Ref: entry.Ref, Kind: SkillKindExternal})
			b.warnings = append(b.warnings, Warning{
				Code: WarningExternalSkillRef, Severity: SeverityWarning,
				Message: "skill " + entry.Ref + " is an external reference; S1 never resolves or installs it",
			})
			continue
		}
		directory, err := normalizePackPath(entry.Path)
		if err != nil {
			return fieldError(CodePackSkillInvalid, field+".path", "%v", err)
		}
		if !b.pkg.HasDirectory(directory) {
			if _, isFile := b.pkg.Lookup(directory); isFile {
				return fieldError(CodePackPathKindUnsupported, field+".path",
					"vendored skill %s must be a directory", directory)
			}
			return fieldError(CodePackPathMissing, field+".path",
				"vendored skill directory %s is missing", directory)
		}
		files := b.pkg.FilesUnder(directory)
		summary := SkillSummary{
			ID:         entry.ID,
			Path:       directory,
			Kind:       SkillKindVendored,
			Scope:      entry.Scope,
			PathDigest: directoryDigest(directory, files),
		}
		prompt, promptBytes, err := validateVendoredSkill(field, directory, files)
		if err != nil {
			return err
		}
		summary.PromptBytes = promptBytes
		b.skills = append(b.skills, summary)
		b.skillIDs[entry.ID] = true
		if prompt != nil {
			b.addDocument(DocumentSkill, entry.ID, prompt.Path, prompt.Data)
		}
	}
	return nil
}

// validateVendoredSkill checks presence and basic readability of a vendored
// skill directory. S1 deliberately does not use skills.Load: that loader wants a
// real directory and the archive is never extracted.
func validateVendoredSkill(field, directory string, files []File) (*File, int64, error) {
	if len(files) == 0 {
		return nil, 0, fieldError(CodePackSkillInvalid, field+".path",
			"vendored skill %s contains no files", directory)
	}
	byPath := make(map[string]*File, len(files))
	for index := range files {
		byPath[files[index].Path] = &files[index]
	}
	manifestFile := byPath[directory+"/skill.json"]
	var prompt *File
	for _, name := range skillPromptNames {
		if candidate := byPath[directory+"/"+name]; candidate != nil {
			prompt = candidate
			break
		}
	}
	if manifestFile == nil && prompt == nil {
		return nil, 0, fieldError(CodePackSkillInvalid, field+".path",
			"vendored skill %s must contain skill.json or SKILL.md", directory)
	}
	if manifestFile != nil {
		var decoded map[string]any
		if err := json.Unmarshal(manifestFile.Data, &decoded); err != nil {
			return nil, 0, fieldError(CodePackSkillInvalid, field+".path",
				"vendored skill %s/skill.json is not valid JSON: %v", directory, err)
		}
	}
	if prompt != nil {
		if len(strings.TrimSpace(string(prompt.Data))) == 0 {
			return nil, 0, fieldError(CodePackSkillInvalid, field+".path",
				"vendored skill prompt %s is empty", prompt.Path)
		}
		return prompt, int64(len(prompt.Data)), nil
	}
	return nil, 0, nil
}

func (b *builder) checkCrossReferences() error {
	// Flows may only reference roles the pack declares.
	for index := range b.manifest.Flows {
		for _, handle := range b.manifest.Flows[index].Roles {
			if !b.roleSet[handle] {
				return fieldError(CodePackRefUnknown, "flows["+strconv.Itoa(index)+"].roles",
					"flow %q references undeclared role %q", b.manifest.Flows[index].ID, handle)
			}
		}
	}
	// A Role-only pack installs global Roles only: a flow-scoped Role would be
	// invisible to every flow.
	if len(b.manifest.Flows) == 0 {
		for index := range b.manifest.Roles {
			if b.manifest.Roles[index].Scope != RoleScopeGlobal {
				return fieldError(CodePackRoleInvalid, "roles["+strconv.Itoa(index)+"].scope",
					"Role-only pack requires scope: global for role %q", b.manifest.Roles[index].Handle)
			}
		}
	}
	// Every flow-scoped Role must be reachable from a flow.
	for index := range b.manifest.Roles {
		role := b.manifest.Roles[index]
		if role.Scope == RoleScopeFlow && !b.flowRoleRefs[role.Handle] {
			return fieldError(CodePackRoleUnreferenced, "roles["+strconv.Itoa(index)+"]",
				"flow-scoped role %q is not referenced by any flow", role.Handle)
		}
	}
	// Model requirement overrides must name a declared role.
	if b.manifest.ModelRequirements != nil {
		handles := make([]string, 0, len(b.manifest.ModelRequirements.Roles))
		for handle := range b.manifest.ModelRequirements.Roles {
			handles = append(handles, handle)
		}
		sort.Strings(handles)
		for _, handle := range handles {
			if !b.roleSet[handle] {
				return fieldError(CodePackRefUnknown, "model_requirements.roles."+handle,
					"model requirements reference undeclared role %q", handle)
			}
			b.perRole[handle] = sortedUnique(b.manifest.ModelRequirements.Roles[handle].Capabilities)
		}
	}
	return nil
}

func (b *builder) checkSupportingFiles() error {
	if b.manifest.Signature != nil {
		path, err := normalizePackPath(b.manifest.Signature.Path)
		if err != nil {
			return fieldError(CodePackSignatureUnsupported, "signature.path", "%v", err)
		}
		if _, ok := b.pkg.Lookup(path); !ok {
			return fieldError(CodePackPathMissing, "signature.path",
				"declared signature file %s is missing", path)
		}
	}
	if b.manifest.Docs != nil {
		if _, _, err := b.requireFile("docs.readme", b.manifest.Docs.Readme, MaxFileBytes); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) collectWarnings() {
	signature := b.signature()
	switch {
	case !signature.Present:
		b.warn(WarningUnsignedPack, SeverityWarning,
			"pack is unsigned; S1 inspect reports the manifest only and does not verify signatures")
	default:
		b.warn(WarningSignatureUnverified, SeverityWarning,
			"pack declares an ed25519 signature; S1 inspect never verifies signatures")
	}
	switch {
	case len(b.manifest.Flows) == 0:
		b.warn(WarningRoleOnlyPack, SeverityInfo,
			"pack ships roles only; every role is installed as a Worker-global Role")
	case len(b.manifest.Roles) == 0:
		b.warn(WarningFlowOnlyPack, SeverityInfo,
			"pack ships flows only; every task role must resolve from the Worker's global Roles")
	}
	// MCP refs are external by definition in S1: report each one as information,
	// never as an error, and never resolve or execute it. manifest.MCP is already
	// sorted and de-duplicated, so the warning order is stable.
	for _, entry := range b.manifest.MCP {
		b.warn(WarningExternalMCPRef, SeverityInfo,
			"mcp "+entry.Ref+" is an external MCP profile reference; S1 never resolves or executes it")
	}
	sort.SliceStable(b.warnings, func(i, j int) bool {
		if b.warnings[i].Code == b.warnings[j].Code {
			return b.warnings[i].Message < b.warnings[j].Message
		}
		return b.warnings[i].Code < b.warnings[j].Code
	})
}

func (b *builder) warn(code, severity, message string) {
	b.warnings = append(b.warnings, Warning{Code: code, Severity: severity, Message: message})
}

func (b *builder) signature() Signature {
	signature := Signature{}
	if b.manifest.Signature == nil {
		return signature
	}
	algorithm, path := b.manifest.Signature.Algorithm, b.manifest.Signature.Path
	signature.Present = true
	signature.Algorithm = &algorithm
	signature.Path = &path
	return signature
}

func (b *builder) requireFile(field, rawPath string, limit int64) (File, string, error) {
	path, err := normalizePackPath(rawPath)
	if err != nil {
		return File{}, "", fieldError(CodePackPathInvalid, field, "%v", err)
	}
	file, ok := b.pkg.Lookup(path)
	if !ok {
		if b.pkg.HasDirectory(path) {
			return File{}, "", fieldError(CodePackPathKindUnsupported, field, "%s must be a file", path)
		}
		return File{}, "", fieldError(CodePackPathMissing, field, "%s is missing from the pack", path)
	}
	if int64(len(file.Data)) > limit {
		return File{}, "", fieldError(CodePackTooLarge, field,
			"%s is %d bytes (limit %d)", path, len(file.Data), limit)
	}
	return file, path, nil
}

func (b *builder) result(source Source, includeDocuments bool) *Result {
	manifestDigest, _ := ManifestDigest(b.pkg)
	files := append([]File(nil), b.pkg.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	summaries := make([]FileSummary, 0, len(files))
	for _, file := range files {
		summaries = append(summaries, FileSummary{
			Path: file.Path, Bytes: int64(len(file.Data)), Digest: fileDigest(file.Data),
		})
	}
	capability := b.capabilityEnvelope()

	result := &Result{
		InspectVersion: InspectVersion,
		Source:         source,
		Container:      b.pkg.Container,
		SchemaVersion:  b.manifest.SchemaVersion,
		Pack: PackInfo{
			ID: b.manifest.ID, Name: b.manifest.Name, Version: b.manifest.Version,
			License: b.manifest.License, RequiresEnnote: b.manifest.RequiresEnnote,
			Description: b.manifest.Description,
		},
		ManifestDigest:     manifestDigest,
		PackDigest:         PackDigest(b.pkg),
		Signature:          b.signature(),
		Signed:             false,
		Flows:              nonNilFlows(b.flows),
		Roles:              nonNilRoles(b.roles),
		Skills:             nonNilSkills(b.skills),
		MCP:                nonNilMCP(b.manifest.MCP),
		Requires:           append([]string{}, b.manifest.Requires...),
		CapabilityEnvelope: capability,
		Files:              summaries,
		FilesTotal:         len(summaries),
		FilesTruncated:     false,
		Warnings:           nonNilWarnings(b.warnings),
		Manifest:           string(dataOf(b.pkg, ManifestName)),
	}
	if includeDocuments {
		documents := append([]DocumentText{}, b.documents...)
		// Documents are reported in review order (flows, roles, skills) rather
		// than analysis order, then by ref for determinism.
		sort.SliceStable(documents, func(i, j int) bool {
			if documentRank(documents[i].Kind) == documentRank(documents[j].Kind) {
				return documents[i].Ref < documents[j].Ref
			}
			return documentRank(documents[i].Kind) < documentRank(documents[j].Kind)
		})
		result.Documents = documents
		result.DocumentsTruncated = b.documentsTruncated
	}
	return result
}

func (b *builder) capabilityEnvelope() CapabilityEnvelope {
	roles := make([]string, 0, len(b.roles))
	tools := make([]string, 0)
	for _, role := range b.roles {
		roles = append(roles, role.Handle)
		tools = append(tools, role.AllowedTools...)
	}
	if b.perRole == nil {
		b.perRole = map[string][]string{}
	}
	capabilities := []string{}
	var minContextWindow int64
	if b.manifest.ModelRequirements != nil {
		capabilities = sortedUnique(b.manifest.ModelRequirements.Defaults.Capabilities)
		minContextWindow = b.manifest.ModelRequirements.Defaults.MinContextWindow
	}
	skillRefs := make([]string, 0, len(b.skills))
	for _, skill := range b.skills {
		if skill.Kind == SkillKindExternal {
			skillRefs = append(skillRefs, skill.Ref)
			continue
		}
		skillRefs = append(skillRefs, skill.ID)
	}
	taskCount := 0
	var flowTokens int64
	for _, flow := range b.flows {
		taskCount += flow.TaskCount
		flowTokens += flow.MaxTotalTokens
	}
	requiresMCP := make([]string, 0, len(b.manifest.MCP))
	for _, entry := range b.manifest.MCP {
		requiresMCP = append(requiresMCP, entry.Ref)
	}
	return CapabilityEnvelope{
		FlowCount:   len(b.flows),
		RoleCount:   len(b.roles),
		SkillCount:  len(b.skills),
		TaskCount:   taskCount,
		Roles:       sortedUnique(roles),
		Skills:      sortedUnique(skillRefs),
		RequiresMCP: sortedUnique(requiresMCP),
		ModelRequirements: ModelEnvelope{
			Capabilities: capabilities, MinContextWindow: minContextWindow, PerRole: b.perRole,
		},
		CheckCommands:      sortedUnique(b.checkCommands),
		Tools:              sortedUnique(tools),
		Writes:             sortedUnique(b.writes),
		FlowMaxTotalTokens: flowTokens,
	}
}

func (b *builder) addDocument(kind, ref, path string, data []byte) {
	remaining := int64(MaxDocumentsBytes) - b.documentsBytes
	if remaining <= 0 {
		b.documentsTruncated = true
		return
	}
	limit := int64(MaxDocumentBytes)
	if remaining < limit {
		limit = remaining
	}
	content := data
	truncated := false
	if int64(len(content)) > limit {
		content = content[:limit]
		truncated = true
		b.documentsTruncated = true
	}
	b.documentsBytes += int64(len(content))
	b.documents = append(b.documents, DocumentText{
		Kind: kind, Ref: ref, Path: path, Bytes: int64(len(data)),
		Content: string(content), Truncated: truncated,
	})
}

func documentRank(kind string) int {
	switch kind {
	case DocumentFlow:
		return 0
	case DocumentRole:
		return 1
	default:
		return 2
	}
}

func dataOf(pkg *Package, path string) []byte {
	if file, ok := pkg.Lookup(path); ok {
		return file.Data
	}
	return nil
}

func normalizeDigest(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}
	if len(normalized) == 64 && isHex(normalized) {
		return "sha256:" + normalized
	}
	return normalized
}

func isHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func nonNilFlows(values []FlowSummary) []FlowSummary {
	if values == nil {
		return []FlowSummary{}
	}
	return values
}

func nonNilRoles(values []RoleSummary) []RoleSummary {
	if values == nil {
		return []RoleSummary{}
	}
	return values
}

func nonNilSkills(values []SkillSummary) []SkillSummary {
	if values == nil {
		return []SkillSummary{}
	}
	return values
}

func nonNilMCP(values []MCPEntry) []MCPSummary {
	summaries := make([]MCPSummary, 0, len(values))
	for _, entry := range values {
		summaries = append(summaries, MCPSummary{Ref: entry.Ref})
	}
	return summaries
}

func nonNilWarnings(values []Warning) []Warning {
	if values == nil {
		return []Warning{}
	}
	return values
}
