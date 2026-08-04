package prompts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// ParseInvocation tests
// ============================================================================

func TestParseInvocation_Valid(t *testing.T) {
	tests := []struct {
		input   string
		name    string
		rawArgs string
	}{
		{"/foo", "foo", ""},
		{"/foo ", "foo", ""},
		{"/foo  ", "foo", ""},
		{"/foo bar", "foo", "bar"},
		{"/foo bar baz", "foo", "bar baz"},
		{"/foo\tbar", "foo", "bar"},
		{"/a", "a", ""},
		{"/a123_-456", "a123_-456", ""},
		{"/a123_-456 abc", "a123_-456", "abc"},
		{"/hello\nworld", "hello", "world"},
		{"/hello\r\nworld", "hello", "world"},
		{"/hello \t\r\n  world", "hello", "world"},
		// Name exactly 32 chars.
		{"/" + strings.Repeat("a", 32) + " arg", strings.Repeat("a", 32), "arg"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := ParseInvocation(tt.input)
			require.True(t, ok, "expected valid: %q", tt.input)
			assert.Equal(t, tt.name, got.Name)
			assert.Equal(t, tt.rawArgs, got.RawArguments)
		})
	}
}

func TestParseInvocation_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"foo",
		" foo",
		"/",
		"/ ",
		"//foo",
		"/foo/bar",
		"/foo!",
		"/FOO",
		"/_foo",
		"/-foo",
		"/foo@",
		"/foo!bar",
		// Name > 32 chars.
		"/" + strings.Repeat("a", 33),
		"/" + strings.Repeat("a", 33) + " arg",
	}
	for _, s := range invalid {
		t.Run(s, func(t *testing.T) {
			_, ok := ParseInvocation(s)
			assert.False(t, ok, "expected invalid: %q", s)
		})
	}
}

func TestParseInvocation_MultilineArgs(t *testing.T) {
	got, ok := ParseInvocation("/review alpha\nbeta\ncharlie")
	require.True(t, ok)
	assert.Equal(t, "review", got.Name)
	assert.Equal(t, "alpha\nbeta\ncharlie", got.RawArguments)
}

// ============================================================================
// ExpandTemplate — positional $N
// ============================================================================

func TestExpandTemplate_Positional(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		args    []string
		want    string
		wantErr error
	}{
		{"$1", "$1", []string{"alpha"}, "alpha", nil},
		{"$01 leading zero", "$01", []string{"alpha"}, "alpha", nil},
		{"$10 multi-digit", "$10", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}, "j", nil},
		{"$N out of range", "$3", []string{"a"}, "", ErrExpandedPromptEmpty},
		{"repeated $1", "$1-$1", []string{"x"}, "x-x", nil},
		{"multiple", "$1 $2 $3", []string{"a", "b", "c"}, "a b c", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandTemplate(tt.body, tt.args)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ============================================================================
// ExpandTemplate — $@ / $ARGUMENTS / boundary
// ============================================================================

func TestExpandTemplate_AllArgs(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		args    []string
		want    string
		wantErr error
	}{
		{"$@ basic", "$@", []string{"a", "b", "c"}, "a b c", nil},
		{"$@ single", "$@", []string{"hello"}, "hello", nil},
		{"$@ empty", "$@", []string{}, "", ErrExpandedPromptEmpty},
		{"$ARGUMENTS", "ARG: $ARGUMENTS", []string{"a", "b"}, "ARG: a b", nil},
		{"$@3 joined+literal", "$@3", []string{"alpha", "beta"}, "alpha beta3", nil},
		{"$@42 joined+literal", "$@42", []string{"alpha", "beta"}, "alpha beta42", nil},
		{"$@suffix", "$@suffix", []string{"a", "b"}, "a bsuffix", nil},
		{"$@. punctuation", "result: $@.", []string{"x"}, "result: x.", nil},
		{"$@ end of input", "$@", []string{"alpha", "beta"}, "alpha beta", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandTemplate(tt.body, tt.args)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExpandTemplate_ARGUMENTSBoundary(t *testing.T) {
	// $ARGUMENTS followed by [a-zA-Z0-9_] → NOT a placeholder.
	assertExpanded(t, "$ARGUMENTSTEST", nil, "$ARGUMENTSTEST")
	assertExpanded(t, "$ARGUMENTS_TEST", []string{"x"}, "$ARGUMENTS_TEST\n\nx")
	assertExpanded(t, "$ARGUMENTS2", []string{"x"}, "$ARGUMENTS2\n\nx")
	// Followed by non-identifier → IS a placeholder.
	assertExpanded(t, "$ARGUMENTS.", []string{"x"}, "x.")
	assertExpanded(t, "$ARGUMENTS!", []string{"x", "y"}, "x y!")
	// End of input → match.
	assertExpanded(t, "pre $ARGUMENTS post", []string{"a", "b"}, "pre a b post")
	// $ARGUMENTS with no args → empty string, entire body becomes empty → ErrExpandedPromptEmpty.
	_, err := ExpandTemplate("$ARGUMENTS", []string{})
	assert.ErrorIs(t, err, ErrExpandedPromptEmpty)
}

// ============================================================================
// ExpandTemplate — braced forms
// ============================================================================

func TestExpandTemplate_BracedDefaults(t *testing.T) {
	tests := []struct {
		name string
		body string
		args []string
		want string
	}{
		{"${1:-d} with arg", "${1:-default}", []string{"hello"}, "hello"},
		{"${1:-d} out of range", "${1:-default}", []string{}, "default"},
		{"${1:-d} empty arg", "${1:-default}", []string{""}, "default"},
		{"${@:-d} with args", "${@:-fallback}", []string{"a", "b"}, "a b"},
		{"${@:-d} no args", "${@:-fallback}", []string{}, "fallback"},
		{"${ARGUMENTS:-d} non-empty", "${ARGUMENTS:-fb}", []string{"x"}, "x"},
		{"${ARGUMENTS:-d} empty", "${ARGUMENTS:-fb}", []string{}, "fb"},
		{"${2:-d} N>len", "${2:-second}", []string{"first"}, "second"},
		{"${2:-d} N==len", "${2:-second}", []string{"first", "second"}, "second"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertExpanded(t, tt.body, tt.args, tt.want)
		})
	}
}

func TestExpandTemplate_BracedSlice(t *testing.T) {
	args := []string{"a", "b", "c"}
	assertExpanded(t, "${@:1}", args, "a b c")
	assertExpanded(t, "${@:2}", args, "b c")
	assertExpanded(t, "${@:3}", args, "c")
	// ${@:4} beyond range → empty → ErrExpandedPromptEmpty.
	_, err := ExpandTemplate("${@:4}", args)
	assert.ErrorIs(t, err, ErrExpandedPromptEmpty)
	assertExpanded(t, "${@:1:2}", args, "a b")
	assertExpanded(t, "${@:2:1}", args, "b")
	// L=0 or empty args → empty expansion → error.
	_, err = ExpandTemplate("${@:1:0}", args)
	assert.ErrorIs(t, err, ErrExpandedPromptEmpty)
	_, err = ExpandTemplate("${@:1}", []string{})
	assert.ErrorIs(t, err, ErrExpandedPromptEmpty)
	// Clamped: limit larger than remaining.
	assertExpanded(t, "pre ${@:1:10} post", args, "pre a b c post")
}

func TestExpandTemplate_UnknownBraced(t *testing.T) {
	// Unsupported braced forms → empty string (not preserved).
	assertExpanded(t, "X${foo}Y", []string{"a"}, "XY")
	assertExpanded(t, "X${@:0}Y", []string{"a"}, "XY")  // N=0 invalid
	assertExpanded(t, "X${@:x}Y", []string{"a"}, "XY")  // non-numeric
}

// ============================================================================
// ExpandTemplate — $$ / no-placeholder / edge cases
// ============================================================================

func TestExpandTemplate_DollarDollar(t *testing.T) {
	// $$ → literal $; only-$$ counts as no-placeholder.
	// No args: return the body with $$ expanded.
	assertExpanded(t, "price: $$5", []string{}, "price: $5")
	// With args: $$ expanded, then args appended.
	assertExpanded(t, "price: $$5", []string{"x"}, "price: $5\n\nx")
	// Multiple $$ with other placeholders (hasPlaceholder is true).
	assertExpanded(t, "$$$1$$", []string{"HI"}, "$HI$")
}

func TestExpandTemplate_NoPlaceholder(t *testing.T) {
	// No placeholders at all, no args: verbatim.
	assertExpanded(t, "hello world", []string{}, "hello world")
	assertExpanded(t, "hello world", nil, "hello world")
	// No placeholders, with args: append after blank line.
	assertExpanded(t, "hello world", []string{"a", "b"}, "hello world\n\na b")
}

func TestExpandTemplate_NoReExpansion(t *testing.T) {
	// Default literal is not re-expanded.
	assertExpanded(t, "${1:-$HOME}", []string{}, "$HOME")
	// Arg value containing $ is not re-expanded.
	assertExpanded(t, "$1", []string{"$ARGUMENTS"}, "$ARGUMENTS")
	assertExpanded(t, "$@", []string{"$HOME", "${foo}"}, "$HOME ${foo}")
}

func TestExpandTemplate_MissingBrace(t *testing.T) {
	// Missing closing } → $ emitted literally, no placeholder recognised.
	// No args → verbatim.
	assertExpanded(t, "${1:-default", []string{}, "${1:-default")
	// With args → no-placeholder append.
	assertExpanded(t, "${1:-default", []string{"x"}, "${1:-default\n\nx")
}

func TestExpandTemplate_UnknownPlaceholder(t *testing.T) {
	// $x is not a recognised placeholder → no-placeholder template.
	// No args: verbatim.
	assertExpanded(t, "$x", []string{}, "$x")
	// With args: append after blank line.
	assertExpanded(t, "$x", []string{"a"}, "$x\n\na")
}

func TestExpandTemplate_TooLarge(t *testing.T) {
	// Build a template with many $@ references that will blow up the output.
	// Each $@ expands to joined args. Build many repetitions.
	args := []string{strings.Repeat("x", 10000)} // each arg 10 KiB joined = 10 KiB
	line := strings.Repeat("$@", 30)             // each line ~30 * 10 KiB = 300 KiB > 256 KiB
	body := line
	_, err := ExpandTemplate(body, args)
	require.ErrorIs(t, err, ErrExpandedPromptTooLarge)
}

func TestExpandTemplate_EmptyExpansion(t *testing.T) {
	// Body with placeholder that expands to empty → ErrExpandedPromptEmpty.
	_, err := ExpandTemplate("$1", []string{})
	assert.ErrorIs(t, err, ErrExpandedPromptEmpty)

	// Body with only whitespace and a placeholder that expands to empty.
	_, err = ExpandTemplate("  $1  ", []string{})
	assert.ErrorIs(t, err, ErrExpandedPromptEmpty)

	// Body that expands to non-empty whitespace → still empty.
	// " " (space) + $@ with no args: $@ → "", body = " " → whitespace only → error.
	_, err = ExpandTemplate(" $@ ", []string{})
	assert.ErrorIs(t, err, ErrExpandedPromptEmpty)

	// But if there's at least one non-whitespace char, it succeeds.
	got, err := ExpandTemplate(" $1 ", []string{"x"})
	require.NoError(t, err)
	assert.Equal(t, " x ", got)
}

func TestExpandTemplate_BodyWithVerificationExample(t *testing.T) {
	// From the design doc §14 verification item 11:
	// Body "结果：$@3" with args [alpha, beta] → "结果：alpha beta3"
	assertExpanded(t, "结果：$@3", []string{"alpha", "beta"}, "结果：alpha beta3")
}

// ============================================================================
// Helpers
// ============================================================================

func assertExpanded(t *testing.T, body string, args []string, want string) {
	t.Helper()
	got, err := ExpandTemplate(body, args)
	require.NoError(t, err, "ExpandTemplate(%q, %v) unexpected error", body, args)
	assert.Equal(t, want, got, "ExpandTemplate(%q, %v)", body, args)
}
