package prompts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitArgs_Basic(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", []string{}},
		{"single", "hello", []string{"hello"}},
		{"two words", "hello world", []string{"hello", "world"}},
		{"multiple spaces", "a  b   c", []string{"a", "b", "c"}},
		{"tabs", "a\tb\tc", []string{"a", "b", "c"}},
		{"mixed whitespace", "a \t b \t\t c", []string{"a", "b", "c"}},
		{"leading whitespace", "  hello", []string{"hello"}},
		{"trailing whitespace", "hello  ", []string{"hello"}},
		{"only whitespace", "  \t  ", []string{}},
		{"newline separator", "a\nb", []string{"a", "b"}},
		{"cr separator", "a\rb", []string{"a", "b"}},
		{"crlf separator", "a\r\nb", []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SplitArgs(tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSplitArgs_Quoting(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"double quotes group", `hello "world today"`, []string{"hello", "world today"}},
		{"single quotes group", `hello 'world today'`, []string{"hello", "world today"}},
		{"double quotes preserve inner single", `"it's"`, []string{"it's"}},
		{"single quotes literal backslash", `'a\b'`, []string{`a\b`}},
		{"double quotes escape backslash", `"a\\b"`, []string{`a\b`}},
		{"double quotes escape quote", `"a\"b"`, []string{`a"b`}},
		{"empty double quotes", `""`, []string{""}},
		{"empty single quotes", `''`, []string{""}},
		{"quotes within word", `ab"c d"ef`, []string{"abc def"}},
		{"adjacent quoted segments", `a"b"'c'`, []string{"abc"}},
		{"mixed quotes mid-word", `prefix"inner text"suffix`, []string{"prefixinner textsuffix"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SplitArgs(tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSplitArgs_NoExpansion(t *testing.T) {
	// These shell expressions must NOT be expanded.
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"dollar home", "$HOME", []string{"$HOME"}},
		{"dollar paren", "$(date)", []string{"$(date)"}},
		{"backtick", "`ls`", []string{"`ls`"}},
		{"glob star", "*.txt", []string{"*.txt"}},
		{"tilde", "~/dir", []string{"~/dir"}},
		{"equal sign", "--flag=value", []string{"--flag=value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SplitArgs(tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSplitArgs_Errors(t *testing.T) {
	// Unclosed quotes and trailing backslash must error.
	_, err := SplitArgs(`"hello`)
	assert.ErrorIs(t, err, ErrArgsUnterminatedQuote)

	_, err = SplitArgs(`'hello`)
	assert.ErrorIs(t, err, ErrArgsUnterminatedQuote)

	_, err = SplitArgs(`hello\`)
	assert.ErrorIs(t, err, ErrArgsTrailingBackslash)
}

func TestSplitArgs_Multiline(t *testing.T) {
	// Multi-line raw arguments: newlines act as separators.
	got, err := SplitArgs("alpha\nbeta\ncharlie")
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta", "charlie"}, got)

	// Newline inside double quotes is preserved as part of the arg.
	got, err = SplitArgs("first \"line 1\nline 2\" last")
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "line 1\nline 2", "last"}, got)
}

func TestSplitArgs_BackslashInUnquoted(t *testing.T) {
	got, err := SplitArgs(`a\ b c\d`)
	require.NoError(t, err)
	assert.Equal(t, []string{"a b", "cd"}, got)
}
