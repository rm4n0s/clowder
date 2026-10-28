package markdown

import (
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// PlainText returns the readable text of body outside code, which is where
// keywords are looked for (SPEC §7.15). A text body is its own text. In a
// markdown body the code spans, code blocks, link destinations, autolinks,
// images and raw HTML are left out, as Mentions leaves them out; the blocks
// are separated by a line break. An unknown format has no text.
func (r *Renderer) PlainText(format Format, body string) string {
	switch format {
	case FormatText:
		return body
	case FormatMarkdown:
		src := []byte(body)
		doc := r.md.Parser().Parse(text.NewReader(src))
		var sb strings.Builder
		_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			if n.Type() == ast.TypeBlock && n.FirstChild() != nil && n.FirstChild().Type() == ast.TypeInline {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				flattenInline(&sb, src, n)
				return ast.WalkSkipChildren, nil
			}
			return ast.WalkContinue, nil
		})
		return sb.String()
	default:
		return ""
	}
}

// KeywordMatcher finds keywords in a text: whole words, ignoring case, and
// phrases of several words, in which any run of white space matches one space.
// Make it with NewKeywordMatcher; it is cheap to ask it about many keywords.
type KeywordMatcher struct {
	text string
}

// NewKeywordMatcher prepares s for matching: lower-cased, with every run of
// white space made one space.
func NewKeywordMatcher(s string) KeywordMatcher {
	return KeywordMatcher{text: foldKeyword(s)}
}

// Matches reports whether the keyword is in the text as a whole word or
// phrase: the character before it and the one after it are not letters,
// digits or underscores (when the keyword itself starts or ends with one), so
// "deploy" matches in "Deploy now!" and "the deploy-script" but not in
// "redeployed". An empty keyword matches nothing.
func (m KeywordMatcher) Matches(keyword string) bool {
	kw := foldKeyword(keyword)
	if kw == "" || m.text == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(kw)
	last, _ := utf8.DecodeLastRuneInString(kw)
	for from := 0; from <= len(m.text)-len(kw); {
		i := strings.Index(m.text[from:], kw)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(kw)
		if boundaryBefore(m.text, start, first) && boundaryAfter(m.text, end, last) {
			return true
		}
		from = start + 1
	}
	return false
}

// boundaryBefore reports whether a keyword that starts with the rune first can
// start at i of s.
func boundaryBefore(s string, i int, first rune) bool {
	if i == 0 || !isWordRune(first) {
		return true
	}
	prev, _ := utf8.DecodeLastRuneInString(s[:i])
	return !isWordRune(prev)
}

// boundaryAfter reports whether a keyword that ends with the rune last can end
// at i of s.
func boundaryAfter(s string, i int, last rune) bool {
	if i >= len(s) || !isWordRune(last) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(s[i:])
	return !isWordRune(next)
}

// foldKeyword makes s comparable ignoring case: white space runs become one
// space, letters are lower-cased, and the Greek final sigma is the ordinary
// one (lower-casing a capital sigma never gives the final form).
func foldKeyword(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(s), " ")), "ς", "σ")
}
