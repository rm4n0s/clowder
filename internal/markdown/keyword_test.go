package markdown_test

import (
	"reflect"
	"testing"

	"github.com/rm4n0s/clowder/internal/markdown"
)

func TestPlainText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		format markdown.Format
		body   string
		want   string
	}{
		{"text is the body as it is", markdown.FormatText, "Deploy `now` @bob\n\nhttps://x.org <b>", "Deploy `now` @bob\n\nhttps://x.org <b>"},
		{"text: empty", markdown.FormatText, "", ""},
		{"markdown: empty", markdown.FormatMarkdown, "", ""},
		{"unknown format", markdown.Format("html"), "<p>deploy</p>", ""},
		{"no format", markdown.Format(""), "deploy", ""},
		{"markdown: plain words", markdown.FormatMarkdown, "deploy the feeder", "deploy the feeder"},
		{"markdown: a code span is left out", markdown.FormatMarkdown, "run `deploy` now", "run   now"},
		{"markdown: a fenced code block is left out", markdown.FormatMarkdown, "before\n\n```\ndeploy\n```\n\nafter", "before\nafter"},
		{"markdown: an indented code block is left out", markdown.FormatMarkdown, "before\n\n    deploy\n\nafter", "before\nafter"},
		{"markdown: link text stays, the destination goes", markdown.FormatMarkdown, "see [the guide](https://example.com/deploy) now", "see the guide now"},
		{"markdown: an autolink is left out", markdown.FormatMarkdown, "see <https://example.com/deploy> now", "see   now"},
		{"markdown: a bare URL is left out", markdown.FormatMarkdown, "see https://example.com/deploy now", "see   now"},
		{"markdown: inline raw html is left out", markdown.FormatMarkdown, "a <span class=\"deploy\">b</span> c", "a  b  c"},
		{"markdown: an html block is left out", markdown.FormatMarkdown, "before\n\n<div>deploy</div>\n\nafter", "before\nafter"},
		{"markdown: an image is left out", markdown.FormatMarkdown, "look ![deploy](https://example.com/a.png) here", "look   here"},
		{"markdown: emphasis text stays", markdown.FormatMarkdown, "**deploy** and *feeder* and ~~old~~", "deploy and feeder and old"},
		{"markdown: blocks are separated by a line break", markdown.FormatMarkdown, "one\n\ntwo\n\nthree", "one\ntwo\nthree"},
		{"markdown: a line break inside a paragraph stays", markdown.FormatMarkdown, "one\ntwo", "one\ntwo"},
		{"markdown: a heading and a paragraph", markdown.FormatMarkdown, "# Deploy plan\n\nstart soon", "Deploy plan\nstart soon"},
		{"markdown: list items", markdown.FormatMarkdown, "- first\n- second", "first\nsecond"},
		{"markdown: a quote", markdown.FormatMarkdown, "> quoted deploy\n\nreply", "quoted deploy\nreply"},
		{"markdown: only code", markdown.FormatMarkdown, "```\ndeploy\n```", ""},
		{"markdown: a mention is plain text", markdown.FormatMarkdown, "hi @bob", "hi @bob"},
	}
	r := markdown.New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := r.PlainText(tt.format, tt.body); got != tt.want {
				t.Errorf("PlainText(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestKeywordMatcher(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		text    string
		keyword string
		want    bool
	}{
		{"the same word", "deploy", "deploy", true},
		{"at the start, with punctuation after", "Deploy now!", "deploy", true},
		{"ignoring case in the text", "DEPLOY NOW", "deploy", true},
		{"ignoring case in the keyword", "deploy now", "DePloy", true},
		{"after a hyphen", "the deploy-script", "deploy", true},
		{"before a hyphen", "re-deploy it", "deploy", true},
		{"in parentheses", "(deploy)", "deploy", true},
		{"at the end", "please deploy", "deploy", true},
		{"after a newline", "x\ndeploy\ny", "deploy", true},
		{"after a dot and a comma", "now.deploy,later", "deploy", true},
		{"inside a longer word at the start", "deployed", "deploy", false},
		{"inside a longer word at the end", "redeploy", "deploy", false},
		{"inside a longer word on both sides", "redeployed", "deploy", false},
		{"a plural", "deploys", "deploy", false},
		{"with an underscore after", "deploy_x", "deploy", false},
		{"with an underscore before", "x_deploy", "deploy", false},
		{"with a digit after", "deploy2", "deploy", false},
		{"with a digit before", "2deploy", "deploy", false},
		{"a partial match before a real one", "redeploy deploy", "deploy", true},
		{"several partial matches before a real one", "deployed redeploy deploys deploy.", "deploy", true},
		{"only partial matches", "deployed redeploy deploys", "deploy", false},
		{"a phrase", "see the feeder schedule today", "feeder schedule", true},
		{"a phrase over several spaces", "the feeder    schedule is", "feeder schedule", true},
		{"a phrase over a newline", "the feeder\nschedule is", "feeder schedule", true},
		{"a phrase over a tab and a carriage return", "the feeder\t\r\nschedule is", "feeder schedule", true},
		{"a phrase with spaces in the keyword", "the feeder schedule is", "feeder   schedule", true},
		{"a phrase in another case", "The FEEDER Schedule", "feeder schedule", true},
		{"a phrase as part of a longer word", "feeder schedules", "feeder schedule", false},
		{"a phrase with only one of its words", "the feeder is", "feeder schedule", false},
		{"a keyword with spaces around", "deploy now", "  deploy  ", true},
		{"a keyword with spaces around and no match", "redeployed", "  deploy  ", false},
		{"a text with spaces around", "   deploy   ", "deploy", true},
		{"an empty keyword", "deploy", "", false},
		{"a blank keyword", "deploy", "   \t ", false},
		{"an empty text", "", "deploy", false},
		{"a blank text", "  \n ", "deploy", false},
		{"both empty", "", "", false},
		{"Greek", "ο Μάνος είπε", "Μάνος", true},
		{"Greek in lower case", "ο Μάνος είπε", "μάνος", true},
		{"Greek in capitals", "Ο ΝΕΡΌ ΕΊΠΕ", "νερό", true},
		{"Greek keyword in capitals", "ο νερό είπε", "ΝΕΡΌ", true},
		{"Greek accents matter", "ο Μανος είπε", "Μάνος", false},
		{"Greek inside a longer word", "ο Μάνοςκαι είπε", "Μάνος", false},
		{"Greek with a letter before", "ο ΑΜάνος είπε", "Μάνος", false},
		{"Greek with a Latin letter after", "ο Μάνοςx", "Μάνος", false},
		{"a keyword ending with a symbol needs no boundary after it", "i like c++ a lot", "c++", true},
		{"a keyword ending with a symbol, glued to a word", "c++ndk", "c++", true},
		{"a keyword starting with a symbol needs no boundary before it", "ping@bob now", "@bob", true},
		{"a keyword starting with a symbol, a word after", "@bobby", "@bob", false},
		{"a keyword starting and ending with symbols", "x(#1)y", "(#1)", true},
		{"a keyword with a symbol inside", "use c++ or c#", "c#", true},
		{"@room is no special name", "hi @room all", "@room", true},
		{"@room as a keyword needs a boundary after", "hi @rooms all", "@room", false},
		{"room as a word", "hi @room all", "room", true},
		{"a repeated word", "deploy deploy", "deploy", true},
		{"a single character keyword", "a b c", "b", true},
		{"a single character inside a word", "abc", "b", false},
		{"a long keyword", "x", "deploy", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := markdown.NewKeywordMatcher(tt.text)
			if got := m.Matches(tt.keyword); got != tt.want {
				t.Errorf("Matches(%q in %q) = %v, want %v", tt.keyword, tt.text, got, tt.want)
			}
		})
	}
}

// TestKeywordMatcherFinalSigma checks that a Greek word in capitals matches a
// keyword written in lower case with a final sigma, and the other way round
// (strings.ToLower never gives the final sigma, so both are folded to the
// plain one).
func TestKeywordMatcherFinalSigma(t *testing.T) {
	tests := []struct {
		name, text, keyword string
	}{
		{"capitals in the text", "Ο ΜΆΝΟΣ ΕΊΠΕ", "μάνος"},
		{"capitals in the keyword", "ο μάνος είπε", "ΜΆΝΟΣ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !markdown.NewKeywordMatcher(tt.text).Matches(tt.keyword) {
				t.Errorf("Matches(%q in %q) = false, want true", tt.keyword, tt.text)
			}
		})
	}
}

func TestKeywordMatcherAsksManyKeywords(t *testing.T) {
	t.Parallel()
	m := markdown.NewKeywordMatcher("We deploy the Feeder   Schedule tomorrow")
	keywords := []string{"nothing", "feeder schedule", "tomorrow", "deploying", "", "schedule"}
	var got []string
	for _, k := range keywords {
		if m.Matches(k) {
			got = append(got, k)
		}
	}
	want := []string{"feeder schedule", "tomorrow", "schedule"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("matched %v, want %v", got, want)
	}
	var zero markdown.KeywordMatcher
	if zero.Matches("deploy") {
		t.Error("the zero matcher matched")
	}
}

func TestKeywordMatcherOnPlainText(t *testing.T) {
	t.Parallel()
	r := markdown.New()
	tests := []struct {
		name    string
		format  markdown.Format
		body    string
		keyword string
		want    bool
	}{
		{"markdown: a word in the prose", markdown.FormatMarkdown, "Please **deploy** it", "deploy", true},
		{"markdown: a word in a code span", markdown.FormatMarkdown, "run `deploy` now", "deploy", false},
		{"markdown: a word in a code block", markdown.FormatMarkdown, "```\ndeploy\n```", "deploy", false},
		{"markdown: a word in a link destination", markdown.FormatMarkdown, "[guide](https://example.com/deploy)", "deploy", false},
		{"markdown: a word in the link text", markdown.FormatMarkdown, "[deploy guide](https://example.com/x)", "deploy", true},
		{"markdown: a word in a bare URL", markdown.FormatMarkdown, "see https://example.com/deploy", "deploy", false},
		{"markdown: a phrase across a soft break", markdown.FormatMarkdown, "the feeder\nschedule", "feeder schedule", true},
		{"markdown: a phrase across two paragraphs", markdown.FormatMarkdown, "the feeder\n\nschedule", "feeder schedule", true},
		{"markdown: a word on both sides of a code span", markdown.FormatMarkdown, "re`x`deploy", "redeploy", false},
		{"text: a word in a code span is still a word", markdown.FormatText, "run `deploy` now", "deploy", true},
		{"text: a phrase over lines", markdown.FormatText, "feeder\nschedule", "feeder schedule", true},
		{"an unknown format has no words", markdown.Format("html"), "deploy", "deploy", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := markdown.NewKeywordMatcher(r.PlainText(tt.format, tt.body))
			if got := m.Matches(tt.keyword); got != tt.want {
				t.Errorf("Matches(%q in %q) = %v, want %v", tt.keyword, tt.body, got, tt.want)
			}
		})
	}
}

func TestMentionsOfGroupNames(t *testing.T) {
	t.Parallel()
	r := markdown.New()
	tests := []struct {
		name   string
		format markdown.Format
		body   string
		want   []string
	}{
		{"text: the three names", markdown.FormatText, "@room @all @here", []string{"room", "all", "here"}},
		{"markdown: the three names", markdown.FormatMarkdown, "@room @all @here", []string{"room", "all", "here"}},
		{"text: mixed with a username", markdown.FormatText, "hi @bob and @ALL, @here!", []string{"bob", "all", "here"}},
		{"markdown: mixed with a username", markdown.FormatMarkdown, "hi @bob and @ALL, @here!", []string{"bob", "all", "here"}},
		{"markdown: not in a code span", markdown.FormatMarkdown, "`@room` and @all", []string{"all"}},
		{"markdown: not in a fenced code block", markdown.FormatMarkdown, "```\n@room @here\n```\n\n@all", []string{"all"}},
		{"markdown: not in an indented code block", markdown.FormatMarkdown, "    @room\n\n@here", []string{"here"}},
		{"markdown: not after a backslash", markdown.FormatMarkdown, `\@room and @all`, []string{"all"}},
		{"markdown: not in a link destination", markdown.FormatMarkdown, "[x](https://example.com/@room) @here", []string{"here"}},
		{"markdown: in link text", markdown.FormatMarkdown, "[@room](https://example.com/x)", []string{"room"}},
		{"text: the same name twice is once", markdown.FormatText, "@room @Room @ROOM", []string{"room"}},
		{"text: in an email address", markdown.FormatText, "write to a@room.example", nil},
		{"text: a longer name", markdown.FormatText, "@rooms @hereby", []string{"rooms", "hereby"}},
		{"markdown: a longer name", markdown.FormatMarkdown, "@rooms @hereby", []string{"rooms", "hereby"}},
		{"unknown format", markdown.Format("html"), "@room", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := r.Mentions(tt.format, tt.body)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Mentions(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
