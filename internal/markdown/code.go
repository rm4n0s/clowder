package markdown

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"regexp"
	"sync"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// CodeClassPrefix is the prefix of the CSS classes of the highlighted tokens
// of a code block: a keyword is <span class="hl-k">. The class after the prefix
// is chroma's name of the token type. The stylesheet
// internal/web/static/code.css is generated for this prefix.
const CodeClassPrefix = "hl-"

//go:generate go run ./cmd/gencodecss -o ../web/static/code.css

const (
	// maxHighlightBytes is the size over which a code block stays plain.
	maxHighlightBytes = 32 << 10
	// maxHighlightLines is the number of lines over which a code block stays
	// plain.
	maxHighlightLines = 1000
	// codeCacheBytes is the most bytes of highlighted blocks a Renderer keeps.
	codeCacheBytes = 8 << 20
	// codeCacheEntryBytes is the size over which one block is never cached.
	codeCacheEntryBytes = 1 << 20
)

// codeLangPattern is what the language of a block must look like to be kept
// (as data-lang and as the name of the lexer to look for).
var codeLangPattern = regexp.MustCompile(`^[A-Za-z0-9_+#.-]{1,32}$`)

// codeFormatter writes the tokens of a block as <span class="hl-…"> elements,
// with no wrapping element, no line spans and no line numbers. It holds no
// state that changes after construction, so every goroutine shares it.
var codeFormatter = chromahtml.New(
	chromahtml.WithClasses(true),
	chromahtml.ClassPrefix(CodeClassPrefix),
	chromahtml.PreventSurroundingPre(true),
)

// codeStyle is the style the formatter is given. With classes the formatter
// takes no colour from it; the colours are in code.css.
var codeStyle = styles.Fallback

// codeBlockRenderer renders fenced code blocks: highlighted when the block
// names a language that chroma knows and is not too large, plain otherwise.
type codeBlockRenderer struct {
	cache *codeCache
}

// RegisterFuncs implements renderer.NodeRenderer.
func (c *codeBlockRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, c.renderFenced)
}

func (c *codeBlockRenderer) renderFenced(
	w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.FencedCodeBlock)
	lang := codeLanguage(n.Language(source))

	lines := n.Lines()
	size := 0
	for i := range lines.Len() {
		seg := lines.At(i)
		size += seg.Len()
	}
	code := make([]byte, 0, size)
	for i := range lines.Len() {
		seg := lines.At(i)
		code = append(code, seg.Value(source)...)
	}

	_, _ = w.WriteString(`<pre class="code"`)
	if lang != "" {
		_, _ = w.WriteString(` data-lang="`)
		_, _ = w.Write(util.EscapeHTML([]byte(lang)))
		_ = w.WriteByte('"')
	}
	_, _ = w.WriteString("><code>")
	if lang == "" || size > maxHighlightBytes || lines.Len() > maxHighlightLines ||
		!c.writeHighlighted(w, lang, string(code)) {
		_, _ = w.Write(util.EscapeHTML(code))
	}
	_, _ = w.WriteString("</code></pre>\n")
	return ast.WalkSkipChildren, nil
}

// codeLanguage returns the language of a block from the first word of its
// info string, or "" when the block has none or it does not look like a
// language name.
func codeLanguage(info []byte) string {
	if !codeLangPattern.Match(info) {
		return ""
	}
	return string(info)
}

// writeHighlighted writes code as highlighted tokens, and reports whether it
// did. It writes nothing and returns false when the language has no lexer
// (found by name, alias or file extension, never by guessing), or when the
// lexer or the formatter fails; the caller then writes the plain block. The
// tokens are made in a buffer first, so a failure half-way leaves no partial
// output. Highlighted output is kept in the cache of the renderer, so a block
// that is read again is not lexed again.
func (c *codeBlockRenderer) writeHighlighted(w util.BufWriter, lang, code string) bool {
	lexer := lexers.Get(lang)
	if lexer == nil {
		return false
	}
	key := codeCacheKey(lexer.Config().Name, code)
	if html, ok := c.cache.get(key); ok {
		_, _ = w.WriteString(html)
		return true
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return false
	}
	var buf bytes.Buffer
	if err := codeFormatter.Format(&buf, codeStyle, iterator); err != nil {
		return false
	}
	html := buf.String()
	c.cache.put(key, html)
	_, _ = w.WriteString(html)
	return true
}

// codeCacheKey returns the key of a block: the SHA-256 of the name of its
// lexer, a zero byte and the code.
func codeCacheKey(lexerName, code string) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write([]byte(lexerName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(code))
	var key [sha256.Size]byte
	h.Sum(key[:0])
	return key
}

// codeCache keeps the HTML of highlighted blocks, least recently used out
// first, so that the bytes it holds stay at most limit. It is safe for
// concurrent use. Two goroutines that highlight the same block both put it;
// the last put wins.
type codeCache struct {
	mu      sync.Mutex
	limit   int
	entry   int // the size over which one entry is not kept
	bytes   int
	order   *list.List // front is the most recently used
	entries map[[sha256.Size]byte]*list.Element
}

// codeCacheItem is what an element of codeCache.order holds.
type codeCacheItem struct {
	key  [sha256.Size]byte
	html string
}

// newCodeCache returns a cache that holds at most limit bytes and no entry
// over entry bytes.
func newCodeCache(limit, entry int) *codeCache {
	return &codeCache{limit: limit, entry: entry, order: list.New(), entries: map[[sha256.Size]byte]*list.Element{}}
}

// get returns the HTML kept for key and makes it the most recently used.
func (c *codeCache) get(key [sha256.Size]byte) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(el)
	return el.Value.(*codeCacheItem).html, true
}

// put keeps html for key, and evicts the least recently used entries until the
// total is within the limit. An entry over the entry limit is not kept.
func (c *codeCache) put(key [sha256.Size]byte, html string) {
	if len(html) > c.entry || len(html) > c.limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		item := el.Value.(*codeCacheItem)
		c.bytes += len(html) - len(item.html)
		item.html = html
		c.order.MoveToFront(el)
	} else {
		c.entries[key] = c.order.PushFront(&codeCacheItem{key: key, html: html})
		c.bytes += len(html)
	}
	for c.bytes > c.limit {
		last := c.order.Back()
		item := last.Value.(*codeCacheItem)
		c.order.Remove(last)
		delete(c.entries, item.key)
		c.bytes -= len(item.html)
	}
}
