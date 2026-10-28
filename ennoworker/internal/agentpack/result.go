package agentpack

// InspectVersion is the inspect result schema version. Clients must reject an
// unknown version instead of guessing at the payload.
const InspectVersion = 1

// Source kinds. S1 only inspects a host path; URL/git sources arrive in S6.
const SourceKindPath = "path"

// Warning severities.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
)

// Warning codes emitted by inspect.
const (
	WarningUnsignedPack        = "unsigned_pack"
	WarningSignatureUnverified = "signature_unverified"
	WarningRoleOnlyPack        = "role_only_pack"
	WarningFlowOnlyPack        = "flow_only_pack"
	WarningFlowRefsGlobalRole  = "flow_refs_global_role"
	WarningFlowRefsGlobalSkill = "flow_refs_global_skill"
	WarningExternalSkillRef    = "external_skill_ref"
	WarningExternalMCPRef      = "external_mcp_ref"
	WarningFilesTruncated      = "files_truncated"
	WarningDocumentsTruncated  = "documents_truncated"
)

// Document kinds echoed back by includeDocuments.
const (
	DocumentFlow  = "flow"
	DocumentRole  = "role"
	DocumentSkill = "skill"
)

// Skill kinds.
const (
	SkillKindVendored = "vendored"
	SkillKindExternal = "external"
)

// Source is the requested pack location.
type Source struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// Request is one read-only inspect request.
type Request struct {
	Source           Source
	IncludeDocuments bool
	ExpectedDigest   string
}

// Result is the read-only inspect verdict. It never implies that the pack was
// installed, executed, or trusted.
type Result struct {
	InspectVersion     int                `json:"inspectVersion"`
	Source             Source             `json:"source"`
	Container          Container          `json:"container"`
	SchemaVersion      int                `json:"schemaVersion"`
	Pack               PackInfo           `json:"pack"`
	ManifestDigest     string             `json:"manifestDigest"`
	PackDigest         string             `json:"packDigest"`
	Signature          Signature          `json:"signature"`
	Signed             bool               `json:"signed"`
	Flows              []FlowSummary      `json:"flows"`
	Roles              []RoleSummary      `json:"roles"`
	Skills             []SkillSummary     `json:"skills"`
	MCP                []MCPSummary       `json:"mcp"`
	Requires           []string           `json:"requires"`
	CapabilityEnvelope CapabilityEnvelope `json:"capabilityEnvelope"`
	Files              []FileSummary      `json:"files"`
	FilesTotal         int                `json:"filesTotal"`
	FilesTruncated     bool               `json:"filesTruncated"`
	Warnings           []Warning          `json:"warnings"`
	// Manifest is always returned (bounded by the 256 KiB manifest limit) so a
	// reviewer sees the exact bytes the digests commit to.
	Manifest string `json:"manifest"`
	// Documents are only populated when includeDocuments is requested.
	Documents          []DocumentText `json:"documents,omitempty"`
	DocumentsTruncated bool           `json:"documentsTruncated,omitempty"`
}

// PackInfo is the manifest identity echoed back for the review surface.
type PackInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Version        string `json:"version"`
	License        string `json:"license,omitempty"`
	RequiresEnnote string `json:"requiresEnnote,omitempty"`
	Description    string `json:"description,omitempty"`
}

// Signature reports the declared signature only. S1 never verifies one, so
// Verified is always false and Result.Signed is always false.
type Signature struct {
	Present   bool    `json:"present"`
	Algorithm *string `json:"algorithm"`
	Path      *string `json:"path"`
	Verified  bool    `json:"verified"`
}

// FlowSummary is the structural summary of one flow source document.
type FlowSummary struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// Format is always reported: format: flow is a full Agent Flow definition
	// (check tasks, fan-out, terminal, next routing) while format: graph is the
	// legacy graphsource.Document that can only express role tasks. S1 never
	// downgrades one into the other.
	Format         string   `json:"format"`
	TaskCount      int      `json:"taskCount"`
	EntryTask      string   `json:"entryTask,omitempty"`
	Roles          []string `json:"roles"`
	CheckTasks     int      `json:"checkTasks"`
	FanOutTasks    int      `json:"fanOutTasks"`
	MaxTotalTokens int64    `json:"maxTotalTokens"`
	PathDigest     string   `json:"pathDigest"`
	SourceDigest   string   `json:"sourceDigest"`
}

// RoleSummary is the structural summary of one pack role.
type RoleSummary struct {
	Handle            string   `json:"handle"`
	Path              string   `json:"path"`
	Scope             string   `json:"scope"`
	Name              string   `json:"name"`
	Authority         string   `json:"authority"`
	PermissionCeiling string   `json:"permissionCeiling,omitempty"`
	AllowedTools      []string `json:"allowedTools"`
	Skills            []string `json:"skills"`
	PathDigest        string   `json:"pathDigest"`
	SourceDigest      string   `json:"sourceDigest"`
}

// SkillSummary is one declared skill: a vendored directory or an external ref.
type SkillSummary struct {
	ID          string `json:"id,omitempty"`
	Path        string `json:"path,omitempty"`
	Ref         string `json:"ref,omitempty"`
	Kind        string `json:"kind"`
	PathDigest  string `json:"pathDigest,omitempty"`
	PromptBytes int64  `json:"promptBytes,omitempty"`
}

// MCPSummary is one declared external MCP profile reference. S1 never resolves
// or executes it; the ref is reported so the reviewer sees the dependency.
type MCPSummary struct {
	Ref string `json:"ref"`
}

// CapabilityEnvelope is the S1 review surface: what the pack can reach, spend,
// and write, derived structurally from the manifest and flow sources.
type CapabilityEnvelope struct {
	FlowCount          int           `json:"flowCount"`
	RoleCount          int           `json:"roleCount"`
	SkillCount         int           `json:"skillCount"`
	TaskCount          int           `json:"taskCount"`
	Roles              []string      `json:"roles"`
	Skills             []string      `json:"skills"`
	RequiresMCP        []string      `json:"requiresMcp"`
	ModelRequirements  ModelEnvelope `json:"modelRequirements"`
	CheckCommands      []string      `json:"checkCommands"`
	Tools              []string      `json:"tools"`
	Writes             []string      `json:"writes"`
	FlowMaxTotalTokens int64         `json:"flowMaxTotalTokens"`
}

// ModelEnvelope is the model capability request in camelCase wire form.
type ModelEnvelope struct {
	Capabilities     []string            `json:"capabilities"`
	MinContextWindow int64               `json:"minContextWindow"`
	PerRole          map[string][]string `json:"perRole"`
}

// FileSummary is one payload file identity.
type FileSummary struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Digest string `json:"digest"`
}

// Warning is one non-fatal inspect finding.
type Warning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// DocumentText is one source document echoed back for review.
type DocumentText struct {
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}
