//go:build clowder_browser

package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// codeToolbars returns, for each code block under the selector, its language
// label, the text of its Copy button and the number of toolbars before it,
// as "go|Copy|1".
func codeToolbars(t *testing.T, ctx context.Context, under string) []string {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Evaluate[[]string](`[...document.querySelectorAll(`+jsQuote(under+" pre.code")+`)].map(pre => {
		const bar = pre.previousElementSibling;
		const toolbars = (bar && bar.classList.contains('code-toolbar')) ? 1 : 0;
		const label = toolbars ? (bar.querySelector('.code-lang') ? bar.querySelector('.code-lang').textContent : '') : '?';
		const button = toolbars ? bar.querySelector('button.code-copy') : null;
		return label + '|' + (button ? button.textContent : '-') + '|' + toolbars;
	})`))
	if err != nil {
		t.Fatalf("read the toolbars of %s: %v", under, err)
	}
	return got
}

// jsQuote returns s as a JavaScript string literal.
func jsQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// TestCodeBlocksHaveALabelAndACopyButton opens the demo post with a Go, a
// Python and a shell block: each block is highlighted and has a toolbar with
// its language and a Copy button; Copy puts the code, and only the code, in the
// clipboard and says "Copied".
func TestCodeBlocksHaveALabelAndACopyButton(t *testing.T) {
	srv := browsertest.Start(t)
	br := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(br, pageTimeout)
	defer cancel()
	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	// Chromium asks for the clipboard permission otherwise.
	for _, name := range []string{"clipboard-read", "clipboard-write"} {
		_, err := chromedp.CallBrowser(ctx, browser.SetPermission, browser.SetPermissionParams{
			Permission: &browser.PermissionDescriptor{Name: name, AllowWithoutSanitization: true},
			Setting:    browser.PermissionSettingGranted,
		})
		if err != nil {
			t.Fatalf("grant %s: %v", name, err)
		}
	}
	openGeneral(t, ctx)

	postID := idOfArticle(t, ctx, ".post", "The same check in Python and in the shell")
	post := "#" + postID
	if err := waitFor(ctx, `document.querySelectorAll(`+jsQuote(post+" .code-toolbar")+`).length === 3`); err != nil {
		t.Fatalf("the three blocks have no toolbar: %v", err)
	}
	got := strings.Join(codeToolbars(t, ctx, post), " ")
	if want := "go|Copy|1 python|Copy|1 sh|Copy|1"; got != want {
		t.Errorf("toolbars = %q, want %q", got, want)
	}
	// Highlighted: spans with classes, and a color that comes from code.css.
	if n := countOf(t, ctx, post+` pre.code[data-lang="go"] span[class^="hl-"]`); n == 0 {
		t.Error("the Go block has no highlighted token")
	}
	if ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](
		`getComputedStyle(document.querySelector(`+jsQuote(post+` pre.code span.hl-kd`)+`)).color !== getComputedStyle(document.querySelector(`+jsQuote(post+` pre.code`)+`)).color`)); err != nil || !ok {
		t.Errorf("a keyword has the color of the plain text: code.css is not applied (%v)", err)
	}
	// The palettes follow the theme setting (data-theme on the html element).
	for theme, want := range map[string]string{"dark": "rgb(22, 27, 34)", "light": "rgb(246, 248, 250)"} {
		runJS(t, ctx, `document.documentElement.setAttribute('data-theme', `+jsQuote(theme)+`); true`)
		got, err := chromedp.Run(ctx, chromedp.Evaluate[string](
			`getComputedStyle(document.querySelector(`+jsQuote(post+" pre.code")+`)).backgroundColor`))
		if err != nil || got != want {
			t.Errorf("background of a code block with data-theme=%s = %q, want %q (%v)", theme, got, want, err)
		}
	}
	// The button.
	btn := post + ` pre.code[data-lang="go"]`
	if ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](
		`(() => { const b = document.querySelector(`+jsQuote(btn)+`).previousElementSibling.querySelector('button'); return b.type === 'button' && b.getAttribute('aria-label') === 'Copy code'; })()`)); err != nil || !ok {
		t.Errorf("the Copy button is not type=button with aria-label \"Copy code\" (%v)", err)
	}
	click := `document.querySelector(` + jsQuote(btn) + `).previousElementSibling.querySelector('button').click(); true`
	runJS(t, ctx, click)
	copied := `document.querySelector(` + jsQuote(btn) + `).previousElementSibling.querySelector('button').textContent === 'Copied'`
	if err := waitFor(ctx, copied); err != nil {
		t.Fatalf("the button did not say Copied: %v", err)
	}
	clip, err := chromedp.Run(ctx, chromedp.Evaluate[string](`navigator.clipboard.readText()`, func(p *runtime.EvaluateParams) {
		p.AwaitPromise = new(true)
	}))
	if err != nil {
		t.Fatalf("read the clipboard: %v", err)
	}
	if want := "func main() {\n\tfmt.Println(\"ship it\")\n}\n"; clip != want {
		t.Errorf("clipboard = %q, want %q", clip, want)
	}
	// The label goes back after two seconds.
	if err := waitFor(ctx, `!(`+copied+`)`); err != nil {
		t.Errorf("the button kept saying Copied: %v", err)
	}
	// A patch that takes the toolbar away and keeps the block (a post put
	// back in the page) gets the toolbar back, once.
	runJS(t, ctx, `document.querySelector(`+jsQuote(btn)+`).previousElementSibling.remove(); true`)
	if err := waitFor(ctx, `document.querySelectorAll(`+jsQuote(post+" .code-toolbar")+`).length === 3`); err != nil {
		t.Errorf("the toolbar did not come back: %v", err)
	}
}

// TestLiveCodeBlockGetsAToolbar has bob on the feed of "general" and alice
// posting a Go block: the block arrives in bob's page through a Datastar patch,
// highlighted, with exactly one toolbar.
func TestLiveCodeBlockGetsAToolbar(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	for _, ctx := range []context.Context{bob, alice} {
		openGeneral(t, ctx)
	}
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	err := chromedp.Do(alice,
		browsertest.Fill("#post-body-new", "Live code:\n\n```go\nfunc live() int { return 42 }\n```\n"),
		chromedp.Click(`#post-editor-new button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("alice posts: %v", err)
	}
	first := `#feed-posts article.post:first-child`
	if err := waitFor(bob, `document.querySelector(`+jsQuote(first+" pre.code span.hl-kd")+`) !== null`); err != nil {
		t.Fatalf("the highlighted block did not reach bob: %v", err)
	}
	if err := waitFor(bob, `document.querySelectorAll(`+jsQuote(first+" .code-toolbar")+`).length === 1`); err != nil {
		t.Fatalf("the live block has no toolbar: %v", err)
	}
	// Alice's own page got it through her form's answer and the stream; still one toolbar.
	if err := waitFor(alice, `document.querySelectorAll(`+jsQuote(first+" .code-toolbar")+`).length === 1`); err != nil {
		t.Fatalf("alice's block has no toolbar: %v", err)
	}
	if err := chromedp.Do(alice, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"bob": bob, "alice": alice} {
		if got := strings.Join(codeToolbars(t, ctx, first), " "); got != "go|Copy|1" {
			t.Errorf("%s: toolbars = %q, want one for go", name, got)
		}
		if n := countOf(t, ctx, first+" .code-toolbar"); n != 1 {
			t.Errorf("%s: %d toolbars, want 1", name, n)
		}
	}
}
