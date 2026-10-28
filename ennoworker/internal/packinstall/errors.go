// Package packinstall installs Agent Packs into the file-native Worker stores.
//
// S2a installs two pack shapes and nothing else:
//
//   - a Role-only pack: every Role is installed once as a Worker-global Role and
//     published as an immutable Role revision; and
//   - a pack Flow with format: graph: the flow becomes an ordinary file-native
//     Graph draft (agents/graphs/<id>/graph.yaml) plus its Graph-private Roles
//     and Skills.
//
// A full Agent Flow definition (format: flow) is refused: the Worker has no
// file-native FlowDefinition source, no CheckTaskRunner assembly, and no check
// approval API yet (S2b). The installer never calls PublishGraphRevision, so an
// installed Graph is inert until a user publishes a revision through the
// existing Graph path; that is the whole disabled-by-default mechanism.
//
// The commit point is the state file under packs/state/<publisher>/<name>.json,
// written last with an atomic rename. A crash before it leaves the store objects
// invisible to List/Remove; a crash after it leaves an installed pack. Nothing is
// ever extracted or staged outside packs/staging/<txid>/.
package packinstall

import (
	"errors"
	"fmt"
)

// Stable install error codes. They are wire codes: the API maps each one onto an
// HTTP status and the CLI prints it verbatim. Every one is non-retryable.
const (
	// CodeFlowFormatUnsupported is retained for wire compatibility with S2a.
	// S2b installs format: flow packs, so it is never emitted.
	CodeFlowFormatUnsupported = "pack_flow_format_unsupported"
	// CodeFlowValidationFailed reports a full-flow definition the real
	// agentflow.Validator rejected before any write.
	CodeFlowValidationFailed = "pack_flow_validation_failed"
	// CodeMCPUnsupported reports a non-empty mcp list.
	CodeMCPUnsupported = "pack_mcp_unsupported"
	// CodeDependencyUnsupported reports a non-empty requires list.
	CodeDependencyUnsupported = "pack_dependency_unsupported"
	// CodeAlreadyInstalled reports the same pack id installed with other bytes.
	CodeAlreadyInstalled = "pack_already_installed"
	// CodeVersionConflict reports the same pack id installed at another version.
	CodeVersionConflict = "pack_version_conflict"
	// CodeFlowIDConflict reports an installed Graph id collision or a bad rename.
	CodeFlowIDConflict = "pack_flow_id_conflict"
	// CodeRoleHandleConflict reports a Role handle collision or a bad rename.
	CodeRoleHandleConflict = "pack_role_handle_conflict"
	// CodeSkillConflict reports a Skill id collision.
	CodeSkillConflict = "pack_skill_conflict"
	// CodeModelUnresolved reports a Role/Task model ref the catalog cannot resolve.
	CodeModelUnresolved = "pack_model_unresolved"
	// CodeSkillUnresolved reports an external or Graph-local Skill the Worker
	// cannot resolve.
	CodeSkillUnresolved = "pack_skill_unresolved"
	// CodeSkillNotPromptOnly reports a vendored skill with payload beyond
	// skill.json and its prompt.
	CodeSkillNotPromptOnly = "pack_skill_not_prompt_only"
	// CodeSkillInvalid reports a vendored skill that cannot become a Skill.
	CodeSkillInvalid = "pack_skill_invalid"
	// CodeInstallBusy reports a concurrent install.
	CodeInstallBusy = "pack_install_busy"
	// CodeInstallBlocked reports blockers, conflicts, or an unsafe removal.
	CodeInstallBlocked = "pack_install_blocked"
	// CodeNotInstalled reports an unknown pack id on remove.
	CodeNotInstalled = "pack_not_installed"
)

// Error is a typed install failure.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Code returns the stable error code of err, or the empty string.
func Code(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

func newError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
