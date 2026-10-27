package markdown_test

import (
	"strings"
	"testing"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/clowder/internal/markdown"
)

// link is the HTML of the link of the mention of name, written as label.
func link(name, label string) string {
	return `<a href="/search?q=from%3A` + name + `" class="mention">` + label + `</a>`
}

func TestRenderMentions(t *testing.T) {
	tests := []struct {
		name      string
		format    markdown.Format
		body      string
		mentioned []string
		want      []string // substrings the output must contain
		notWant   []string // substrings it must not contain
	}{
		{
			name: "text: a saved mention is a link", format: markdown.FormatText,
			body: "hi @bob, welcome", mentioned: []string{"bob"},
			want: []string{"<p>hi " + link("bob", "@bob") + ", welcome</p>"},
		},
		{
			name: "markdown: a saved mention is a link", format: markdown.FormatMarkdown,
			body: "hi @bob, welcome", mentioned: []string{"bob"},
			want: []string{"<p>hi " + link("bob", "@bob") + ", welcome</p>"},
		},
		{
			name: "text: only the saved ones", format: markdown.FormatText,
			body: "@bob and @carol", mentioned: []string{"bob"},
			want: []string{link("bob", "@bob"), "and @carol"}, notWant: []string{"from%3Acarol"},
		},
		{
			name: "markdown: only the saved ones", format: markdown.FormatMarkdown,
			body: "@bob and @carol", mentioned: []string{"bob"},
			want: []string{link("bob", "@bob"), "and @carol"}, notWant: []string{"from%3Acarol"},
		},
		{
			name: "text: none saved means no link", format: markdown.FormatText,
			body: "@bob", mentioned: nil,
			want: []string{"<p>@bob</p>"}, notWant: []string{"<a "},
		},
		{
			name: "markdown: none saved means no link", format: markdown.FormatMarkdown,
			body: "@bob", mentioned: nil,
			want: []string{"<p>@bob</p>"}, notWant: []string{"<a "},
		},
		{
			name: "the case of the mention is kept in the text and not in the link", format: markdown.FormatText,
			body: "@Bob", mentioned: []string{"bob"},
			want: []string{link("bob", "@Bob")},
		},
		{
			name: "the saved names may be upper case", format: markdown.FormatMarkdown,
			body: "@bob", mentioned: []string{"BOB"},
			want: []string{link("bob", "@bob")},
		},
		{
			name: "the same mention twice", format: markdown.FormatMarkdown,
			body: "@bob @bob", mentioned: []string{"bob"},
			want: []string{link("bob", "@bob") + " " + link("bob", "@bob")},
		},
		{
			name: "markdown: not in a code span", format: markdown.FormatMarkdown,
			body: "run `@bob` now", mentioned: []string{"bob"},
			want: []string{"<code>@bob</code>"}, notWant: []string{"<a "},
		},
		{
			name: "markdown: not in a fenced block", format: markdown.FormatMarkdown,
			body: "```\n@bob\n```", mentioned: []string{"bob"},
			want: []string{"<code>@bob\n</code>"}, notWant: []string{"<a "},
		},
		{
			name: "markdown: not in an indented block", format: markdown.FormatMarkdown,
			body: "text\n\n    @bob\n", mentioned: []string{"bob"},
			want: []string{"<code>@bob\n</code>"}, notWant: []string{"from%3Abob"},
		},
		{
			name: "markdown: not in the text of a link", format: markdown.FormatMarkdown,
			body: "[@bob](https://example.com)", mentioned: []string{"bob"},
			want: []string{`>@bob</a>`}, notWant: []string{"from%3Abob"},
		},
		{
			name: "markdown: not in an autolink", format: markdown.FormatMarkdown,
			body: "mail <bob@bob.example.com> now", mentioned: []string{"bob"},
			notWant: []string{"from%3Abob"},
		},
		{
			name: "markdown: not in raw html", format: markdown.FormatMarkdown,
			body: `<b title="@bob">x</b>`, mentioned: []string{"bob"},
			notWant: []string{"from%3Abob"},
		},
		{
			name: "markdown: a backslash cancels it", format: markdown.FormatMarkdown,
			body: `\@bob`, mentioned: []string{"bob"},
			want: []string{"<p>@bob</p>"}, notWant: []string{"<a "},
		},
		{
			name: "markdown: inside emphasis, a heading and a list", format: markdown.FormatMarkdown,
			body: "**@bob**\n\n# hi @bob\n\n- @bob\n", mentioned: []string{"bob"},
			want: []string{"<strong>" + link("bob", "@bob") + "</strong>", "<h3>hi " + link("bob", "@bob") + "</h3>", "<li>" + link("bob", "@bob") + "</li>"},
		},
		{
			name: "markdown: in a table cell", format: markdown.FormatMarkdown,
			body: "| a |\n|---|\n| @bob |", mentioned: []string{"bob"},
			want: []string{"<td>" + link("bob", "@bob") + "</td>"},
		},
		{
			name: "markdown: a name with an underscore is one mention", format: markdown.FormatMarkdown,
			body: "hi @bob_smith!", mentioned: []string{"bob_smith"},
			want: []string{"hi " + link("bob_smith", "@bob_smith") + "!"},
		},
		{
			name: "markdown: a name with a dash", format: markdown.FormatMarkdown,
			body: "hi @bob-x there", mentioned: []string{"bob-x"},
			want: []string{"hi " + link("bob-x", "@bob-x") + " there"},
		},
		{
			name: "markdown: at the end of a line keeps the line break", format: markdown.FormatMarkdown,
			body: "hi @bob\nnext line", mentioned: []string{"bob"},
			want: []string{link("bob", "@bob") + "\nnext line"},
		},
		{
			name: "markdown: a hard line break after it", format: markdown.FormatMarkdown,
			body: "hi @bob  \nnext", mentioned: []string{"bob"},
			want: []string{link("bob", "@bob") + "<br>\nnext"},
		},
		{
			name: "markdown: an email address is no mention", format: markdown.FormatMarkdown,
			body: "write to a@bob.example.com", mentioned: []string{"bob"},
			notWant: []string{"from%3Abob"},
		},
		{
			name: "text: an email address is no mention", format: markdown.FormatText,
			body: "write to a@bob.example.com", mentioned: []string{"bob"},
			notWant: []string{"from%3Abob"},
		},
		{
			name: "text: not inside a URL", format: markdown.FormatText,
			body: "see https://example.com/@bob and @bob", mentioned: []string{"bob"},
			want: []string{`>https://example.com/@bob</a> and ` + link("bob", "@bob")},
		},
		{
			name: "text: several lines", format: markdown.FormatText,
			body: "@bob\n@carol", mentioned: []string{"bob", "carol"},
			want: []string{link("bob", "@bob") + "<br>\n" + link("carol", "@carol")},
		},
		{
			name: "text: markup is escaped around a mention", format: markdown.FormatText,
			body: "<b>@bob</b> & co", mentioned: []string{"bob"},
			want: []string{"&lt;b&gt;" + link("bob", "@bob") + "&lt;/b&gt; &amp; co"}, notWant: []string{"<b>"},
		},
		{
			name: "markdown: raw html next to a mention stays omitted", format: markdown.FormatMarkdown,
			body: "x <script>alert(1)</script> @bob", mentioned: []string{"bob"},
			want: []string{link("bob", "@bob")}, notWant: []string{"<script"},
		},
		{
			name: "markdown: a name too short is no mention", format: markdown.FormatMarkdown,
			body: "@bo", mentioned: []string{"bo"},
			want: []string{"<p>@bo</p>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := markdown.New().RenderMentions(tt.format, tt.body, tt.mentioned)
			if err != nil {
				t.Fatalf("RenderMentions: %+v", err)
			}
			got := string(out)
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

// TestRenderMentionsWithNoneEqualsRender checks that a body with no saved
// mention renders as Render does.
func TestRenderMentionsWithNoneEqualsRender(t *testing.T) {
	bodies := []string{"@bob **x**\n\n```\n@bob\n```", "text @bob and https://example.com/@bob"}
	for _, f := range []markdown.Format{markdown.FormatText, markdown.FormatMarkdown} {
		for _, body := range bodies {
			r := markdown.New()
			want, err := r.Render(f, body)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			got, err := r.RenderMentions(f, body, []string{})
			if err != nil || got != want {
				t.Errorf("%s %q: RenderMentions = %q, %v; want %q", f, body, got, err, want)
			}
		}
	}
}

func TestRenderMentionsUnknownFormat(t *testing.T) {
	_, err := markdown.New().RenderMentions("html", "@bob", []string{"bob"})
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if !appErr.HasRoute("Renderer.RenderMentions.FormatUnknown") || appErr.Tag != markdown.TagFormatUnknown {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

// TestRenderMentionsMatchMentions checks that every mention that Mentions
// finds is linked when all of them are passed back.
func TestRenderMentionsMatchMentions(t *testing.T) {
	bodies := []string{
		"hi @bob_smith and @alice-w, see @carol.\n@dave",
		"*a* @bob **@carol** _@dave_ `@erin`",
		"- @bob\n- [ ] @carol\n\n| x |\n|---|\n| @dave |",
	}
	r := markdown.New()
	for _, f := range []markdown.Format{markdown.FormatText, markdown.FormatMarkdown} {
		for _, body := range bodies {
			names := r.Mentions(f, body)
			out, err := r.RenderMentions(f, body, names)
			if err != nil {
				t.Fatalf("RenderMentions: %v", err)
			}
			for _, n := range names {
				if !strings.Contains(string(out), `href="/search?q=from%3A`+n+`"`) {
					t.Errorf("%s %q: no link for %q in %s", f, body, n, out)
				}
			}
		}
	}
}
