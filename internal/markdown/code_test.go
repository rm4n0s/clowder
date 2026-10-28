package markdown_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/markdown"
)

// fence returns a fenced code block with the info string info and the body
// code (which ends in a newline unless it is empty).
func fence(info, code string) string {
	return "```" + info + "\n" + code + "```\n"
}

// goSnippet is a realistic piece of Go code of 11 lines.
const goSnippet = `package main

import "fmt"

// greet prints a greeting for every name.
func greet(names []string) {
	for i, n := range names {
		fmt.Printf("%d: hello, %s\n", i, n) // say it
	}
}

`

// goCode returns Go code of about size bytes in at most maxLines lines.
func goCode(size, maxLines int) string {
	var sb strings.Builder
	lines := 0
	for sb.Len()+len(goSnippet) <= size && lines+strings.Count(goSnippet, "\n") <= maxLines {
		sb.WriteString(goSnippet)
		lines += strings.Count(goSnippet, "\n")
	}
	return sb.String()
}

var (
	reHLSpan  = regexp.MustCompile(`<span class="hl-[a-z0-9]+">`)
	reStyle   = regexp.MustCompile(`\sstyle\s*=`)
	rePreCode = regexp.MustCompile(`^<pre class="code"( data-lang="[^"]*")?><code>`)
)

func TestRenderCodeBlocks(t *testing.T) {
	lines1000 := strings.Repeat("x := 1\n", 1000)
	lines1001 := strings.Repeat("x := 1\n", 1001)
	// 512 lines of 64 bytes are exactly 32 KiB.
	bytes32K := strings.Repeat("// "+strings.Repeat("a", 60)+"\n", 512)
	bytes32K1 := bytes32K[:len(bytes32K)-1] + "a\n"
	if len(bytes32K) != 32768 || len(bytes32K1) != 32769 {
		t.Fatalf("test data: %d and %d bytes", len(bytes32K), len(bytes32K1))
	}

	tests := []struct {
		name     string
		format   markdown.Format
		body     string
		wantLang string // the data-lang; "" for none
		wantHL   bool   // the hl- spans are there
		want     []string
		notWant  []string
	}{
		{
			name: "go", format: markdown.FormatMarkdown, body: fence("go", "func main() { return }\n"),
			wantLang: "go", wantHL: true,
			want: []string{`<span class="hl-kd">func</span>`},
		},
		{
			name: "python", format: markdown.FormatMarkdown, body: fence("python", "def f(x):\n    return x + 1\n"),
			wantLang: "python", wantHL: true, want: []string{`<span class="hl-k">def</span>`},
		},
		{
			name: "sh", format: markdown.FormatMarkdown, body: fence("sh", "echo \"hi\" | grep h\n"),
			wantLang: "sh", wantHL: true, want: []string{`<span class="hl-s2">&#34;hi&#34;</span>`},
		},
		{
			name: "bash", format: markdown.FormatMarkdown, body: fence("bash", "if [ -f x ]; then echo $HOME; fi\n"),
			wantLang: "bash", wantHL: true, want: []string{`<span class="hl-k">if</span>`},
		},
		{
			name: "alias golang", format: markdown.FormatMarkdown, body: fence("golang", "func main() {}\n"),
			wantLang: "golang", wantHL: true,
		},
		{
			name: "alias py", format: markdown.FormatMarkdown, body: fence("py", "def f(): pass\n"),
			wantLang: "py", wantHL: true,
		},
		{
			name: "file extension rs", format: markdown.FormatMarkdown, body: fence("rs", "fn main() { let x = 1; }\n"),
			wantLang: "rs", wantHL: true,
		},
		{
			name: "language in capitals", format: markdown.FormatMarkdown, body: fence("Go", "func main() {}\n"),
			wantLang: "Go", wantHL: true,
		},
		{
			name: "unknown language", format: markdown.FormatMarkdown, body: fence("nosuchlang", "a <b> c\n"),
			wantLang: "nosuchlang", want: []string{"<code>a &lt;b&gt; c\n</code>"},
		},
		{
			name: "no language", format: markdown.FormatMarkdown, body: fence("", "func main() {}\n"),
			want: []string{"<code>func main() {}\n</code>"},
		},
		{
			name: "tilde fence", format: markdown.FormatMarkdown, body: "~~~go\nfunc main() {}\n~~~\n",
			wantLang: "go", wantHL: true,
		},
		{
			name: "info string with attributes", format: markdown.FormatMarkdown,
			body:     fence(`go title="x" {1,2}`, "func main() {}\n"),
			wantLang: "go", wantHL: true, notWant: []string{"title", "{1,2}"},
		},
		{
			name: "info string with a space", format: markdown.FormatMarkdown, body: fence("go  extra", "func main() {}\n"),
			wantLang: "go", wantHL: true,
		},
		{
			name: "info string that breaks the pattern: braces", format: markdown.FormatMarkdown,
			body: fence("{.go}", "func main() {}\n"), notWant: []string{"data-lang"},
		},
		{
			name: "info string that breaks the pattern: punctuation", format: markdown.FormatMarkdown,
			body: fence("go!", "func main() {}\n"), notWant: []string{"data-lang"},
		},
		{
			name: "info string that breaks the pattern: too long", format: markdown.FormatMarkdown,
			body: fence(strings.Repeat("a", 33), "x\n"), notWant: []string{"data-lang"},
		},
		{
			name: "info string of 32 characters", format: markdown.FormatMarkdown,
			body: fence(strings.Repeat("a", 32), "x\n"), wantLang: strings.Repeat("a", 32),
		},
		{
			name: "info string with a quote", format: markdown.FormatMarkdown,
			body: fence(`go"onload="alert(1)`, "x\n"), notWant: []string{"data-lang", "onload"},
		},
		{
			name: "info string with an entity", format: markdown.FormatMarkdown,
			body: fence(`g&#111;`, "func main() {}\n"), notWant: []string{"data-lang"},
		},
		{
			name: "1000 lines", format: markdown.FormatMarkdown, body: fence("go", lines1000),
			wantLang: "go", wantHL: true,
		},
		{
			name: "1001 lines", format: markdown.FormatMarkdown, body: fence("go", lines1001),
			wantLang: "go", want: []string{"x := 1\n"},
		},
		{
			name: "exactly 32 KiB", format: markdown.FormatMarkdown, body: fence("go", bytes32K),
			wantLang: "go", wantHL: true,
		},
		{
			name: "over 32 KiB", format: markdown.FormatMarkdown, body: fence("go", bytes32K1), wantLang: "go",
		},
		{
			name: "inline code is untouched", format: markdown.FormatMarkdown, body: "use `fmt.Println(x)` here",
			want: []string{"<code>fmt.Println(x)</code>"}, notWant: []string{"<pre", "hl-"},
		},
		{
			name: "indented code is untouched", format: markdown.FormatMarkdown, body: "    func main() {}\n",
			want: []string{"<pre><code>func main() {}\n</code></pre>"}, notWant: []string{"hl-", "data-lang"},
		},
		{
			name: "text posts are untouched", format: markdown.FormatText, body: fence("go", "func main() {}\n"),
			want: []string{"```go"}, notWant: []string{"<pre", "hl-", "data-lang"},
		},
		{
			name: "mentions stay text in code", format: markdown.FormatMarkdown, body: fence("go", "// @alice\n"),
			wantLang: "go", wantHL: true, notWant: []string{`class="mention"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, tt.format, tt.body)
			if reStyle.MatchString(got) {
				t.Errorf("output has a style attribute:\n%.600s", got)
			}
			if tt.format == markdown.FormatMarkdown && !strings.Contains(tt.name, "indented") &&
				!strings.Contains(tt.name, "inline") {
				m := rePreCode.FindStringSubmatch(got)
				if m == nil {
					t.Fatalf("output does not start with the code block markup:\n%.600s", got)
				}
				if has := m[1] != ""; has != (tt.wantLang != "") {
					t.Errorf("data-lang present = %v, want lang %q:\n%.300s", has, tt.wantLang, got)
				}
				if tt.wantLang != "" && !strings.HasPrefix(got, fmt.Sprintf(`<pre class="code" data-lang=%q><code>`, tt.wantLang)) {
					t.Errorf("data-lang is not %q:\n%.300s", tt.wantLang, got)
				}
				if !strings.HasSuffix(got, "</code></pre>\n") {
					t.Errorf("output does not end with the block:\n%.100s", got[max(0, len(got)-100):])
				}
			}
			if hl := reHLSpan.MatchString(got); hl != tt.wantHL {
				t.Errorf("hl- spans present = %v, want %v:\n%.600s", hl, tt.wantHL, got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("output lacks %q:\n%.600s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("output has %q:\n%.600s", w, got)
				}
			}
			checkNoExecutableHTML(t, got)
		})
	}
}

// TestRenderCodeKeepsTheCode checks that the text of a block is the code, as
// written, whatever the tokens are.
func TestRenderCodeKeepsTheCode(t *testing.T) {
	reTags := regexp.MustCompile(`<[^>]*>`)
	unescape := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&", "&#34;", `"`, "&#39;", "'")
	tests := []struct {
		name, lang, code string
	}{
		{"go", "go", goSnippet},
		{"python", "python", "def f(a, b=\"x\"):\n    return a < b and 'y' & 1\n"},
		{"html", "html", "<div class=\"a\" onclick='x()'>&amp; text</div>\n"},
		{"shell", "sh", "cat <<EOF\n$HOME > /dev/null && echo 'x'\nEOF\n"},
		{"tabs", "go", "func f() {\n\treturn\n}\n"},
		{"no trailing newline lexer", "json", "{\"a\": [1, 2, null]}\n"},
		{"unknown", "nosuchlang", "<b>&</b>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, markdown.FormatMarkdown, fence(tt.lang, tt.code))
			inner := strings.TrimSuffix(got, "</code></pre>\n")
			inner = rePreCode.ReplaceAllString(inner, "")
			text := unescape.Replace(reTags.ReplaceAllString(inner, ""))
			if text != tt.code {
				t.Errorf("text of the block = %q, want %q", text, tt.code)
			}
		})
	}
}

// codeSafetyBodies are bodies with a hostile code block, for the safety table
// and the fuzz corpus.
var codeSafetyBodies = []struct {
	name, info, code string
}{
	{"script tag", "html", "<script>alert(1)</script>\n"},
	{"script tag in go", "go", "x := \"<script>alert(1)</script>\"\n"},
	{"script tag without language", "", "<script>alert(1)</script>\n"},
	{"closing the block", "html", "</code></pre><script>alert(1)</script>\n"},
	{"closing the block in js", "js", "</code></pre><script>alert(1)</script>\n"},
	{"closing the block without language", "", "</code></pre><script>alert(1)</script>\n"},
	{"closing the block in markdown", "markdown", "</code></pre><script>alert(1)</script>\n"},
	{"javascript link", "html", "<a href=\"javascript:alert(1)\">x</a>\n"},
	{"javascript link in markdown", "markdown", "[x](javascript:alert(1))\n"},
	{"javascript text", "js", "location = 'javascript:alert(1)'\n"},
	{"onerror", "html", "<img src=x onerror=alert(1)>\n"},
	{"onerror in js", "js", "img.onerror=alert(1)\n"},
	{"onerror without language", "", "<img src=x onerror=alert(1)>\n"},
	{"quote in the info string", `go"onload="alert(1)`, "x := 1\n"},
	{"tag in the info string", `<script>alert(1)</script>`, "x := 1\n"},
	{"quote and tag in the info string", `html"><script>alert(1)</script>`, "x\n"},
	{"entity in the info string", `&quot;onload=&quot;alert(1)`, "x\n"},
	{"svg", "xml", "<svg onload=alert(1)>\n"},
	{"style", "css", "</style><script>alert(1)</script>\n"},
	{"comment", "html", "<!-- --><script>alert(1)</script>\n"},
	{"null byte", "go", "x := \"<scr\x00ipt>alert(1)</scr\x00ipt>\"\n"},
	{"invalid utf-8", "go", "x := \"\xff\xfe<script>\"\n"},
}

func TestRenderCodeInjection(t *testing.T) {
	for _, tt := range codeSafetyBodies {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, markdown.FormatMarkdown, fence(tt.info, tt.code))
			checkNoExecutableHTML(t, got)
			if strings.Contains(strings.ToLower(got), "<script") {
				t.Errorf("output has a script tag:\n%s", got)
			}
		})
	}
}

// FuzzRenderCode checks that no code block, whatever its language and its
// text, makes a script tag or an event attribute. Without -fuzz it runs the
// seed corpus.
func FuzzRenderCode(f *testing.F) {
	for _, tt := range codeSafetyBodies {
		f.Add(tt.info, tt.code)
	}
	f.Add("go", goSnippet)
	f.Add("python", "def f():\n    pass\n")
	f.Add("sh", "echo $(date) `uname`\n")
	r := markdown.New()
	f.Fuzz(func(t *testing.T, info, code string) {
		info = strings.NewReplacer("\n", " ", "\r", " ", "`", "'").Replace(info)
		out, err := r.Render(markdown.FormatMarkdown, "```"+info+"\n"+code+"\n```\n")
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		got := string(out)
		if strings.Contains(strings.ToLower(got), "<script") {
			t.Errorf("output has a script tag:\n%s", got)
		}
		checkNoExecutableHTML(t, got)
	})
}

// BenchmarkRenderCode32KiB renders a block of 32 KiB that is not in the
// cache (each round has a new renderer).
func BenchmarkRenderCode32KiB(b *testing.B) {
	code := goCode(32<<10, 1000)
	body := fence("go", code)
	b.SetBytes(int64(len(code)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := markdown.New().Render(markdown.FormatMarkdown, body); err != nil {
			b.Fatal(err)
		}
	}
}

// feedBodies returns the bodies of a feed page: 30 posts that each have a
// different 2 KiB Go block.
func feedBodies() []string {
	bodies := make([]string, 30)
	for i := range bodies {
		code := strings.Replace(goCode(2<<10, 1000), "package main", fmt.Sprintf("package post%d", i), 1)
		bodies[i] = "Here is the change:\n\n" + fence("go", code) + "\nWhat do you think?\n"
	}
	return bodies
}

// BenchmarkRenderCodeFeed renders a feed page of 30 posts that each have a
// 2 KiB Go block, none of them in the cache (each round has a new renderer).
func BenchmarkRenderCodeFeed(b *testing.B) {
	bodies := feedBodies()
	b.ReportAllocs()
	for b.Loop() {
		r := markdown.New()
		for _, body := range bodies {
			if _, err := r.Render(markdown.FormatMarkdown, body); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkRenderCodeFeedWarm is the same feed page rendered a second time,
// with its blocks in the cache of the renderer.
func BenchmarkRenderCodeFeedWarm(b *testing.B) {
	bodies := feedBodies()
	r := markdown.New()
	for _, body := range bodies {
		if _, err := r.Render(markdown.FormatMarkdown, body); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, body := range bodies {
			if _, err := r.Render(markdown.FormatMarkdown, body); err != nil {
				b.Fatal(err)
			}
		}
	}
}
