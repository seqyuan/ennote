package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandHostPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}

	cases := []struct {
		name string
		in   string
		want func(string) string
	}{
		{name: "bare tilde", in: "~", want: func(h string) string { return h }},
		{name: "tilde slash", in: "~/projects/rna", want: func(h string) string { return filepath.Join(h, "projects", "rna") }},
		{name: "absolute passthrough", in: "/data/projects/x", want: func(h string) string { return "/data/projects/x" }},
	}
	if filepath.Separator == '\\' {
		cases = append(cases, struct {
			name string
			in   string
			want func(string) string
		}{name: "tilde backslash", in: `~\projects\rna`, want: func(h string) string { return filepath.Join(h, "projects", "rna") }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandHostPath(tc.in)
			if err != nil {
				t.Fatalf("expandHostPath(%q): %v", tc.in, err)
			}
			want := filepath.Clean(tc.want(home))
			if got != want {
				t.Fatalf("expandHostPath(%q) = %q, want %q", tc.in, got, want)
			}
			if !filepath.IsAbs(got) {
				t.Fatalf("expandHostPath(%q) not absolute: %q", tc.in, got)
			}
		})
	}
}

func TestExpandHostPathExpandsTildeInsidePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	got, err := expandHostPath("~/data")
	if err != nil {
		t.Fatalf("expandHostPath: %v", err)
	}
	if !strings.HasPrefix(got, home) {
		t.Fatalf("expected expansion under home %q, got %q", home, got)
	}
}
