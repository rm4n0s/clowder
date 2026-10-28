package markdown_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/clowder/internal/markdown"
)

func render(t *testing.T, f markdown.Format, body string) string {
	t.Helper()
	out, err := markdown.New().Render(f, body)
	if err != nil {
		t.Fatalf("Render(%q, %q): %v", f, body, err)
	}
	return string(out)
}

func TestRenderMarkdownFeatures(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []string // substrings the output must contain
		notWant []string // substrings it must not contain
	}{
		{name: "emphasis", body: "*a* and **b**", want: []string{"<em>a</em>", "<strong>b</strong>"}},
		{name: "list", body: "- one\n- two\n\n1. x\n2. y", want: []string{"<ul>", "<li>one</li>", "<ol>", "<li>y</li>"}},
		{name: "table", body: "| a | b |\n|---|---|\n| 1 | 2 |", want: []string{"<table>", "<th>a</th>", "<td>2</td>"}},
		{name: "strikethrough", body: "~~gone~~", want: []string{"<del>gone</del>"}},
		{
			name: "autolink http",
			body: "see https://example.com/a?b=1 now",
			want: []string{`<a href="https://example.com/a?b=1" rel="nofollow noopener noreferrer" target="_blank">https://example.com/a?b=1</a>`},
		},
		{
			name: "autolink www",
			body: "see www.example.com now",
			want: []string{`href="http://www.example.com"`, `target="_blank"`},
		},
		{
			name: "autolink angle brackets",
			body: "<https://example.com>",
			want: []string{`href="https://example.com"`, `rel="nofollow noopener noreferrer"`},
		},
		{
			name: "email autolink",
			body: "<a@b.example>",
			want: []string{`href="mailto:a@b.example"`, `target="_blank"`},
		},
		{
			name: "task list is read-only",
			body: "- [ ] open\n- [x] done",
			want: []string{`<input disabled="" type="checkbox">`, `<input checked="" disabled="" type="checkbox">`},
		},
		{
			name:    "code span is escaped",
			body:    "use `<b>x</b> & y`",
			want:    []string{"<code>&lt;b&gt;x&lt;/b&gt; &amp; y</code>"},
			notWant: []string{"<b>"},
		},
		{
			name:    "fenced code is escaped",
			body:    "```html\n<script>alert(1)</script>\n```",
			want:    []string{"&lt;", "script", "&gt;"},
			notWant: []string{"<script"},
		},
		{
			name:    "fenced code without a language is escaped",
			body:    "```\n<script>alert(1)</script>\n```",
			want:    []string{"<pre class=\"code\"><code>&lt;script&gt;alert(1)&lt;/script&gt;\n</code></pre>"},
			notWant: []string{"<script"},
		},
		{
			name:    "indented code is escaped",
			body:    "    <b>x</b>",
			want:    []string{"&lt;b&gt;x&lt;/b&gt;"},
			notWant: []string{"<b>"},
		},
		{
			name:    "headings are shifted down",
			body:    "# one\n## two\n### three\n#### four\n##### five\n###### six\n\nSetext\n======",
			want:    []string{"<h3>one</h3>", "<h4>two</h4>", "<h5>three</h5>", "<h6>four</h6>", "<h6>five</h6>", "<h6>six</h6>", "<h3>Setext</h3>"},
			notWant: []string{"<h1", "<h2"},
		},
		{
			name:    "headings have no id",
			body:    "# Title {#custom}\n## Other",
			notWant: []string{" id=", "<h1", "<h2"},
		},
		{
			name: "image is lazy",
			body: `![a cat](https://example.com/c.png "Tom")`,
			want: []string{`<img src="https://example.com/c.png" alt="a cat" title="Tom" loading="lazy">`},
		},
		{
			name: "link with title",
			body: `[x](https://example.com "T")`,
			want: []string{`<a href="https://example.com" title="T" rel="nofollow noopener noreferrer" target="_blank">x</a>`},
		},
		{
			name: "relative link",
			body: "[x](/rooms/1)",
			want: []string{`<a href="/rooms/1"`, `target="_blank"`},
		},
		{
			name: "mailto link",
			body: "[x](mailto:a@b.example)",
			want: []string{`<a href="mailto:a@b.example"`},
		},
		{name: "blockquote", body: "> quoted", want: []string{"<blockquote>", "quoted"}},
		{name: "empty", body: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, markdown.FormatMarkdown, tt.body)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("output lacks %q:\n%s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("output has %q:\n%s", w, got)
				}
			}
		})
	}
}

var (
	// reTag matches one tag, with quoted attribute values inside.
	reTag    = regexp.MustCompile(`<[^>"']*(?:(?:"[^"]*"|'[^']*')[^>"']*)*>`)
	reTagEnd = regexp.MustCompile(`^</?([a-zA-Z][a-zA-Z0-9]*)`)
	reAttr   = regexp.MustCompile(`\s([a-zA-Z-]+)(?:="([^"]*)")?`)
	// reClassValue is what the class of an element may be: the code block, a
	// highlighted token or a mention.
	reClassValue = regexp.MustCompile(`^(code|mention|hl-[a-z0-9]{1,4})$`)
	// reLangValue is what data-lang of a code block may be.
	reLangValue = regexp.MustCompile(`^[A-Za-z0-9_+#.-]{1,32}$`)
	// reQuoted matches a quoted attribute value.
	reQuoted = regexp.MustCompile(`"[^"]*"`)

	allowedTags = []string{
		"p", "br", "a", "img", "em", "strong", "del", "code", "pre", "ul", "ol", "li", "blockquote",
		"h3", "h4", "h5", "h6", "hr", "table", "thead", "tbody", "tr", "th", "td", "input",
		"span", // highlighted code (code.go): <span class="hl-…">
	}
	allowedAttrs = []string{
		"href", "title", "rel", "target", "src", "alt", "loading", "disabled", "checked", "type", "align", "start",
		"class", "data-lang", // code blocks (code.go)
	}
)

// checkNoExecutableHTML fails when out has a tag or an attribute that the
// package does not write on purpose, or a script URL in href or src. Text that
// only looks like markup is fine when it is escaped, so the check reads the
// real tags and attributes instead of searching for substrings.
func checkNoExecutableHTML(t *testing.T, out string) {
	t.Helper()
	rest := strings.ReplaceAll(out, "<!-- raw HTML omitted -->", "")
	for _, tag := range reTag.FindAllString(rest, -1) {
		m := reTagEnd.FindStringSubmatch(tag)
		if m == nil {
			t.Errorf("unexpected markup %q in:\n%s", tag, out)
			continue
		}
		if !slices.Contains(allowedTags, strings.ToLower(m[1])) {
			t.Errorf("unexpected tag %q in:\n%s", tag, out)
		}
		for _, a := range reAttr.FindAllStringSubmatch(reQuotedValues(tag), -1) {
			name := strings.ToLower(a[1])
			if !slices.Contains(allowedAttrs, name) {
				t.Errorf("unexpected attribute %q in %q:\n%s", name, tag, out)
			}
		}
		for _, a := range reAttr.FindAllStringSubmatch(tag, -1) {
			name := strings.ToLower(a[1])
			if name == "class" && !reClassValue.MatchString(a[2]) {
				t.Errorf("unexpected class %q in:\n%s", a[2], out)
			}
			if name == "data-lang" && !reLangValue.MatchString(a[2]) {
				t.Errorf("unexpected data-lang %q in:\n%s", a[2], out)
			}
			if name != "href" && name != "src" {
				continue
			}
			v := strings.ToLower(strings.Map(func(r rune) rune {
				if r <= ' ' {
					return -1
				}
				return r
			}, a[2]))
			for _, bad := range []string{"javascript:", "vbscript:", "data:", "file:"} {
				if strings.HasPrefix(v, bad) {
					t.Errorf("dangerous %s %q in:\n%s", name, a[2], out)
				}
			}
		}
	}
	if left := reTag.ReplaceAllString(rest, ""); strings.Contains(left, "<") {
		t.Errorf("raw < outside a tag in:\n%s", out)
	}
}

// reQuotedValues returns tag with its quoted attribute values emptied, so that
// only the attribute names are left to look at.
func reQuotedValues(tag string) string {
	return reQuoted.ReplaceAllString(tag, `""`)
}

func TestRenderMarkdownInjection(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"script tag", `<script>alert(1)</script>`},
		{"script inline", `hello <script>alert(1)</script> world`},
		{"img onerror", `<img src=x onerror=alert(1)>`},
		{"img onerror inline", `text <img src=x onerror=alert(1)> text`},
		{"javascript link", `[x](javascript:alert(1))`},
		{"JAVASCRIPT link", `[x](JAVASCRIPT:alert(1))`},
		{"mixed case javascript link", `[x](JaVaScRiPt:alert(1))`},
		{"space before javascript", `[x](  javascript:alert(1))`},
		{"space before javascript in angle brackets", `[x](<  javascript:alert(1)>)`},
		{"tab inside scheme", "[x](<java\tscript:alert(1)>)"},
		{"entity in scheme", `[x](&#106;avascript:alert(1))`},
		{"hex entity in scheme", `[x](&#x6A;avascript:alert(1))`},
		{"percent in scheme", `[x](java%0ascript:alert(1))`},
		{"vbscript link", `[x](vbscript:msgbox(1))`},
		{"data link", `[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)`},
		{"file link", `[x](file:///etc/passwd)`},
		{"reference link", "[x][r]\n\n[r]: javascript:alert(1)"},
		{"raw anchor", `<a href="javascript:alert(1)">x</a>`},
		{"raw anchor block", "<a href=\"javascript:alert(1)\">x</a>\n\ntext"},
		{"image javascript", `![x](javascript:alert(1))`},
		{"image data", `![x](data:image/svg+xml;base64,PHN2Zy8+)`},
		{"image data png", `![x](data:image/png;base64,AAAA)`},
		{"image relative", `![x](/a.png)`},
		{"iframe", `<iframe src="https://evil.example"></iframe>`},
		{"iframe javascript", `<iframe src="javascript:alert(1)"></iframe>`},
		{"style", `<style>body{display:none}</style>`},
		{"style inline", `a <style>body{display:none}</style> b`},
		{"html comment", `<!-- <script>alert(1)</script> -->`},
		{"html comment inline", `a <!--x--> b`},
		{"cdata", `<![CDATA[<script>alert(1)</script>]]>`},
		{"link title attribute injection", `[x](http://a "\" onmouseover=\"alert(1)")`},
		{"link title quote entity", `[x](http://a "&quot; onmouseover=&quot;alert(1)")`},
		{"image title attribute injection", `![x](http://a "\" onerror=\"alert(1)")`},
		{"image alt injection", `![" onerror="alert(1)](http://a/b.png)`},
		{"link destination quote", `[x](<http://a/"onmouseover="alert(1)>)`},
		{"link text html", `[<script>alert(1)</script>](http://a)`},
		{"autolink javascript", `<javascript:alert(1)>`},
		{"autolink vbscript", `<vbscript:x>`},
		{"autolink data", `<data:text/html,x>`},
		{"svg onload", `<svg onload=alert(1)>`},
		{"object", `<object data="javascript:alert(1)"></object>`},
		{"form", `<form action="javascript:alert(1)"><input type=submit></form>`},
		{"html in heading", `# <script>alert(1)</script>`},
		{"html in table", "| a |\n|---|\n| <img src=x onerror=alert(1)> |"},
		{"html in list", "- <img src=x onerror=alert(1)>"},
		{"html in quote", "> <script>alert(1)</script>"},
		{"html in emphasis", `*<script>alert(1)</script>*`},
		{"task list html", "- [ ] <img src=x onerror=alert(1)>"},
		{"entity-encoded script", `&lt;script&gt;alert(1)&lt;/script&gt;`},
		{"null byte", "<scr\x00ipt>alert(1)</scr\x00ipt>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkNoExecutableHTML(t, render(t, markdown.FormatMarkdown, tt.body))
		})
	}
}

func TestRenderMarkdownDroppedLinksKeepText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
		not  string
	}{
		{"javascript link", `[click me](javascript:alert(1))`, "click me", "<a"},
		{"autolink javascript", `<javascript:alert(1)>`, "javascript:alert(1)", "<a"},
		{"ftp autolink", `<ftp://example.com/f>`, "ftp://example.com/f", "<a"},
		{"image javascript", `![the alt](javascript:alert(1))`, "the alt", "<img"},
		{"image relative", `![the alt](/a.png)`, "the alt", "<img"},
		{"image data", `![the alt](data:image/png;base64,AAAA)`, "the alt", "<img"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, markdown.FormatMarkdown, tt.body)
			if !strings.Contains(got, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, got)
			}
			if strings.Contains(got, tt.not) {
				t.Errorf("output has %q:\n%s", tt.not, got)
			}
		})
	}
}

func TestRenderMarkdownLinkAttributes(t *testing.T) {
	bodies := []string{
		"[a](https://example.com)",
		"[a](/rel)",
		"[a][r]\n\n[r]: https://example.com",
		"https://example.com",
		"www.example.com",
		"<https://example.com>",
		"<a@b.example>",
		"> [a](https://example.com)",
		"| a |\n|---|\n| [a](https://example.com) |",
		"- [a](https://example.com)",
		"[![img](https://example.com/i.png)](https://example.com)",
	}
	anchor := regexp.MustCompile(`<a [^>]*>`)
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			got := render(t, markdown.FormatMarkdown, body)
			tags := anchor.FindAllString(got, -1)
			if len(tags) == 0 {
				t.Fatalf("no link in:\n%s", got)
			}
			for _, tag := range tags {
				if !strings.Contains(tag, ` rel="nofollow noopener noreferrer"`) || !strings.Contains(tag, ` target="_blank"`) {
					t.Errorf("link without rel and target: %s", tag)
				}
			}
		})
	}
}

func TestRenderText(t *testing.T) {
	const rel = ` rel="nofollow noopener noreferrer" target="_blank"`
	tests := []struct {
		name string
		body string
		want string
	}{
		{"plain", "hello", "<p>hello</p>\n"},
		{"escape tags", "a <b>bold</b> c", "<p>a &lt;b&gt;bold&lt;/b&gt; c</p>\n"},
		{"escape ampersand", "Tom & Jerry &amp; co", "<p>Tom &amp; Jerry &amp;amp; co</p>\n"},
		{"escape quotes", `say "hi" and 'bye'`, "<p>say &#34;hi&#34; and &#39;bye&#39;</p>\n"},
		{"line breaks", "one\ntwo\nthree", "<p>one<br>\ntwo<br>\nthree</p>\n"},
		{"windows line breaks", "one\r\ntwo", "<p>one<br>\ntwo</p>\n"},
		{"paragraphs", "one\n\ntwo", "<p>one</p>\n<p>two</p>\n"},
		{"many blank lines", "one\n\n\n\ntwo\n\n", "<p>one</p>\n<p>two</p>\n"},
		{"blank line with spaces", "one\n  \t\ntwo", "<p>one</p>\n<p>two</p>\n"},
		{"empty", "", ""},
		{"only white space", " \n\n \n", ""},
		{
			"url linked",
			"see https://example.com/a now",
			`<p>see <a href="https://example.com/a"` + rel + `>https://example.com/a</a> now</p>` + "\n",
		},
		{
			"http url linked",
			"http://example.com",
			`<p><a href="http://example.com"` + rel + `>http://example.com</a></p>` + "\n",
		},
		{
			"upper case scheme",
			"HTTPS://Example.com/x",
			`<p><a href="HTTPS://Example.com/x"` + rel + `>HTTPS://Example.com/x</a></p>` + "\n",
		},
		{
			"trailing period",
			"go to https://example.com.",
			`<p>go to <a href="https://example.com"` + rel + `>https://example.com</a>.</p>` + "\n",
		},
		{
			"trailing comma and question",
			"https://example.com/a, or https://example.com/b?",
			`<p><a href="https://example.com/a"` + rel + `>https://example.com/a</a>, or <a href="https://example.com/b"` + rel + `>https://example.com/b</a>?</p>` + "\n",
		},
		{
			"url in parentheses",
			"(see https://example.com/a)",
			`<p>(see <a href="https://example.com/a"` + rel + `>https://example.com/a</a>)</p>` + "\n",
		},
		{
			"url with its own parentheses",
			"https://en.wikipedia.org/wiki/Go_(game) is",
			`<p><a href="https://en.wikipedia.org/wiki/Go_(game)"` + rel + `>https://en.wikipedia.org/wiki/Go_(game)</a> is</p>` + "\n",
		},
		{
			"url with query and ampersand",
			"https://example.com/?a=1&b=2",
			`<p><a href="https://example.com/?a=1&amp;b=2"` + rel + `>https://example.com/?a=1&amp;b=2</a></p>` + "\n",
		},
		{
			"two urls on a line",
			"https://a.example https://b.example",
			`<p><a href="https://a.example"` + rel + `>https://a.example</a> <a href="https://b.example"` + rel + `>https://b.example</a></p>` + "\n",
		},
		{
			"url on its own line",
			"first\nhttps://a.example\nlast",
			`<p>first<br>` + "\n" + `<a href="https://a.example"` + rel + `>https://a.example</a><br>` + "\n" + `last</p>` + "\n",
		},
		{"javascript is not linked", "javascript:alert(1)", "<p>javascript:alert(1)</p>\n"},
		{"javascript with a scheme-like prefix", "xhttp://a.example", "<p>xhttp://a.example</p>\n"},
		{"ftp is not linked", "ftp://example.com", "<p>ftp://example.com</p>\n"},
		{"data is not linked", "data:text/html,<script>x</script>", "<p>data:text/html,&lt;script&gt;x&lt;/script&gt;</p>\n"},
		{"scheme without host", "http:// alone", "<p>http:// alone</p>\n"},
		{
			"url cut at an angle bracket",
			"https://a.example/<script>",
			`<p><a href="https://a.example/"` + rel + `>https://a.example/</a>&lt;script&gt;</p>` + "\n",
		},
		{
			"url cut at a quote",
			`https://a.example/x"onmouseover="alert(1)`,
			`<p><a href="https://a.example/x"` + rel + `>https://a.example/x</a>&#34;onmouseover=&#34;alert(1)</p>` + "\n",
		},
		{"markdown is not rendered", "**bold** `code` # head", "<p>**bold** `code` # head</p>\n"},
		{"unicode", "héllo wörld ✓", "<p>héllo wörld ✓</p>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, markdown.FormatText, tt.body)
			if got != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tt.want)
			}
			checkNoExecutableHTML(t, got)
		})
	}
}

func TestRenderTextInjection(t *testing.T) {
	bodies := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<iframe src=x></iframe>`,
		`<style>x{}</style>`,
		`" onmouseover="alert(1)`,
		`https://a.example/" onmouseover="alert(1)`,
		`https://a.example/'onmouseover='alert(1)`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			checkNoExecutableHTML(t, render(t, markdown.FormatText, body))
		})
	}
}

func TestRenderUnknownFormat(t *testing.T) {
	for _, f := range []markdown.Format{"", "html", "Markdown", "TEXT"} {
		t.Run(string(f), func(t *testing.T) {
			out, err := markdown.New().Render(f, "<b>x</b>")
			if err == nil {
				t.Fatalf("Render(%q) returned %q and no error", f, out)
			}
			if out != "" {
				t.Errorf("Render(%q) returned output %q with an error", f, out)
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T", err)
			}
			if !appErr.HasRoute("Renderer.Render.FormatUnknown") {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
			if appErr.Tag != markdown.TagFormatUnknown {
				t.Errorf("tag = %q, want %q", appErr.Tag, markdown.TagFormatUnknown)
			}
			if appErr.Metadata["format"] != string(f) {
				t.Errorf("metadata = %v", appErr.Metadata)
			}
			if !errors.Is(err, &errors.Error{Tag: markdown.TagFormatUnknown}) {
				t.Errorf("errors.Is does not match the sentinel")
			}
		})
	}
}

func TestFormatValues(t *testing.T) {
	if markdown.FormatText != "text" || markdown.FormatMarkdown != "markdown" {
		t.Errorf("formats = %q, %q", markdown.FormatText, markdown.FormatMarkdown)
	}
}

func TestMentions(t *testing.T) {
	tests := []struct {
		name   string
		format markdown.Format
		body   string
		want   []string
	}{
		// Both formats.
		{"plain", markdown.FormatText, "hi @alice!", []string{"alice"}},
		{"plain md", markdown.FormatMarkdown, "hi @alice!", []string{"alice"}},
		{"several", markdown.FormatText, "@alice and @bob, @carol-x", []string{"alice", "bob", "carol-x"}},
		{"several md", markdown.FormatMarkdown, "@alice and @bob, @carol-x", []string{"alice", "bob", "carol-x"}},
		{"duplicates", markdown.FormatText, "@alice @bob @alice @ALICE", []string{"alice", "bob"}},
		{"duplicates md", markdown.FormatMarkdown, "@alice @bob @alice @ALICE", []string{"alice", "bob"}},
		{"upper case", markdown.FormatText, "@AlIcE", []string{"alice"}},
		{"upper case md", markdown.FormatMarkdown, "@AlIcE", []string{"alice"}},
		{"order of appearance", markdown.FormatText, "@zed @amy @mid", []string{"zed", "amy", "mid"}},
		{"at start", markdown.FormatText, "@alice hello", []string{"alice"}},
		{"at end", markdown.FormatText, "hello @alice", []string{"alice"}},
		{"at start md", markdown.FormatMarkdown, "@alice hello", []string{"alice"}},
		{"at end md", markdown.FormatMarkdown, "hello @alice", []string{"alice"}},
		{"only the mention", markdown.FormatText, "@alice", []string{"alice"}},
		{"after newline", markdown.FormatText, "a\n@alice", []string{"alice"}},
		{"after soft break md", markdown.FormatMarkdown, "a\n@alice", []string{"alice"}},
		{"in two paragraphs md", markdown.FormatMarkdown, "@alice\n\n@bob", []string{"alice", "bob"}},
		{"punctuation around", markdown.FormatText, `("@alice"), [@bob]; {@carol}: <@dave>. @eve? @fred!`, []string{"alice", "bob", "carol", "dave", "eve", "fred"}},
		{"punctuation around md", markdown.FormatMarkdown, `("@alice"), [@bob]; {@carol}: @eve? @fred!`, []string{"alice", "bob", "carol", "eve", "fred"}},
		{"digits underscore dash", markdown.FormatText, "@a_b-9", []string{"a_b-9"}},
		{"unicode before", markdown.FormatText, "«@alice»", []string{"alice"}},
		{"comma separated", markdown.FormatText, "@alice,@bob", []string{"alice", "bob"}},

		// Length.
		{"min length", markdown.FormatText, "@abc", []string{"abc"}},
		{"too short", markdown.FormatText, "@ab @a @", nil},
		{"too short md", markdown.FormatMarkdown, "@ab @a @", nil},
		{"max length", markdown.FormatText, "@" + strings.Repeat("a", 32), []string{strings.Repeat("a", 32)}},
		{"too long", markdown.FormatText, "@" + strings.Repeat("a", 33), nil},
		{"too long md", markdown.FormatMarkdown, "@" + strings.Repeat("a", 33), nil},
		{"non username chars end it", markdown.FormatText, "@alice.smith", []string{"alice"}},

		// Not a mention.
		{"email", markdown.FormatText, "write to bob@example.com", nil},
		{"email md", markdown.FormatMarkdown, "write to bob@example.com", nil},
		{"email and mention", markdown.FormatText, "bob@example.com and @carol", []string{"carol"}},
		{"email autolink md", markdown.FormatMarkdown, "<bob@example.com> and @carol", []string{"carol"}},
		{"word before", markdown.FormatText, "abc@def", nil},
		{"underscore before", markdown.FormatText, "a_@def", nil},
		{"digit before", markdown.FormatText, "1@def", nil},
		{"double at", markdown.FormatText, "@@alice", []string{"alice"}},
		{"chained", markdown.FormatText, "@alice@bob", []string{"alice"}},
		{"no mention", markdown.FormatText, "nothing here", nil},
		{"empty", markdown.FormatText, "", nil},
		{"empty md", markdown.FormatMarkdown, "", nil},
		{"escaped md", markdown.FormatMarkdown, `\@alice and @bob`, []string{"bob"}},

		// Markdown code, links and HTML.
		{"code span", markdown.FormatMarkdown, "`@alice` and @bob", []string{"bob"}},
		{"code span only", markdown.FormatMarkdown, "use `@alice`", nil},
		{"double backtick code span", markdown.FormatMarkdown, "``a `@alice` b`` @bob", []string{"bob"}},
		{"fenced code block", markdown.FormatMarkdown, "```\n@alice\n```\n\n@bob", []string{"bob"}},
		{"fenced code block with info", markdown.FormatMarkdown, "```go\nx := \"@alice\"\n```", nil},
		{"tilde fence", markdown.FormatMarkdown, "~~~\n@alice\n~~~", nil},
		{"indented code block", markdown.FormatMarkdown, "text\n\n    @alice\n\n@bob", []string{"bob"}},
		{"link destination", markdown.FormatMarkdown, "[mail](https://example.com/@alice) @bob", []string{"bob"}},
		{"link destination only", markdown.FormatMarkdown, "[x](https://example.com/@alice)", nil},
		{"link text", markdown.FormatMarkdown, "[@alice](https://example.com)", []string{"alice"}},
		{"autolink url", markdown.FormatMarkdown, "https://example.com/@alice and <https://example.com/@bob>", nil},
		{"link title", markdown.FormatMarkdown, `[x](https://example.com "@alice")`, nil},
		{"image destination", markdown.FormatMarkdown, "![x](https://example.com/@alice.png)", nil},
		{"raw html", markdown.FormatMarkdown, `<span title="@alice">x</span> @bob`, []string{"bob"}},
		{"html block", markdown.FormatMarkdown, "<div>\n@alice\n</div>\n\n@bob", []string{"bob"}},
		{"emphasis", markdown.FormatMarkdown, "**@alice** and *@bob*", []string{"alice", "bob"}},
		{"heading", markdown.FormatMarkdown, "# hello @alice", []string{"alice"}},
		{"list", markdown.FormatMarkdown, "- @alice\n- @bob", []string{"alice", "bob"}},
		{"task list", markdown.FormatMarkdown, "- [ ] ask @alice", []string{"alice"}},
		{"blockquote", markdown.FormatMarkdown, "> @alice said", []string{"alice"}},
		{"table", markdown.FormatMarkdown, "| a |\n|---|\n| @alice |", []string{"alice"}},
		{"strikethrough", markdown.FormatMarkdown, "~~@alice~~", []string{"alice"}},
		{"nested list code", markdown.FormatMarkdown, "- item\n\n  ```\n  @alice\n  ```\n- @bob", []string{"bob"}},
		{"underscore name split by emphasis rules", markdown.FormatMarkdown, "@foo_bar_baz", []string{"foo_bar_baz"}},

		// Text keeps everything: no markdown there.
		{"text keeps code", markdown.FormatText, "`@alice` and ```\n@bob\n```", []string{"alice", "bob"}},
		{"text keeps link destination", markdown.FormatText, "[x](https://example.com/@alice) @bob", []string{"alice", "bob"}},

		// Unknown format.
		{"unknown format", "html", "@alice", nil},
	}
	r := markdown.New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Mentions(tt.format, tt.body)
			if !slices.Equal(got, tt.want) {
				t.Errorf("Mentions(%q, %q) = %q, want %q", tt.format, tt.body, got, tt.want)
			}
		})
	}
}

func TestRendererConcurrent(t *testing.T) {
	r := markdown.New()
	bodies := []struct {
		format markdown.Format
		body   string
	}{
		{markdown.FormatMarkdown, "# Title\n\n*a* [x](https://example.com) @alice\n\n| a |\n|---|\n| 1 |\n\n- [ ] t\n\n```\ncode\n```"},
		{markdown.FormatMarkdown, `<script>alert(1)</script> [x](javascript:alert(1)) ![i](https://e.example/i.png)`},
		{markdown.FormatText, "line one\nline two https://example.com. @bob\n\npara"},
	}
	want := make([]string, len(bodies))
	for i, b := range bodies {
		out, err := r.Render(b.format, b.body)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		want[i] = string(out)
	}
	var wg sync.WaitGroup
	errs := make(chan string, 64*len(bodies))
	for g := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				for i, b := range bodies {
					out, err := r.Render(b.format, b.body)
					if err != nil {
						errs <- fmt.Sprintf("goroutine %d: %v", g, err)
						return
					}
					if string(out) != want[i] {
						errs <- fmt.Sprintf("goroutine %d: body %d rendered differently", g, i)
						return
					}
					if got := r.Mentions(b.format, b.body); len(got) > 1 {
						errs <- fmt.Sprintf("goroutine %d: mentions %q", g, got)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
