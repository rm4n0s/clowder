package markdown

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/rm4n0s/errors"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Format is the format of a body. Its values are the same strings the store
// keeps, so a stored format can be converted to it directly.
type Format string

const (
	// FormatText is a body shown as written: escaped, with its line breaks and
	// with http and https URLs made clickable.
	FormatText Format = "text"
	// FormatMarkdown is a body written in GitHub-flavored markdown.
	FormatMarkdown Format = "markdown"
)

// linkRel is the rel attribute of every link in a rendered body.
const linkRel = "nofollow noopener noreferrer"

// headingShift is the number of levels that headings of a markdown body move
// down, so that the body never emits h1 or h2.
const headingShift = 2

// Renderer renders bodies and finds their mentions. Build it once with New and
// pass it around. It is safe for concurrent use.
type Renderer struct {
	md goldmark.Markdown
}

// New returns a Renderer: goldmark with the GFM extensions, in safe mode, with
// the link, image and heading rules of the package documentation.
func New() *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(
			// Priority 100 wins over goldmark's own renderers (1000).
			renderer.WithNodeRenderers(util.Prioritized(&safeNodeRenderer{}, 100)),
		),
	)
	return &Renderer{md: md}
}

// Render returns the HTML of body in the given format. The result is safe to
// insert in a page as it is (see the package documentation). An unknown format
// is the error TagFormatUnknown.
func (r *Renderer) Render(format Format, body string) (template.HTML, error) {
	switch format {
	case FormatMarkdown:
		var buf bytes.Buffer
		if err := r.md.Convert([]byte(body), &buf); err != nil {
			return "", errors.NewErr(tagMarkdownRenderFailed, err)
		}
		return template.HTML(buf.String()), nil //nolint:gosec // goldmark in safe mode, see the package documentation
	case FormatText:
		return renderText(body), nil
	default:
		return "", errors.New(TagFormatUnknown, "unknown body format", "format", string(format))
	}
}

// Mentions returns the usernames mentioned in body as @username, lower-cased,
// without duplicates, in order of appearance. A username has 3 to 32 of
// [a-z0-9_-] and the @ stands at the start or after a character that is not a
// letter, a digit or an underscore (so an e-mail address is no mention). In a
// markdown body, code spans, code blocks, link destinations, autolinks and raw
// HTML are ignored. An unknown format has no mentions.
func (r *Renderer) Mentions(format Format, body string) []string {
	switch format {
	case FormatText:
		return scanMentions(body, false, nil)
	case FormatMarkdown:
		return r.markdownMentions(body)
	default:
		return nil
	}
}

// markdownMentions walks the AST and scans the text of every block that holds
// inline content.
func (r *Renderer) markdownMentions(body string) []string {
	src := []byte(body)
	doc := r.md.Parser().Parse(text.NewReader(src))
	var found []string
	seen := map[string]bool{}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if n.Type() == ast.TypeBlock && n.FirstChild() != nil && n.FirstChild().Type() == ast.TypeInline {
			var sb strings.Builder
			flattenInline(&sb, src, n)
			found = scanMentions(sb.String(), true, &mentionSet{seen: seen, list: found})
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return found
}

// flattenInline writes the readable text of the inline children of n. Nodes
// that carry no prose (code spans, raw HTML, autolinks, images and checkboxes)
// become a space, so they neither hide nor join a mention.
func flattenInline(sb *strings.Builder, src []byte, n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			sb.Write(v.Segment.Value(src))
			if v.SoftLineBreak() || v.HardLineBreak() {
				sb.WriteByte('\n')
			}
		case *ast.String:
			sb.Write(v.Value)
		case *ast.CodeSpan, *ast.RawHTML, *ast.AutoLink, *ast.Image:
			sb.WriteByte(' ')
		default:
			if c.Type() == ast.TypeInline && c.HasChildren() {
				flattenInline(sb, src, c)
			} else {
				sb.WriteByte(' ')
			}
		}
	}
}

// safeNodeRenderer renders links, autolinks, images and headings by the safety
// rules of the package documentation.
type safeNodeRenderer struct{}

// RegisterFuncs implements renderer.NodeRenderer.
func (s *safeNodeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindLink, s.renderLink)
	reg.Register(ast.KindAutoLink, s.renderAutoLink)
	reg.Register(ast.KindImage, s.renderImage)
	reg.Register(ast.KindHeading, s.renderHeading)
}

func (s *safeNodeRenderer) renderHeading(
	w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	level := min(node.(*ast.Heading).Level+headingShift, 6)
	if entering {
		_, _ = w.WriteString("<h")
		_ = w.WriteByte(byte('0' + level))
		_ = w.WriteByte('>')
	} else {
		_, _ = w.WriteString("</h")
		_ = w.WriteByte(byte('0' + level))
		_, _ = w.WriteString(">\n")
	}
	return ast.WalkContinue, nil
}

func (s *safeNodeRenderer) renderLink(
	w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	dest := util.URLEscape(n.Destination, true)
	if !isSafeLinkURL(n.Destination) || !isSafeLinkURL(dest) {
		// The link is dropped; its text stays.
		return ast.WalkContinue, nil
	}
	if !entering {
		_, _ = w.WriteString("</a>")
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<a href="`)
	_, _ = w.Write(util.EscapeHTML(dest))
	_ = w.WriteByte('"')
	if n.Title != nil {
		_, _ = w.WriteString(` title="`)
		html.DefaultWriter.Write(w, n.Title)
		_ = w.WriteByte('"')
	}
	writeLinkAttributes(w)
	_ = w.WriteByte('>')
	return ast.WalkContinue, nil
}

func (s *safeNodeRenderer) renderAutoLink(
	w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.AutoLink)
	url := util.URLEscape(n.URL(source), false)
	if n.AutoLinkType == ast.AutoLinkEmail && !bytes.HasPrefix(bytes.ToLower(url), []byte("mailto:")) {
		url = append([]byte("mailto:"), url...)
	}
	label := util.EscapeHTML(n.Label(source))
	if !isSafeLinkURL(url) {
		_, _ = w.Write(label)
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<a href="`)
	_, _ = w.Write(util.EscapeHTML(url))
	_ = w.WriteByte('"')
	writeLinkAttributes(w)
	_ = w.WriteByte('>')
	_, _ = w.Write(label)
	_, _ = w.WriteString("</a>")
	return ast.WalkContinue, nil
}

func (s *safeNodeRenderer) renderImage(
	w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Image)
	var alt bytes.Buffer
	collectAlt(&alt, source, n)
	dest := util.URLEscape(n.Destination, true)
	if !isSafeImageURL(n.Destination) || !isSafeImageURL(dest) {
		_, _ = w.Write(util.EscapeHTML(alt.Bytes()))
		return ast.WalkSkipChildren, nil
	}
	_, _ = w.WriteString(`<img src="`)
	_, _ = w.Write(util.EscapeHTML(dest))
	_, _ = w.WriteString(`" alt="`)
	_, _ = w.Write(util.EscapeHTML(alt.Bytes()))
	_ = w.WriteByte('"')
	if n.Title != nil {
		_, _ = w.WriteString(` title="`)
		html.DefaultWriter.Write(w, n.Title)
		_ = w.WriteByte('"')
	}
	_, _ = w.WriteString(` loading="lazy">`)
	return ast.WalkSkipChildren, nil
}

// writeLinkAttributes writes the attributes every link has.
func writeLinkAttributes(w util.BufWriter) {
	_, _ = w.WriteString(` rel="` + linkRel + `" target="_blank"`)
}

// collectAlt writes the plain text of the children of n (the alt text of an
// image).
func collectAlt(buf *bytes.Buffer, source []byte, n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			buf.Write(v.Segment.Value(source))
			if v.SoftLineBreak() || v.HardLineBreak() {
				buf.WriteByte(' ')
			}
		case *ast.String:
			buf.Write(v.Value)
		default:
			collectAlt(buf, source, c)
		}
	}
}

// urlScheme returns the lower-cased scheme of a URL as a browser reads it
// (white space and control characters inside are ignored), and whether it has
// one. A colon after a slash, a question mark or a hash is no scheme.
func urlScheme(raw []byte) (string, bool) {
	var sb strings.Builder
	for _, b := range raw {
		if b <= ' ' || b == 0x7f {
			continue
		}
		if b == ':' {
			return strings.ToLower(sb.String()), true
		}
		if b == '/' || b == '?' || b == '#' {
			return "", false
		}
		sb.WriteByte(b)
	}
	return "", false
}

// isSafeLinkURL reports whether a link may point to raw: a relative reference
// or a URL with the scheme http, https or mailto.
func isSafeLinkURL(raw []byte) bool {
	scheme, ok := urlScheme(raw)
	if !ok {
		return true
	}
	switch scheme {
	case "http", "https", "mailto":
		return true
	default:
		return false
	}
}

// isSafeImageURL reports whether an image may load raw: only http and https.
func isSafeImageURL(raw []byte) bool {
	scheme, ok := urlScheme(raw)
	return ok && (scheme == "http" || scheme == "https")
}

const (
	// TagFormatUnknown marks a body format that is neither FormatText nor
	// FormatMarkdown.
	TagFormatUnknown = "FormatUnknown"
	// tagMarkdownRenderFailed marks a failure of goldmark while converting a
	// markdown body.
	tagMarkdownRenderFailed = "MarkdownRenderFailed"
)
