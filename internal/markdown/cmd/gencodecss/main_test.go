package main

import (
	"strings"
	"testing"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/clowder/internal/web/static"
)

func TestGenerateMatchesCommittedFile(t *testing.T) {
	got, err := generate(lightStyle, darkStyle)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	want, err := static.FS.ReadFile("code.css")
	if err != nil {
		t.Fatalf("read code.css: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("internal/web/static/code.css is out of date: run go generate ./... (%d bytes generated, %d committed)",
			len(got), len(want))
	}
}

func TestGenerateContent(t *testing.T) {
	css, err := generate(lightStyle, darkStyle)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got := string(css)
	tests := []struct {
		name, want string
	}{
		{"block", "pre.code {"},
		{"block scrolls", "overflow-x: auto;"},
		{"block font", "var(--mono,"},
		{"light keyword", "\n.code .hl-k {"},
		{"light comment", "\n.code .hl-c1 {"},
		{"dark by setting", ":root[data-theme=\"dark\"] .code .hl-c1 {"},
		{"dark by system", "@media (prefers-color-scheme: dark) {\n  :root:not([data-theme=\"light\"]) {"},
		{"dark properties by setting", ":root[data-theme=\"dark\"] {\n  --code-bg:"},
		{"dark tokens by system", "\n  :root:not([data-theme=\"light\"]) .code .hl-c1 {"},
		{"dark properties by system", "  :root:not([data-theme=\"light\"]) {\n    --code-bg:"},
		{"light palette", "github"},
		{"dark palette", "github-dark"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(got, tt.want) {
				t.Errorf("code.css lacks %q", tt.want)
			}
		})
	}
	if strings.Contains(got, "url(") || strings.Contains(got, "@import") {
		t.Errorf("code.css has a url or an import")
	}
}

func TestGenerateStyleMissing(t *testing.T) {
	tests := []struct {
		name        string
		light, dark string
	}{
		{"light missing", "no-such-style", darkStyle},
		{"dark missing", lightStyle, "no-such-style"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := generate(tt.light, tt.dark)
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if !appErr.HasRoute("generate.CodeStyleMissing") {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
		})
	}
}
