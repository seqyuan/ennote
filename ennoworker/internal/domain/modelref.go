package domain

import (
	"fmt"
	"strings"
	"unicode"
)

// SplitPortableModelRef is the canonical syntax rule for a portable model
// reference, shared by the Hero and Graph source parsers. It splits on the
// FIRST slash, so the model part may itself contain slashes (for example
// "openai-main/org/gpt-5.4"). The provider part must be non-empty and contain
// no whitespace; the model part must be non-empty and contain no whitespace.
//
// This rule is deliberately permissive about what a provider part may look
// like. A source file must stay parseable so the UI can open it and the user
// can fix an unresolvable reference; resolution-level callers (for example
// fileconfig.SplitModelRef) layer a stricter provider-key check on top because
// they must reject anything that cannot name a real provider.
func SplitPortableModelRef(ref string) (provider, model string, err error) {
	ref = strings.TrimSpace(ref)
	separator := strings.IndexByte(ref, '/')
	if separator <= 0 || separator == len(ref)-1 {
		return "", "", fmt.Errorf("portable model reference must use provider/model")
	}
	provider, model = ref[:separator], ref[separator+1:]
	if containsWhitespace(provider) || containsWhitespace(model) {
		return "", "", fmt.Errorf("portable model reference must not contain whitespace")
	}
	return provider, model, nil
}

func containsWhitespace(value string) bool {
	return strings.IndexFunc(value, unicode.IsSpace) >= 0
}
