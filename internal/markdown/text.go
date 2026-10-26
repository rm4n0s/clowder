package markdown

import (
	"html"
	"html/template"
	"net/url"
	"regexp"
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
// breaks become <br>, and http and https URLs become links.
func renderText(body string) template.HTML {
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
			writeLine(&sb, line)
		}
		sb.WriteString("</p>\n")
	}
	return template.HTML(sb.String()) //nolint:gosec // everything is escaped, see the package documentation
}

// writeLine writes one line of a text body, escaped, with its URLs linked.
func writeLine(sb *strings.Builder, line string) {
	last := 0
	for _, loc := range urlPattern.FindAllStringIndex(line, -1) {
		raw := trimURL(line[loc[0]:loc[1]])
		if !validURL(raw) {
			continue
		}
		end := loc[0] + len(raw)
		sb.WriteString(html.EscapeString(line[last:loc[0]]))
		esc := html.EscapeString(raw)
		sb.WriteString(`<a href="` + esc + `" rel="` + linkRel + `" target="_blank">` + esc + `</a>`)
		last = end
	}
	sb.WriteString(html.EscapeString(line[last:]))
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

// scanMentions finds the @username mentions in s and returns the list, with
// the ones of set (if any) first. With skipEscaped a backslash before the @
// (markdown's escape) cancels the mention.
func scanMentions(s string, skipEscaped bool, set *mentionSet) []string {
	if set == nil {
		set = &mentionSet{seen: map[string]bool{}}
	}
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
			name := strings.ToLower(s[i+1 : j])
			if !set.seen[name] {
				set.seen[name] = true
				set.list = append(set.list, name)
			}
		}
		i = j - 1
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
