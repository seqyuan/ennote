package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/prompts"
)

// ——— project-scoped endpoints ———

// listPromptTemplates returns the effective template catalog for a project.
func (s *Server) listPromptTemplates(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	reg, err := s.Prompts.ProjectList(r.Context(), projectID)
	if err != nil {
		promptAPIError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"templates":   reg.List(),
		"diagnostics": sanitizeDiagnosticsForAPI(reg.Diagnostics()),
	})
}

// expandPromptTemplate expands a slash invocation against the project's catalog.
func (s *Server) expandPromptTemplate(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")

	var input struct {
		Invocation string `json:"invocation"`
	}
	if !decodeJSONWithLimit(w, r, &input, 20*1024) {
		return
	}

	result, err := s.Prompts.ProjectExpand(r.Context(), projectID, input.Invocation)
	if err != nil {
		promptAPIError(w, r, err)
		return
	}

	if err := result.Validate(); err != nil {
		writeInternal(w, r, fmt.Errorf("expand result validation: %w", err))
		return
	}

	switch result.Case {
	case prompts.ExpandCaseMatched:
		writeData(w, http.StatusOK, map[string]any{
			"case":        "matched",
			"name":        result.Name,
			"text":        result.Text,
			"diagnostics": sanitizeDiagnosticsForAPI(result.Diagnostics),
		})
	case prompts.ExpandCaseNotFound:
		writeData(w, http.StatusOK, map[string]any{
			"case":        "not_found",
			"name":        result.Name,
			"diagnostics": sanitizeDiagnosticsForAPI(result.Diagnostics),
		})
	case prompts.ExpandCaseInvalidInvocation:
		writeData(w, http.StatusOK, map[string]any{
			"case":        "invalid_invocation",
			"diagnostics": []prompts.Diagnostic{},
		})
	default:
		writeInternal(w, r, fmt.Errorf("unknown expand case %q", result.Case))
	}
}

// ——— global management endpoints ———

// listGlobalPromptTemplates returns the management catalog (builtin + settings + global).
func (s *Server) listGlobalPromptTemplates(w http.ResponseWriter, r *http.Request) {
	reg, globalEntries, recoveryMode, diags, err := s.Prompts.ManagementList()
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"templates":          reg.List(),
		"globalTemplates":    globalEntries,
		"globalRecoveryMode": recoveryMode,
		"diagnostics":        sanitizeDiagnosticsForAPI(diags),
	})
}

// createPromptTemplate creates a new global template.
func (s *Server) createPromptTemplate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		ArgumentHint string `json:"argumentHint"`
		Body         string `json:"body"`
	}
	if !decodeJSONWithLimit(w, r, &input, 80*1024) {
		return
	}

	if input.Name == "" || input.Body == "" {
		writeError(w, r, http.StatusBadRequest, "validation_failed",
			"name and body are required", false)
		return
	}

	if err := s.Prompts.GlobalStore.Create(input.Name, input.Description, input.ArgumentHint, input.Body); err != nil {
		promptAPIError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// getPromptTemplate returns a single global template by name.
func (s *Server) getPromptTemplate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tmpl, err := s.Prompts.GlobalStore.Get(name)
	if err != nil {
		promptAPIError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"name":         tmpl.Name,
		"description":  tmpl.Description,
		"argumentHint": tmpl.ArgumentHint,
		"body":         tmpl.Body,
		"source":       tmpl.Source,
	})
}

// updatePromptTemplate replaces an existing global template atomically.
func (s *Server) updatePromptTemplate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var input struct {
		Description  string `json:"description"`
		ArgumentHint string `json:"argumentHint"`
		Body         string `json:"body"`
	}
	if !decodeJSONWithLimit(w, r, &input, 80*1024) {
		return
	}

	if input.Body == "" {
		writeError(w, r, http.StatusBadRequest, "validation_failed",
			"body is required", false)
		return
	}

	if err := s.Prompts.GlobalStore.Update(name, input.Description, input.ArgumentHint, input.Body); err != nil {
		promptAPIError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"updated": name})
}

// deletePromptTemplate deletes a global template.
func (s *Server) deletePromptTemplate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Prompts.GlobalStore.Delete(name); err != nil {
		promptAPIError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ——— helpers ———

// decodeJSONWithLimit is like decodeJSON but with a custom size limit.
func decodeJSONWithLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large",
				"request body exceeds size limit", false)
			return false
		}
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error(), false)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "invalid_json",
			"request body must contain one JSON value", false)
		return false
	}
	return true
}

// promptAPIError maps prompts package errors to HTTP error codes.
func promptAPIError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, prompts.ErrInvocationTooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large",
			"invocation exceeds 16 KiB limit", false)
	case errors.Is(err, prompts.ErrPromptTemplateTooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, "prompt_template_too_large",
			"serialized template exceeds 64 KiB", false)
	case errors.Is(err, prompts.ErrPromptTemplateExists):
		writeError(w, r, http.StatusConflict, "prompt_template_exists", err.Error(), false)
	case errors.Is(err, prompts.ErrPromptTemplateLimit):
		writeError(w, r, http.StatusConflict, "prompt_template_limit", err.Error(), false)
	case errors.Is(err, prompts.ErrPromptTemplateInvalid):
		writeError(w, r, http.StatusConflict, "prompt_template_invalid", err.Error(), false)
	case errors.Is(err, prompts.ErrProjectNotFound):
		writeError(w, r, http.StatusNotFound, "project_not_found", err.Error(), false)
	case errors.Is(err, prompts.ErrPromptTemplateNotFound):
		writeError(w, r, http.StatusNotFound, "prompt_template_not_found", err.Error(), false)
	case errors.Is(err, prompts.ErrRecoveryMode):
		writeError(w, r, http.StatusConflict, "prompt_template_limit", err.Error(), false)
	case errors.Is(err, prompts.ErrExpandedPromptTooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, "expanded_prompt_too_large",
			"expanded prompt exceeds 256 KiB", false)
	case errors.Is(err, prompts.ErrExpandedPromptEmpty):
		writeError(w, r, http.StatusUnprocessableEntity, "expanded_prompt_empty",
			"expanded prompt is empty", false)
	case errors.Is(err, prompts.ErrPromptConfigInvalid):
		writeError(w, r, http.StatusInternalServerError, "prompt_config_invalid", err.Error(), false)
	case errors.Is(err, prompts.ErrWorkspaceTrustUnavailable):
		writeError(w, r, http.StatusInternalServerError, "workspace_trust_unavailable", err.Error(), false)
	case errors.Is(err, prompts.ErrPromptResourceLimit):
		writeError(w, r, http.StatusInternalServerError, "prompt_resource_limit", err.Error(), false)
	case errors.Is(err, prompts.ErrPromptStorageUnavailable):
		writeError(w, r, http.StatusInternalServerError, "prompt_storage_unavailable", err.Error(), false)
	case errors.Is(err, prompts.ErrTemplateNameInvalid):
		writeError(w, r, http.StatusBadRequest, "validation_failed", err.Error(), false)
	default:
		writeInternal(w, r, err)
	}
}

// sanitizeDiagnosticsForAPI strips the internal Path field and any absolute
// path-looking segments from diagnostic messages before they reach the UI.
func sanitizeDiagnosticsForAPI(diags []prompts.Diagnostic) []prompts.Diagnostic {
	out := make([]prompts.Diagnostic, 0, len(diags))
	for _, d := range diags {
		d.Path = ""
		d.Message = stripAbsolutePathSegments(d.Message)
		out = append(out, d)
	}
	return out
}

// stripAbsolutePathSegments rewrites absolute Unix path segments to their
// basename so host paths never appear in user-facing text.
func stripAbsolutePathSegments(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '/' && i+1 < len(s) {
			// Consume a full path segment run: '/foo/bar/baz.md' → 'baz.md'.
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '\t' && s[j] != '\n' && s[j] != '\r' {
				j++
			}
			segment := s[i:j]
			if base := path.Base(segment); base != "/" && base != "." {
				b.WriteString(base)
			} else {
				b.WriteString(segment)
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
