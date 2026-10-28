package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"

	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/web/static"
)

func TestGenerateMatchesCommittedFile(t *testing.T) {
	got := generate()
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
	got := string(generate())
	tests := []struct {
		name, want string
	}{
		{"block", "pre.code {"},
		{"block scrolls", "overflow-x: auto;"},
		{"block font", "var(--font-mono,"},
		{"block ground", "background: var(--code-ground);"},
		{"block text", "color: var(--code-text);"},
		{"block border", "border: 1px solid var(--code-border);"},
		{"keyword", "\n.code .hl-k { color: var(--code-keyword); }"},
		{"declaration keyword", "\n.code .hl-kd { color: var(--code-keyword); }"},
		{"type keyword", "\n.code .hl-kt { color: var(--code-type); }"},
		{"function", "\n.code .hl-nf { color: var(--code-function); }"},
		{"class is a type", "\n.code .hl-nc { color: var(--code-type); }"},
		{"plain name", "\n.code .hl-n { color: var(--code-name); }"},
		{"double quoted string", "\n.code .hl-s2 { color: var(--code-string); }"},
		{"integer", "\n.code .hl-mi { color: var(--code-number); }"},
		{"comment", "\n.code .hl-c1 { color: var(--code-comment); }"},
		{"preprocessor is a keyword", "\n.code .hl-cp { color: var(--code-keyword); }"},
		{"operator", "\n.code .hl-o { color: var(--code-operator); }"},
		{"operator word is a keyword", "\n.code .hl-ow { color: var(--code-keyword); }"},
		{"punctuation", "\n.code .hl-p { color: var(--code-punctuation); }"},
		{"strong", "\n.code .hl-gs { font-weight: bold; }"},
		{"emphasis", "\n.code .hl-ge { font-style: italic; }"},
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

// TestGenerateUsesTokensOnly checks that no colour is written in code.css: no
// #hex, no rgb() or hsl(), and every var() is a --code- token or the font.
func TestGenerateUsesTokensOnly(t *testing.T) {
	got := string(generate())
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`),
		regexp.MustCompile(`\b(rgb|rgba|hsl|hsla|oklch|oklab|lab|lch)\(`),
	} {
		if m := re.FindString(got); m != "" {
			t.Errorf("code.css has the colour %q", m)
		}
	}
	for _, m := range regexp.MustCompile(`var\(--([a-z-]+)`).FindAllStringSubmatch(got, -1) {
		if !strings.HasPrefix(m[1], "code-") && m[1] != "font-mono" {
			t.Errorf("code.css uses the custom property --%s", m[1])
		}
	}
}

// TestEveryTokenTypeIsMapped checks that each token class chroma
// can write maps to a family (or is one of the few without a colour), so that
// a type added by a new chroma version is not left unstyled by accident.
func TestEveryTokenTypeIsMapped(t *testing.T) {
	uncoloured := map[chroma.TokenType]bool{
		chroma.Background: true, chroma.PreWrapper: true, chroma.Line: true, chroma.CodeLine: true,
		chroma.LineNumbers: true, chroma.LineNumbersTable: true, chroma.LineHighlight: true,
		chroma.LineTable: true, chroma.LineTableTD: true, chroma.LineLink: true,
		chroma.Text: true, chroma.Whitespace: true, chroma.Other: true, chroma.Generic: true,
	}
	for tt, name := range chroma.StandardTypes {
		if uncoloured[tt] {
			continue
		}
		fam, extra := family(tt)
		if fam == "" && len(extra) == 0 {
			t.Errorf("token type %s (%s%s) has no family", tt, markdown.CodeClassPrefix, name)
		}
	}
}
