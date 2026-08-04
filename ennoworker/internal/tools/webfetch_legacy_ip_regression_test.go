package tools

import "testing"

// Regression: hex-letter domains must never be treated as legacy IPv4 literals.
func TestLooksLikeLegacyIPv4Literal_NoFalsePositiveDomains(t *testing.T) {
	for _, host := range []string{
		"cafe.com", "dead.beef", "cafe.f00", "badcafe.org",
		"face.example", "example.com", "google.com", "sub.domain.io",
	} {
		if looksLikeLegacyIPv4Literal(host) {
			t.Errorf("legit domain %q rejected as legacy IPv4 literal", host)
		}
	}
}

// Real legacy numeric forms must still be rejected.
func TestLooksLikeLegacyIPv4Literal_TruePositives(t *testing.T) {
	for _, host := range []string{
		"2130706433", "127.1", "10.0", "0177.0.0.1",
		"0x7f000001", "0x7f.0.0.1", "010.0.0.1",
	} {
		if !looksLikeLegacyIPv4Literal(host) {
			t.Errorf("legacy IPv4 form %q not detected", host)
		}
	}
}
