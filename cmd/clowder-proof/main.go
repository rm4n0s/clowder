//go:build clowder_browser

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/errors"
)

// Permissions of the directory of the PNG (made if missing) and of the PNG.
const (
	outputDirMode  = 0o750
	outputFileMode = 0o644
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		if appErr, ok := errors.FromError(err); ok {
			_, _ = fmt.Fprintf(os.Stderr, "clowder-proof: %s: %s\n", appErr.Tag, appErr.Message)
		} else {
			_, _ = fmt.Fprintln(os.Stderr, "clowder-proof:", err)
		}
		os.Exit(1)
	}
}

// invitePlaceholder in -page stands for the token of the invitation that
// -invite makes.
const invitePlaceholder = "{invite}"

// roomPlaceholder in -page stands for the whole path of the room with the
// name, as the sidebar of the logged-in user links it: -page {room:secret}
// opens /rooms/<id>, and -page {board:general} opens /rooms/<id>/board, the
// board of the room. It needs -as. The tool finds the room by following the
// sidebar link of that name, so the proof does not depend on the IDs of the
// demo rooms.
var roomPlaceholder = regexp.MustCompile(`\{(room|board):([^{}]+)\}`)

// postPlaceholder in -page stands for the path of the post whose text contains
// the given text: -page {post:Quick question} opens /posts/<id>. It needs -as.
// The tool looks for the post in the feed of every room in the sidebar of that
// user (the posts the feed shows first, newest first) and follows the comment
// count link of the first one that matches, without its #comments, so the page
// opens at the top. The proof does not depend on the IDs of the demo posts.
var postPlaceholder = regexp.MustCompile(`\{post:([^{}]+)\}`)

// fill is one -fill flag: type value into the first element that matches
// selector.
type fill struct {
	selector string
	value    string
}

// fills is the repeatable -fill flag.
type fills []fill

// String implements flag.Value.
func (f *fills) String() string {
	parts := make([]string, len(*f))
	for i, x := range *f {
		parts[i] = x.selector + "=" + x.value
	}
	return strings.Join(parts, " ")
}

// Set implements flag.Value: it adds one selector=value.
func (f *fills) Set(v string) error {
	selector, value, ok := splitFill(v)
	if !ok {
		return errors.New(tagFlagsInvalid, "-fill must be selector=value", "value", v)
	}
	*f = append(*f, fill{selector: selector, value: value})
	return nil
}

// splitFill splits "selector=value" at the first "=" that is not inside
// brackets or quotes of the selector, so that input[name=user]=ana works. It
// returns false when there is no such "=" or the selector is empty.
func splitFill(v string) (selector, value string, ok bool) {
	depth := 0
	var quote rune
	for i, c := range v {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			if depth > 0 {
				depth--
			}
		case c == '=' && depth == 0:
			if i == 0 {
				return "", "", false
			}
			return v[:i], v[i+1:], true
		}
	}
	return "", "", false
}

// clicks is the repeatable -click flag: the selectors of the elements to click,
// in order.
type clicks []string

// String implements flag.Value.
func (c *clicks) String() string { return strings.Join(*c, " ") }

// Set implements flag.Value: it adds one selector.
func (c *clicks) Set(v string) error {
	if v == "" {
		return errors.New(tagFlagsInvalid, "-click needs a selector", "value", v)
	}
	*c = append(*c, v)
	return nil
}

// options are the flags of the tool.
type options struct {
	page   string
	out    string
	as     string
	width  int
	height int
	wait   time.Duration
	// invite makes the server start with one invitation for a new member and
	// replaces invitePlaceholder in page with its token.
	invite bool
	// fills are typed into the page, in order, before the click.
	fills fills
	// clicks are the selectors of the elements to click after the fills, in
	// order; the tool waits for the page to settle after each.
	clicks clicks
	// other is a second user who acts in a second browser after the page of
	// the first one has loaded and its live stream is open.
	other script
}

// script is what a user does in a browser: log in, open a page, type into
// fields and click. The first user's script is the -as, -page, -fill and
// -click flags; the second user's is the -other flags.
type script struct {
	// user is the demo user to log in as; empty for none.
	user string
	// page is the page to open; {room:name}, {board:name} and {post:text} are resolved in
	// the sidebar and the feeds of user.
	page string
	// fills are typed into the page, in order, before the click.
	fills fills
	// clicks are the selectors of the elements to click after the fills, in
	// order.
	clicks clicks
}

// run takes one screenshot. It stops the server and the browser it started
// before it returns, also when ctx is canceled (Ctrl-C) or something fails.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	srv, err := browsertest.StartServer(ctx, browsertest.Options{InviteMember: opts.invite})
	if err != nil {
		return err
	}
	if opts.invite {
		opts.page = strings.ReplaceAll(opts.page, invitePlaceholder, srv.InviteToken)
	}
	shot, runErr := screenshot(ctx, srv, opts)
	if stopErr := srv.Stop(); stopErr != nil {
		_, _ = fmt.Fprintln(stderr, "clowder-proof: stopping the server:", stopErr)
	}
	if runErr != nil {
		return runErr
	}

	if err := os.MkdirAll(filepath.Dir(opts.out), outputDirMode); err != nil {
		return errors.NewErr(tagOutputWriteFailed, err).SetMetadata("path", opts.out)
	}
	if err := os.WriteFile(opts.out, shot, outputFileMode); err != nil {
		return errors.NewErr(tagOutputWriteFailed, err).SetMetadata("path", opts.out)
	}
	_, _ = fmt.Fprintf(stdout, "saved %s (%d bytes)\n", opts.out, len(shot))
	return nil
}

// parseFlags reads and checks the flags.
func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("clowder-proof", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.page, "page", "/", "path of the page to open, such as /rooms/1; {room:name} is the path of the room of that name in the sidebar of the -as user, {board:name} the path of its board; {post:text} is the path of the first post in those rooms whose text contains text")
	fs.StringVar(&o.out, "o", "", "path of the PNG to write (required)")
	fs.StringVar(&o.as, "as", "", "demo user to log in as through /login before the page is opened, such as admin or alice")
	fs.IntVar(&o.width, "width", 1280, "viewport width in pixels")
	fs.IntVar(&o.height, "height", 800, "viewport height in pixels")
	fs.DurationVar(&o.wait, "wait", 500*time.Millisecond, "extra time to settle after the page has loaded and after the click")
	fs.BoolVar(&o.invite, "invite", false, "start with an invitation for a new member and put its token in -page where it says "+invitePlaceholder)
	fs.Var(&o.fills, "fill", "type text into an element before the click, as selector=value, such as '#username=ana' (repeatable)")
	fs.Var(&o.clicks, "click", "CSS selector of an element to click after the -fill flags; the tool then waits for the page to settle (repeatable: the clicks are made in order, such as to open a menu and then pick from it)")
	fs.StringVar(&o.other.user, "other", "", "a second demo user who acts in a second browser once the page has loaded and its live stream is open, before the screenshot of the first one; see -other-page, -other-fill and -other-click")
	fs.StringVar(&o.other.page, "other-page", "", "path of the page the -other user opens (default /); {room:name}, {board:name} and {post:text} are looked up for that user")
	fs.Var(&o.other.fills, "other-fill", "type text into an element of the -other user's page before the click, as selector=value (repeatable)")
	fs.Var(&o.other.clicks, "other-click", "CSS selector of an element the -other user clicks after the -other-fill flags (repeatable, in order)")
	if err := fs.Parse(args); err != nil {
		return options{}, errors.NewErr(tagFlagsInvalid, err)
	}
	switch {
	case fs.NArg() > 0:
		return options{}, errors.New(tagFlagsInvalid, "unexpected arguments", "args", fs.Args())
	case o.out == "":
		return options{}, errors.New(tagFlagsInvalid, "-o is required", "flag", "o")
	case !strings.HasPrefix(o.page, "/") && !strings.HasPrefix(o.page, "{room:") && !strings.HasPrefix(o.page, "{board:") && !strings.HasPrefix(o.page, "{post:"):
		return options{}, errors.New(tagFlagsInvalid, "-page must start with /, {room:name}, {board:name} or {post:text}", "page", o.page)
	case o.width <= 0 || o.height <= 0:
		return options{}, errors.New(tagFlagsInvalid, "-width and -height must be positive",
			"width", o.width, "height", o.height)
	case o.wait < 0:
		return options{}, errors.New(tagFlagsInvalid, "-wait must not be negative", "wait", o.wait.String())
	case o.as == "" && roomPlaceholder.MatchString(o.page):
		return options{}, errors.New(tagFlagsInvalid, "{room:name} or {board:name} in -page needs -as: the room is found in the sidebar of a user",
			"page", o.page)
	case o.as == "" && postPlaceholder.MatchString(o.page):
		return options{}, errors.New(tagFlagsInvalid, "{post:text} in -page needs -as: the post is found in the rooms of a user",
			"page", o.page)
	case o.other.user == "" && (o.other.page != "" || len(o.other.fills) > 0 || len(o.other.clicks) > 0):
		return options{}, errors.New(tagFlagsInvalid, "-other-page, -other-fill and -other-click need -other", "flag", "other")
	case o.other.user != "" && o.as == "":
		return options{}, errors.New(tagFlagsInvalid, "-other needs -as: the second user acts while the first one watches", "flag", "other")
	case o.other.user != "" && o.other.page != "" && !strings.HasPrefix(o.other.page, "/") && !strings.HasPrefix(o.other.page, "{room:") && !strings.HasPrefix(o.other.page, "{board:") && !strings.HasPrefix(o.other.page, "{post:"):
		return options{}, errors.New(tagFlagsInvalid, "-other-page must start with /, {room:name}, {board:name} or {post:text}", "page", o.other.page)
	case o.invite != strings.Contains(o.page, invitePlaceholder):
		return options{}, errors.New(tagFlagsInvalid, "-invite and "+invitePlaceholder+" in -page go together",
			"invite", o.invite, "page", o.page)
	}
	if o.other.user != "" && o.other.page == "" {
		o.other.page = "/"
	}
	return o, nil
}

// screenshot opens the page of srv in a headless Chromium, waits for it to
// settle and returns the PNG of the viewport. When -other is set, a second user
// acts in a second browser after the page has loaded and before the fills and
// the click of the first one, so that the screenshot shows what the first user
// sees of it. The browsers are closed before it returns.
func screenshot(ctx context.Context, srv *browsertest.Server, o options) ([]byte, error) {
	browser, cancel, err := browsertest.NewBrowser(ctx, chromedp.WindowSize(o.width, o.height))
	if err != nil {
		return nil, err
	}
	defer cancel()

	// The first run started the browser; this timeout limits the login and the
	// page.
	pageCtx, cancelPage := context.WithTimeout(browser, pageTimeout)
	defer cancelPage()
	first := script{user: o.as, page: o.page, fills: o.fills, clicks: o.clicks}
	url, err := first.open(pageCtx, srv, o)
	if err != nil {
		return nil, err
	}
	if o.other.user != "" {
		if err := actAs(ctx, srv, o); err != nil {
			return nil, err
		}
		// The live update takes a moment to reach the page.
		if err := chromedp.Do(pageCtx, chromedp.Sleep(o.wait)); err != nil {
			return nil, errors.NewErr(tagPageLoadFailed, err).SetMetadata("url", url)
		}
	}
	if err := first.interact(pageCtx, url, o); err != nil {
		return nil, err
	}
	shot, err := chromedp.Run(pageCtx, chromedp.CaptureScreenshot())
	if err != nil {
		return nil, errors.NewErr(tagScreenshotFailed, err).SetMetadata("url", url)
	}
	return shot, nil
}

// actAs runs the script of the -other user in a browser of their own, which is
// closed when it is done.
func actAs(ctx context.Context, srv *browsertest.Server, o options) error {
	browser, cancel, err := browsertest.NewBrowser(ctx, chromedp.WindowSize(o.width, o.height))
	if err != nil {
		return err
	}
	defer cancel()
	pageCtx, cancelPage := context.WithTimeout(browser, pageTimeout)
	defer cancelPage()
	other := o.other
	url, err := other.open(pageCtx, srv, o)
	if err != nil {
		return err
	}
	return other.interact(pageCtx, url, o)
}

// open logs the user in, resolves the placeholders of the page, opens it and
// waits for it to settle. It returns the URL of the page.
func (sc script) open(ctx context.Context, srv *browsertest.Server, o options) (string, error) {
	if sc.user != "" {
		if err := browsertest.LogIn(ctx, srv, sc.user); err != nil {
			return "", err
		}
	}
	page, err := resolveRooms(ctx, sc.page)
	if err != nil {
		return "", err
	}
	page, err = resolvePosts(ctx, srv.URL, page)
	if err != nil {
		return "", err
	}
	url := srv.URL + page
	err = chromedp.Do(ctx,
		chromedp.EmulateViewport(int64(o.width), int64(o.height)),
		chromedp.Navigate(url),
		chromedp.WaitReady("body"),
		chromedp.Sleep(o.wait),
	)
	if err != nil {
		return "", errors.NewErr(tagPageLoadFailed, err).SetMetadata("url", url)
	}
	return url, nil
}

// interact types the fills of the script into the page, then makes the clicks,
// waiting for the page to settle after each.
func (sc script) interact(ctx context.Context, url string, o options) error {
	for _, f := range sc.fills {
		err := chromedp.Do(ctx, browsertest.Fill(f.selector, f.value))
		if _, tagged := errors.FromError(err); err != nil && !tagged {
			// Fill tags its own failures; this is chromedp's (the browser died).
			return errors.NewErr(tagFillFailed, err).SetMetadata("url", url, "selector", f.selector)
		}
		if err != nil {
			return err
		}
	}
	for _, selector := range sc.clicks {
		err := chromedp.Do(ctx,
			chromedp.Click(selector),
			chromedp.Sleep(o.wait),
		)
		if err != nil {
			return errors.NewErr(tagClickFailed, err).SetMetadata("url", url, "selector", selector)
		}
	}
	return nil
}

// resolveRooms replaces every {room:name} in page with the path of the room of
// that name, and every {board:name} with the path of its board (the path of the
// room and "/board"), read from the link in the sidebar of the page the browser
// shows (the home page, right after the login). It fails with a RoomNotFound
// error when the sidebar has no link with that name.
func resolveRooms(ctx context.Context, page string) (string, error) {
	var out strings.Builder
	last := 0
	for _, m := range roomPlaceholder.FindAllStringSubmatchIndex(page, -1) {
		href, err := roomHref(ctx, page[m[4]:m[5]])
		if err != nil {
			return "", err
		}
		if page[m[2]:m[3]] == "board" {
			href += boardSuffix
		}
		out.WriteString(page[last:m[0]])
		out.WriteString(href)
		last = m[1]
	}
	out.WriteString(page[last:])
	return out.String(), nil
}

// roomHref returns the href of the sidebar link of the room with the name. It
// fails with a RoomNotFound error when there is none.
func roomHref(ctx context.Context, name string) (string, error) {
	quoted, err := json.Marshal(name)
	if err != nil {
		return "", errors.NewErr(tagRoomNotFound, err).SetMetadata("room", name)
	}
	js := `(() => { for (const a of document.querySelectorAll('aside a[data-room]')) { if (a.dataset.room === ` + string(quoted) + `) { return a.getAttribute('href'); } } return ""; })()`
	href, err := chromedp.Run(ctx, chromedp.Evaluate[string](js))
	if err != nil {
		return "", errors.NewErr(tagRoomNotFound, err).SetMetadata("room", name)
	}
	if href == "" {
		return "", errors.New(tagRoomNotFound, "the sidebar has no link to a room with this name", "room", name)
	}
	return href, nil
}

// resolvePosts replaces every {post:text} in page with the path of the first
// post that contains the text, found in the feeds of the rooms in the sidebar
// of the page the browser shows (the home page, right after the login). It
// fails with a PostNotFound error when no feed has such a post. It leaves the
// browser on the last page it opened, so call it after resolveRooms.
func resolvePosts(ctx context.Context, baseURL, page string) (string, error) {
	matches := postPlaceholder.FindAllStringSubmatchIndex(page, -1)
	if len(matches) == 0 {
		return page, nil
	}
	rooms, err := chromedp.Run(ctx, chromedp.Evaluate[[]string](
		`[...document.querySelectorAll('aside a[data-room]')].map(a => a.getAttribute('href'))`))
	if err != nil {
		return "", errors.NewErr(tagPostNotFound, err)
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		href, err := postHref(ctx, baseURL, rooms, page[m[2]:m[3]])
		if err != nil {
			return "", err
		}
		out.WriteString(page[last:m[0]])
		out.WriteString(href)
		last = m[1]
	}
	out.WriteString(page[last:])
	return out.String(), nil
}

// postHref opens the feed of each room (the paths in rooms) in turn and
// returns the path of the first post whose text contains text, from the
// comment count link of the post, without its fragment. It fails with a
// PostNotFound error when there is none.
func postHref(ctx context.Context, baseURL string, rooms []string, text string) (string, error) {
	quoted, err := json.Marshal(text)
	if err != nil {
		return "", errors.NewErr(tagPostNotFound, err).SetMetadata("text", text)
	}
	js := `(() => { for (const a of document.querySelectorAll('#feed-posts article.post')) { const body = a.querySelector('.post-body'); const link = a.querySelector('a.post-comments'); if (body && link && body.textContent.includes(` + string(quoted) + `)) { return link.getAttribute('href').split('#')[0]; } } return ""; })()`
	for _, room := range rooms {
		err := chromedp.Do(ctx, chromedp.Navigate(baseURL+room), chromedp.WaitReady("body"))
		if err != nil {
			return "", errors.NewErr(tagPostNotFound, err).SetMetadata("text", text, "room", room)
		}
		href, err := chromedp.Run(ctx, chromedp.Evaluate[string](js))
		if err != nil {
			return "", errors.NewErr(tagPostNotFound, err).SetMetadata("text", text, "room", room)
		}
		if href != "" {
			return href, nil
		}
	}
	return "", errors.New(tagPostNotFound, "no post in the rooms of the sidebar contains this text", "text", text)
}

// boardSuffix is what {board:name} adds to the path of the room.
const boardSuffix = "/board"

// pageTimeout bounds loading the page and taking the screenshot.
const pageTimeout = time.Minute

const (
	tagFlagsInvalid      = "FlagsInvalid"
	tagOutputWriteFailed = "OutputWriteFailed"
	tagPageLoadFailed    = "PageLoadFailed"
	tagFillFailed        = "FillFailed"
	tagClickFailed       = "ClickFailed"
	tagScreenshotFailed  = "ScreenshotFailed"
	tagRoomNotFound      = "RoomNotFound"
	tagPostNotFound      = "PostNotFound"
)
