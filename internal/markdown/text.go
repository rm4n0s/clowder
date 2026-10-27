package markdown

import (
	"html"
	"html/template"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// urlPattern finds the start of an http or https URL in a text body; the end
// is cut at white space and at characters that cannot be in a URL, and the
// trailing punctuation is trimmed by trimURL.
var urlPattern = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"` + "`" + `]+`)

// paragraphBreak separates the paragraphs of a text body: a blank line.
var paragraphBreak = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)*`)

// renderText escapes a text body: blank lines make paragraphs, single line
// breaks become <br>, and http and https URLs become links. The usernames in
// mentioned (lower case) become links to their search when written as
// @username outside a URL.
func renderText(body string, mentioned map[string]bool) template.HTML {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	var sb strings.Builder
	for _, para := range paragraphBreak.Split(body, -1) {
		para = strings.Trim(para, "\n")
		if strings.TrimSpace(para) == "" {
			continue
		}
		sb.WriteString("<p>")
		for i, line := range strings.Split(para, "\n") {
			if i > 0 {
				sb.WriteString("<br>\n")
			}
			writeLine(&sb, line, mentioned)
		}
		sb.WriteString("</p>\n")
	}
	return template.HTML(sb.String()) //nolint:gosec // everything is escaped, see the package documentation
}

// region is a part of a line that is written as HTML of its own: a URL or a
// mention.
type region struct {
	start, end int
	html       string
}

// writeLine writes one line of a text body, escaped, with its URLs and its
// mentions of mentioned linked.
func writeLine(sb *strings.Builder, line string, mentioned map[string]bool) {
	var regions []region
	for _, loc := range urlPattern.FindAllStringIndex(line, -1) {
		raw := trimURL(line[loc[0]:loc[1]])
		if !validURL(raw) {
			continue
		}
		esc := html.EscapeString(raw)
		regions = append(regions, region{
			start: loc[0], end: loc[0] + len(raw),
			html: `<a href="` + esc + `" rel="` + linkRel + `" target="_blank">` + esc + `</a>`,
		})
	}
	if len(mentioned) > 0 {
		urls := len(regions)
		for _, m := range findMentions(line, false) {
			if !mentioned[m.name] || overlaps(regions[:urls], m.start, m.end) {
				continue
			}
			regions = append(regions, region{start: m.start, end: m.end, html: mentionHTML(m.name, line[m.start:m.end])})
		}
		sort.Slice(regions, func(i, j int) bool { return regions[i].start < regions[j].start })
	}
	last := 0
	for _, r := range regions {
		sb.WriteString(html.EscapeString(line[last:r.start]))
		sb.WriteString(r.html)
		last = r.end
	}
	sb.WriteString(html.EscapeString(line[last:]))
}

// overlaps reports whether [start, end) meets one of the regions.
func overlaps(regions []region, start, end int) bool {
	for _, r := range regions {
		if start < r.end && r.start < end {
			return true
		}
	}
	return false
}

// mentionHTML is the link of a saved mention: there are no profile pages, so
// it goes to the search for the user's posts (SPEC D30). name is a username,
// which has only a-z, 0-9, "_" and "-"; label is the mention as written
// ("@Bob").
func mentionHTML(name, label string) string {
	return `<a href="/search?q=from%3A` + url.QueryEscape(name) + `" class="mention">` + html.EscapeString(label) + `</a>`
}

// trimURL removes the punctuation at the end of a matched URL that belongs to
// the sentence around it: . , ; : ! ? quotes, and a closing bracket that has no
// opening one inside the URL.
func trimURL(u string) string {
	for u != "" {
		last := u[len(u)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?', '\'', '*':
			u = u[:len(u)-1]
			continue
		case ')':
			if strings.Count(u, "(") < strings.Count(u, ")") {
				u = u[:len(u)-1]
				continue
			}
		case ']':
			if strings.Count(u, "[") < strings.Count(u, "]") {
				u = u[:len(u)-1]
				continue
			}
		case '}':
			if strings.Count(u, "{") < strings.Count(u, "}") {
				u = u[:len(u)-1]
				continue
			}
		}
		return u
	}
	return u
}

// validURL reports whether u is an http or https URL with a host.
func validURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	return (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

const (
	usernameMin = 3
	usernameMax = 32
)

// mentionSet collects the usernames found so far, without duplicates.
type mentionSet struct {
	seen map[string]bool
	list []string
}

// mentionSpan is a mention found in a string: s[start:end] is "@" and the
// username, name is the username in lower case.
type mentionSpan struct {
	start, end int
	name       string
}

// findMentions finds the @username mentions in s, in order. With skipEscaped a
// backslash before the @ (markdown's escape) cancels the mention.
func findMentions(s string, skipEscaped bool) []mentionSpan {
	var spans []mentionSpan
	for i := 0; i < len(s); i++ {
		if s[i] != '@' {
			continue
		}
		if i > 0 {
			prev, _ := utf8.DecodeLastRuneInString(s[:i])
			if isWordRune(prev) || (skipEscaped && prev == '\\') {
				continue
			}
		}
		j := i + 1
		for j < len(s) && isUsernameByte(s[j]) {
			j++
		}
		if n := j - (i + 1); n >= usernameMin && n <= usernameMax {
			spans = append(spans, mentionSpan{start: i, end: j, name: strings.ToLower(s[i+1 : j])})
		}
		i = j - 1
	}
	return spans
}

// scanMentions finds the @username mentions in s and returns the list, with
// the ones of set (if any) first. With skipEscaped a backslash before the @
// (markdown's escape) cancels the mention.
func scanMentions(s string, skipEscaped bool, set *mentionSet) []string {
	if set == nil {
		set = &mentionSet{seen: map[string]bool{}}
	}
	for _, m := range findMentions(s, skipEscaped) {
		if !set.seen[m.name] {
			set.seen[m.name] = true
			set.list = append(set.list, m.name)
		}
	}
	return set.list
}

// isWordRune reports whether r is a letter, a digit or an underscore.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isUsernameByte reports whether b can be in a username (case aside).
func isUsernameByte(b byte) bool {
	return b == '_' || b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
