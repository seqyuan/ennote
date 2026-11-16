package domain_test

// HeroReportContract: the enforceable half of a Hero's output promise. Each test
// names the rule it pins, and the nil-contract case is the default path every
// Hero written before the field existed keeps taking.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func completedReport(summary string, payload string, artifacts int) *domain.SubmitResult {
	result := &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: summary}
	if payload != "" {
		result.Payload = json.RawMessage(payload)
	}
	for index := 0; index < artifacts; index++ {
		result.ArtifactRefs = append(result.ArtifactRefs, domain.ArtifactReference{
			ArtifactID: "artifact-" + string(rune('a'+index)), Name: "report.csv", Kind: "report",
			MIMEType: "text/csv", SHA256: "sha256:" + strings.Repeat("a", 64),
		})
	}
	return result
}

// A Hero without a report contract keeps exactly the pre-existing behaviour: the
// outputContract label equality rule is untouched, so nothing is validated here.
func TestValidateReportAgainstContractNilContractPassesEverything(t *testing.T) {
	require.NoError(t, domain.ValidateReportAgainstContract(nil, nil))
	require.NoError(t, domain.ValidateReportAgainstContract(completedReport("done", "", 0), nil))
	require.NoError(t, domain.ValidateReportAgainstContract(
		&domain.SubmitResult{Status: domain.SubmitCompleted}, nil),
		"a nil contract cannot invent rules the terminal contract does not have")
}

func TestValidateReportAgainstContractSummaryMaxBytes(t *testing.T) {
	contract := &domain.HeroReportContract{Version: domain.HeroReportContractV1, SummaryMaxBytes: 8}
	require.NoError(t, domain.ValidateReportAgainstContract(completedReport("12345678", "", 0), contract))
	err := domain.ValidateReportAgainstContract(completedReport("123456789", "", 0), contract)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "summary is 9 bytes")
	assert.Contains(t, err.Error(), "at most 8")
}

func TestValidateReportAgainstContractRequiredArtifact(t *testing.T) {
	contract := &domain.HeroReportContract{Version: domain.HeroReportContractV1, RequireArtifacts: true}
	err := domain.ValidateReportAgainstContract(completedReport("done", `{}`, 0), contract)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one artifact")
	require.NoError(t, domain.ValidateReportAgainstContract(completedReport("done", `{}`, 1), contract))
}

func TestValidateReportAgainstContractRequiredPayloadFields(t *testing.T) {
	contract := &domain.HeroReportContract{Version: domain.HeroReportContractV1,
		PayloadFields: []string{"changedFiles", "testsRun"}}

	require.NoError(t, domain.ValidateReportAgainstContract(
		completedReport("done", `{"changedFiles":["a.go"],"testsRun":true,"extra":"allowed"}`, 0), contract),
		"keys the contract does not name are allowed")

	err := domain.ValidateReportAgainstContract(completedReport("done", `{"changedFiles":["a.go"]}`, 0), contract)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing required field "testsRun"`)
	assert.Contains(t, err.Error(), "changedFiles, testsRun", "the message names every required key")

	// A payload field that is JSON null is still present: the contract names
	// keys, not values.
	require.NoError(t, domain.ValidateReportAgainstContract(
		completedReport("done", `{"changedFiles":null,"testsRun":null}`, 0), contract))

	// An absent payload cannot carry a required key.
	err = domain.ValidateReportAgainstContract(completedReport("done", "", 0), contract)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "payload to be a JSON object")
}

func TestValidateReportAgainstContractPayloadMustBeObject(t *testing.T) {
	contract := &domain.HeroReportContract{Version: domain.HeroReportContractV1,
		PayloadFields: []string{"changedFiles"}}
	for _, payload := range []string{`["changedFiles"]`, `"changedFiles"`, `42`, `null`} {
		err := domain.ValidateReportAgainstContract(completedReport("done", payload, 0), contract)
		require.Error(t, err, payload)
		assert.Contains(t, err.Error(), "JSON object", payload)
	}
	// A contract with no payload fields does not constrain the payload shape: the
	// terminal contract already decides it, and this rule exists to make the
	// required keys reachable.
	require.NoError(t, domain.ValidateReportAgainstContract(
		completedReport("done", `[]`, 0), &domain.HeroReportContract{Version: domain.HeroReportContractV1}))
}

// A version this Worker cannot enforce fails closed. Silently passing it would
// make a contract the runtime does not understand look enforced.
func TestValidateReportAgainstContractRejectsUnsupportedVersion(t *testing.T) {
	err := domain.ValidateReportAgainstContract(completedReport("done", `{}`, 0),
		&domain.HeroReportContract{Version: 2})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version 2 is not supported")
}

// A missing terminal report is a violation, not a pass: a contract that requires
// an artifact cannot be satisfied by "the report could not be read".
func TestValidateReportAgainstContractMissingReport(t *testing.T) {
	err := domain.ValidateReportAgainstContract(nil, &domain.HeroReportContract{Version: domain.HeroReportContractV1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a terminal report")
}

func TestIsHeroReportPayloadField(t *testing.T) {
	for _, valid := range []string{"a", "_a", "changedFiles", "A_b9", "_"} {
		assert.True(t, domain.IsHeroReportPayloadField(valid), valid)
	}
	for _, invalid := range []string{"", "9a", "a-b", "a.b", "a b", "a$", "ünicode"} {
		assert.False(t, domain.IsHeroReportPayloadField(invalid), invalid)
	}
}

func TestCloneHeroReportContractIsDeep(t *testing.T) {
	require.Nil(t, domain.CloneHeroReportContract(nil))
	original := &domain.HeroReportContract{Version: 1, PayloadFields: []string{"a"}}
	clone := domain.CloneHeroReportContract(original)
	clone.PayloadFields[0] = "mutated"
	assert.Equal(t, []string{"a"}, original.PayloadFields, "the frozen copy must not alias the source")
	assert.Equal(t, 1, clone.Version)
}
