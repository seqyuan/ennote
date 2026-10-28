// Package agentpack implements the read-only Agent Pack (.enpack) inspect
// pipeline: strict manifest parsing, directory and archive reading without
// extraction, path safety, payload digests, and structural cross-reference
// validation.
//
// The package is deliberately pure. It never opens a store or database, never
// consults the model catalog, never touches the network or a subprocess, never
// writes to disk (no temp directories), and never verifies signatures. It also
// does not run agentflow.Validator, which needs a database-backed resolver;
// S1 only performs structural parsing and cross-reference checks.
package agentpack

import (
	"errors"
	"fmt"
)

// Error codes reported by inspect. They are stable wire codes: the API maps
// each one onto an HTTP status and the CLI prints them verbatim.
const (
	CodePackNotFound             = "pack_not_found"
	CodePackPathForbidden        = "pack_path_forbidden"
	CodePackFormatUnsupported    = "pack_format_unsupported"
	CodePackUnsupportedContainer = "pack_unsupported_container"
	CodePackTooLarge             = "pack_too_large"
	CodePackTooManyFiles         = "pack_too_many_files"
	CodePackManifestMissing      = "pack_manifest_missing"
	CodePackManifestInvalid      = "pack_manifest_invalid"
	CodePackManifestUnknownField = "pack_manifest_unknown_field"
	CodePackSchemaUnsupported    = "pack_schema_unsupported"
	CodePackIdentityInvalid      = "pack_identity_invalid"
	CodePackDuplicateID          = "pack_duplicate_id"
	CodePackPathInvalid          = "pack_path_invalid"
	CodePackPathMissing          = "pack_path_missing"
	CodePackPathKindUnsupported  = "pack_path_kind_unsupported"
	CodePackSymlinkRejected      = "pack_symlink_rejected"
	CodePackEmpty                = "pack_empty"
	CodePackFlowInvalid          = "pack_flow_invalid"
	CodePackRoleInvalid          = "pack_role_invalid"
	CodePackSkillInvalid         = "pack_skill_invalid"
	CodePackRefUnknown           = "pack_ref_unknown"
	CodePackRoleUnreferenced     = "pack_role_unreferenced"
	CodePackCapabilityUnknown    = "pack_capability_unknown"
	CodePackDigestMismatch       = "pack_digest_mismatch"
	CodePackSignatureUnsupported = "pack_signature_unsupported"
)

// Error is a typed inspect failure. Every inspect failure is non-retryable:
// the pack bytes or the manifest must change before a retry can succeed.
//
// Field and Path are optional context that identifies the offending manifest
// field (e.g. "flows[0].path") or pack path (e.g. "flows/a/flow.yaml"). The API
// folds them into the error message because the Worker error envelope keeps its
// existing {code,message,requestId,retryable} shape.
type Error struct {
	Code    string
	Message string
	Field   string
	Path    string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	context := e.Field
	if context == "" {
		context = e.Path
	}
	if context == "" {
		return e.Message
	}
	return context + ": " + e.Message
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

func fieldError(code, field, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Field: field}
}

func pathError(code, path, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Path: path}
}
