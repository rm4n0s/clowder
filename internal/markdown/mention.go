package markdown

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// mentionsKey is the key of the parser context under which RenderMentions
// puts the set of usernames (map[string]bool) that become links.
var mentionsKey = parser.NewContextKey()

// kindMention is the kind of mentionNode.
var kindMention = ast.NewNodeKind("Mention")

// mentionNode is the link of a saved mention, an inline node made by
// mentionTransformer.
type mentionNode struct {
	ast.BaseInline
	// name is the username in lower case; label is the mention as written.
	name, label string
}

// Kind implements ast.Node.
func (n *mentionNode) Kind() ast.NodeKind { return kindMention }

// Dump implements ast.Node.
func (n *mentionNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Name": n.name}, nil)
}

// mentionTransformer replaces the "@username" of the text of a parsed document
// by mentionNode, for the usernames in the set that RenderMentions put in the
// parser context. Code spans, links, images, raw HTML and code blocks hold no
// text node that it visits, so a mention in them stays as it is.
type mentionTransformer struct{}

// Transform implements parser.ASTTransformer.
func (mentionTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	set, _ := pc.Get(mentionsKey).(map[string]bool)
	if len(set) == 0 {
		return
	}
	src := reader.Source()
	var parents []ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.CodeSpan, *ast.Link, *ast.AutoLink, *ast.Image, *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		}
		if n.FirstChild() != nil {
			parents = append(parents, n)
		}
		return ast.WalkContinue, nil
	})
	for _, p := range parents {
		linkRuns(p, src, set)
	}
}

// piece is a text node of a run and where its text starts in the text of the
// run.
type piece struct {
	node  *ast.Text
	start int
}

// linkRuns links the mentions in the runs of consecutive text nodes among the
// children of parent. The parser splits text at characters such as "_" and
// "*", so a username may be spread over several nodes; the run is read as one
// text, the way Renderer.Mentions reads a block.
func linkRuns(parent ast.Node, src []byte, set map[string]bool) {
	var run []*ast.Text
	flush := func() {
		if len(run) > 0 {
			linkRun(parent, run, src, set)
			run = nil
		}
	}
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok && !t.IsRaw() {
			run = append(run, t)
			continue
		}
		flush()
	}
	flush()
}

// linkRun replaces the mentions of the set in the text of run by mentionNode.
func linkRun(parent ast.Node, run []*ast.Text, src []byte, set map[string]bool) {
	var sb strings.Builder
	pieces := make([]piece, len(run))
	for i, t := range run {
		pieces[i] = piece{node: t, start: sb.Len()}
		sb.Write(t.Segment.Value(src))
		if t.SoftLineBreak() || t.HardLineBreak() {
			sb.WriteByte('\n')
		}
	}
	all := sb.String()
	var spans []mentionSpan
	for _, m := range findMentions(all, true) {
		if set[m.name] {
			spans = append(spans, m)
		}
	}
	if len(spans) == 0 {
		return
	}
	// The node after the run: the new nodes go before it.
	next := run[len(run)-1].NextSibling()
	for _, t := range run {
		parent.RemoveChild(parent, t)
	}
	add := func(n ast.Node) {
		if next == nil {
			parent.AppendChild(parent, n)
			return
		}
		parent.InsertBefore(parent, next, n)
	}
	si := 0
	for _, p := range pieces {
		seg := p.node.Segment
		length := seg.Len()
		// cur is the offset in the node up to which text is written.
		cur := 0
		var last *ast.Text
		emit := func(from, to int) {
			if to > from {
				last = ast.NewTextSegment(text.NewSegment(seg.Start+from, seg.Start+to))
				add(last)
			}
		}
		for si < len(spans) && spans[si].start < p.start+length {
			m := spans[si]
			from, to := max(m.start-p.start, 0), min(m.end-p.start, length)
			if m.start >= p.start {
				emit(cur, from)
				add(&mentionNode{name: m.name, label: all[m.start:m.end]})
				last = nil
			}
			cur = max(cur, to)
			if m.end > p.start+length {
				break // the mention goes on in the next node
			}
			si++
		}
		emit(cur, length)
		if p.node.SoftLineBreak() || p.node.HardLineBreak() {
			if last == nil {
				last = ast.NewTextSegment(text.NewSegment(seg.Stop, seg.Stop))
				add(last)
			}
			last.SetSoftLineBreak(p.node.SoftLineBreak())
			last.SetHardLineBreak(p.node.HardLineBreak())
		}
	}
}
