package heroesource

import (
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ValidateForWrite enforces the PortableHeroDocument write-time constraints
// declared by contracts/openapi.yaml, except the authority spelling: the parsers
// deliberately keep accepting the legacy "mutation" alias and an omitted value
// (see normalizeAndValidate), and tightening that here would reject Hero files
// that already exist. It is deliberately separate from normalizeAndValidate:
// normalizeAndValidate runs on the READ path (Parse, SourceDigest, Encode, and
// every revision reader depend on it), so it must stay lenient enough to keep
// loading pre-existing Hero files and already published revisions. Rejecting
// these values there would make a legacy file unopenable and break the
// "published revisions stay runnable" guarantee.
//
// ValidateForWrite calls normalizeAndValidate first so a document that passes
// here encodes exactly as it would have before this check existed; it then
// enforces only the constraints a reader is allowed to skip. Every message
// names the field path so the UI can point at the offending field.
func ValidateForWrite(document *Document) error {
	if document == nil {
		return fmt.Errorf("Hero source document is required")
	}
	// Duplicate payload keys are decided on the RAW list: normalisation is what
	// collapses them, so a check that ran after it could never fire. "aa" and
	// " aa " are the same key once trimmed, which is the shape a caller gets
	// wrong by copy-pasting, so the duplicate test is trim-insensitive.
	if err := validateReportContractDuplicates(document.ReportContract); err != nil {
		return err
	}
	if err := normalizeAndValidate(document); err != nil {
		return err
	}

	switch document.PermissionCeiling {
	case domain.PermissionDiscuss, domain.PermissionAsk, domain.PermissionAuto:
	default:
		return fmt.Errorf("permissionCeiling must be one of discuss, ask, auto")
	}

	switch document.Delegation.Admission {
	case domain.DelegationAutoWithinBudget, domain.DelegationApprovalRequired, AdmissionDeny:
	default:
		return fmt.Errorf("delegation.admission must be one of auto_within_budget, approval_required, deny")
	}
	if document.Delegation.MaxInvocationsPerParentRun < 1 {
		return fmt.Errorf("delegation.maxInvocationsPerParentRun must be >= 1")
	}
	if document.Delegation.MaxConcurrentInstances < 1 {
		return fmt.Errorf("delegation.maxConcurrentInstances must be >= 1")
	}

	ceiling := document.Delegation.BudgetCeiling
	if ceiling.MaxModelCalls < 1 {
		return fmt.Errorf("delegation.budgetCeiling.maxModelCalls must be >= 1")
	}
	if ceiling.MaxToolCalls < 0 {
		return fmt.Errorf("delegation.budgetCeiling.maxToolCalls must be >= 0")
	}
	if ceiling.MaxTotalTokens < 1 {
		return fmt.Errorf("delegation.budgetCeiling.maxTotalTokens must be >= 1")
	}
	if ceiling.MaxOutputTokens < 1 {
		return fmt.Errorf("delegation.budgetCeiling.maxOutputTokens must be >= 1")
	}
	if ceiling.MaxCostUSDMicros < 0 {
		return fmt.Errorf("delegation.budgetCeiling.maxCostUsdMicros must be >= 0")
	}
	if ceiling.MaxWallTimeMS < 1 {
		return fmt.Errorf("delegation.budgetCeiling.maxWallTimeMs must be >= 1")
	}

	if document.MaxLoopIterations < 1 {
		return fmt.Errorf("maxLoopIterations must be >= 1")
	}

	if len(document.Context.AllowedModes) == 0 {
		return fmt.Errorf("context.allowedModes must not be empty")
	}
	if document.Context.DefaultMode == "" {
		return fmt.Errorf("context.defaultMode must not be empty")
	}
	if !containsContextMode(document.Context.AllowedModes, document.Context.DefaultMode) {
		return fmt.Errorf("context.defaultMode must be a member of context.allowedModes")
	}

	switch document.Model.ThinkingEffort {
	case domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium, domain.ThinkingHigh:
	default:
		return fmt.Errorf("model.thinkingEffort must be one of default, low, medium, high")
	}

	if document.Context.OwnExecutionContinuity != domain.HeroContinuityNone {
		return fmt.Errorf("context.ownExecutionContinuity must be \"none\"")
	}

	// acceptanceKinds names the acceptance leaves a Graph task may declare when
	// it runs this Hero. The vocabulary is agentflow.KnownChecks - the kernel's
	// own single source of truth - so a Hero cannot name a leaf the kernel will
	// never decide. heroesource may import agentflow because agentflow depends
	// only on domain: the edge stays acyclic. An empty list is deliberately
	// accepted: it means "the whole vocabulary".
	for index, kind := range document.AcceptanceKinds {
		if !knownAcceptanceKind(kind) {
			return fmt.Errorf("acceptanceKinds[%d] %q is not a known acceptance check (supported: %s)",
				index, kind, strings.Join(agentflow.KnownChecks, ", "))
		}
	}

	if strings.TrimSpace(document.Prompt) == "" {
		return fmt.Errorf("prompt must not be empty")
	}
	if len(document.Prompt) > 65536 {
		return fmt.Errorf("prompt must be at most 65536 bytes")
	}

	return validateReportContractForWrite(document.ReportContract)
}

// validateReportContractDuplicates rejects a payloadFields list that names the
// same key twice (after trimming) and an entry that is not a plain identifier.
// The identifier rule is checked here as well as in
// validateReportContractForWrite because a malformed key that normalisation would
// drop (an all-whitespace entry) must be reported, not silently discarded.
//
// The list is read as given, so this is reached by the API write boundary where
// the body is still un-normalised. A hand-authored Hero file has already been
// canonicalised by Parse (duplicates collapse, order-independently), which is why
// publishing one stays possible: the read path must keep loading every revision
// that exists.
func validateReportContractDuplicates(contract *domain.HeroReportContract) error {
	if contract == nil {
		return nil
	}
	seen := make(map[string]int, len(contract.PayloadFields))
	for index, field := range contract.PayloadFields {
		trimmed := strings.TrimSpace(field)
		if trimmed == "" {
			return fmt.Errorf("reportContract.payloadFields[%d] must not be empty", index)
		}
		if first, exists := seen[trimmed]; exists {
			return fmt.Errorf("reportContract.payloadFields[%d] duplicates payloadFields[%d] (%q)", index, first, trimmed)
		}
		seen[trimmed] = index
	}
	return nil
}

// validateReportContractForWrite enforces the write-time rules of a report
// contract. The read path deliberately skips them (see normalizeAndValidate), so
// a revision a future Worker published stays loadable; the enforcement path
// fails closed on an unsupported version instead of ignoring the contract.
func validateReportContractForWrite(contract *domain.HeroReportContract) error {
	if contract == nil {
		return nil
	}
	if contract.Version != domain.HeroReportContractV1 {
		return fmt.Errorf("reportContract.version must be %d (unsupported versions cannot be enforced)",
			domain.HeroReportContractV1)
	}
	if contract.SummaryMaxBytes < 1 || contract.SummaryMaxBytes > domain.HeroReportSummaryMaxBytesCeiling {
		return fmt.Errorf("reportContract.summaryMaxBytes must be 1..%d (state the cap explicitly: 0 would leave the promise unstated)",
			domain.HeroReportSummaryMaxBytesCeiling)
	}
	if len(contract.PayloadFields) > domain.HeroReportPayloadFieldLimit {
		return fmt.Errorf("reportContract.payloadFields must contain at most %d entries",
			domain.HeroReportPayloadFieldLimit)
	}
	for index, field := range contract.PayloadFields {
		if !domain.IsHeroReportPayloadField(field) {
			return fmt.Errorf("reportContract.payloadFields[%d] %q must match ^[A-Za-z_][A-Za-z0-9_]*$",
				index, field)
		}
	}
	return nil
}

func containsContextMode(modes []domain.HeroContextMode, target domain.HeroContextMode) bool {
	for _, mode := range modes {
		if mode == target {
			return true
		}
	}
	return false
}

// knownAcceptanceKind reports whether the value is one of the kernel's
// acceptance leaves.
func knownAcceptanceKind(kind string) bool {
	for _, known := range agentflow.KnownChecks {
		if kind == known {
			return true
		}
	}
	return false
}
