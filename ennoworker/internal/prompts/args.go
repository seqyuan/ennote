// Package prompts implements the prompt-template slash-command system: template
// file parsing, argument tokenization, placeholder expansion, multi-tier
// registry merging, and global CRUD storage.
//
// This file implements SplitArgs, a safe shell-like lexical tokenizer that
// honours single quotes, double quotes, backslash escapes, and concatenation
// of adjacent segments, without performing any shell evaluation.
package prompts

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrArgsUnterminatedQuote is returned by SplitArgs when a single- or
// double-quoted segment is not closed before the end of input.
var ErrArgsUnterminatedQuote = errors.New("unterminated quote in template arguments")

// ErrArgsTrailingBackslash is returned by SplitArgs when the input ends with
// an isolated, unescaped backslash.
var ErrArgsTrailingBackslash = errors.New("trailing backslash in template arguments")

// SplitArgs tokenizes raw using shell-like lexical quoting.
func SplitArgs(raw string) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	var args []string
	var current strings.Builder
	var building bool
	i := 0
	n := len(raw)

	emit := func() {
		args = append(args, current.String())
		current.Reset()
	}

	for i < n {
		c := raw[i]

		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			if building {
				emit()
				building = false
			}
			i++
			continue
		}

		building = true

		if c == '\'' {
			i++
			start := i
			for i < n && raw[i] != '\'' {
				i++
			}
			if i >= n {
				return nil, ErrArgsUnterminatedQuote
			}
			current.WriteString(raw[start:i])
			i++
			continue
		}

		if c == '"' {
			i++
			for i < n && raw[i] != '"' {
				if raw[i] == '\\' && i+1 < n {
					i++
				}
				r, size := utf8.DecodeRuneInString(raw[i:])
				current.WriteRune(r)
				i += size
			}
			if i >= n {
				return nil, ErrArgsUnterminatedQuote
			}
			i++
			continue
		}

		if c == '\\' {
			if i+1 >= n {
				return nil, ErrArgsTrailingBackslash
			}
			i++
			r, size := utf8.DecodeRuneInString(raw[i:])
			current.WriteRune(r)
			i += size
			continue
		}

		r, size := utf8.DecodeRuneInString(raw[i:])
		current.WriteRune(r)
		i += size
	}

	if building {
		emit()
	}

	if args == nil {
		return []string{}, nil
	}
	return args, nil
}

func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}
