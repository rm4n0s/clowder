// Package markdown renders the body of a post or a comment to HTML and extracts
// the mentions from it. It is pure: it resolves no users and does no I/O.
//
// This package is the only place where HTML made from user content is
// produced, and the web layer marks its output as trusted (template.HTML)
// because of the rules below (SPEC D9; CLAUDE.md "Don't"). Anyone who changes
// the package must keep every one of them.
//
// Markdown bodies are rendered by goldmark with the GitHub-flavored extensions
// (tables, strikethrough, autolinks, task lists), in safe mode:
//   - html.WithUnsafe is never used: raw HTML, block or inline, is replaced by an
//     HTML comment that says it was omitted, so a body cannot add a tag, an
//     attribute, a script or a style.
//   - Links are emitted only for the schemes http, https and mailto, and for
//     relative references; any other scheme (javascript:, vbscript:, data:,
//     file:, ftp:, and the like, in any letter case, with or without hidden
//     white space or control characters) drops the link and keeps its text.
//   - Images are emitted only for http and https sources; anything else is
//     replaced by its alt text.
//   - Every link has rel="nofollow noopener noreferrer" and target="_blank".
//     Every image has loading="lazy".
//   - Task-list checkboxes are always disabled, so the reader cannot change them.
//   - Headings are shifted down by two levels and capped at h6, so a body never
//     emits h1 or h2 (the page has its own), and they have no id attribute.
//   - Attribute values (href, src, alt, title) are always HTML-escaped.
//
// Text bodies are HTML-escaped as a whole; only the markup that this package
// writes itself (paragraphs, line breaks and links to http and https URLs) is
// HTML.
//
// A [Renderer] is built once by [New] and shared. It holds no state that
// changes after construction, and goldmark's Convert and Parse are safe for
// concurrent use, so one Renderer serves any number of goroutines.
package markdown
