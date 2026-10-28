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
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
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

// resetPlaceholder in -page stands for the token of the password reset link
// that -reset makes.
const resetPlaceholder = "{reset}"

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

// conversationPlaceholder in -page stands for the path of the conversation
// whose members are the given usernames, separated by commas:
// -page {conversation:alice,bob} opens /messages/<id> of the conversation of
// alice and bob. It needs -as. The tool finds the conversation in the list of
// conversations of that user (the entries carry their members' usernames), so
// the proof does not depend on the IDs of the demo conversations. It only opens
// the list, which marks nothing read.
var conversationPlaceholder = regexp.MustCompile(`\{conversation:([^{}]+)\}`)

// pagePrefixes are what a -page or -other-page may start with: a path, or a
// placeholder that the tool resolves.
var pagePrefixes = []string{"/", "{room:", "{board:", "{post:", "{conversation:"}

// validPage reports whether a -page value starts like a path or a placeholder.
func validPage(page string) bool {
	for _, p := range pagePrefixes {
		if strings.HasPrefix(page, p) {
			return true
		}
	}
	return false
}

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
	// reset makes the server start with a password reset link for this demo
	// user and replaces resetPlaceholder in page with its token.
	reset string
	// fills are typed into the page, in order, before the click.
	fills fills
	// clicks are the selectors of the elements to click after the fills, in
	// order; the tool waits for the page to settle after each.
	clicks clicks
	// other is a second user who acts in a second browser after the page of
	// the first one has loaded and its live stream is open.
	other script
	// otherHold keeps the browser of the -other user open, on its page, until
	// the screenshot is taken, so that the user stays online (presence, SPEC
	// §7.15) for a page of the first user that is loaded again by a -click.
	otherHold bool
	// cluster starts two servers on one new PostgreSQL database, one Redis and
	// one data directory (SPEC D41): the -other user acts on the first, and the
	// screenshot is taken on the second.
	cluster bool
	// postgresURL is the PostgreSQL server where -cluster makes its database
	// (the user may create databases); empty: CLOWDER_TEST_POSTGRES.
	postgresURL string
	// redisURL is the Redis server of -cluster; empty: CLOWDER_TEST_REDIS.
	redisURL string
	// apiLogin is the client name of a session that the -as user starts
	// through the API before the page is opened; empty for none.
	apiLogin string
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

// Environment variables that -postgres-url and -redis-url fall back to; they
// are those of the tests (SPEC D39), which scripts/test-docker.sh sets.
const (
	postgresURLEnv = "CLOWDER_TEST_POSTGRES"
	redisURLEnv    = "CLOWDER_TEST_REDIS"
)

// servers are the servers of a run: where the screenshot is taken and where
// the -other user acts. They are the same server unless -cluster is set.
type servers struct {
	shot, other *browsertest.Server
}

// run takes one screenshot. It stops the servers and the browsers it started
// before it returns, also when ctx is canceled (Ctrl-C) or something fails.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	var (
		srvs servers
		stop func() error
	)
	if opts.cluster {
		srvs, stop, err = startCluster(ctx, opts, stdout)
	} else {
		srvs, stop, err = startOne(ctx, opts)
	}
	if err != nil {
		return err
	}
	if opts.invite {
		opts.page = strings.ReplaceAll(opts.page, invitePlaceholder, srvs.shot.InviteToken)
	}
	if opts.reset != "" {
		opts.page = strings.ReplaceAll(opts.page, resetPlaceholder, srvs.shot.ResetToken)
	}
	shot, runErr := screenshot(ctx, srvs, opts)
	if stopErr := stop(); stopErr != nil {
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

// startOne starts the one server of a run and returns the function that stops
// it.
func startOne(ctx context.Context, opts options) (servers, func() error, error) {
	srv, err := browsertest.StartServer(ctx, browsertest.Options{InviteMember: opts.invite, ResetUser: opts.reset})
	if err != nil {
		return servers{}, nil, err
	}
	return servers{shot: srv, other: srv}, srv.Stop, nil
}

// startCluster makes a new PostgreSQL database and starts two servers on it,
// with one Redis and one data directory: the first is where the -other user
// acts, the second where the screenshot is taken. It prints both addresses to
// stdout, so that the output says which server each user used. The returned
// function stops the servers and drops the database.
func startCluster(ctx context.Context, opts options, stdout io.Writer) (servers, func() error, error) {
	pgURL, redisURL := opts.postgresURL, opts.redisURL
	if pgURL == "" {
		pgURL = os.Getenv(postgresURLEnv)
	}
	if redisURL == "" {
		redisURL = os.Getenv(redisURLEnv)
	}
	if pgURL == "" || redisURL == "" {
		return servers{}, nil, errors.New(tagFlagsInvalid,
			"-cluster needs a PostgreSQL server and a Redis server: -postgres-url and -redis-url, or "+postgresURLEnv+" and "+redisURLEnv,
			"postgres", pgURL != "", "redis", redisURL != "")
	}
	dbURL, drop, err := store.NewPostgresDatabase(ctx, pgURL)
	if err != nil {
		return servers{}, nil, err
	}
	cluster, err := browsertest.StartCluster(ctx, browsertest.Options{DatabaseURL: dbURL, RedisURL: redisURL}, clusterServers)
	if err != nil {
		if dropErr := drop(); dropErr != nil {
			return servers{}, nil, errors.NewErr(tagClusterFailed, err).SetMetadata("drop", dropErr.Error())
		}
		return servers{}, nil, err
	}
	a, b := cluster.Servers[0], cluster.Servers[1]
	_, _ = fmt.Fprintf(stdout, "server A, where -other acts: %s\nserver B, where the screenshot is taken: %s\n", a.URL, b.URL)
	_, _ = fmt.Fprintf(stdout, "both on the PostgreSQL database %s and the Redis %s, with the data directory %s\n",
		config.RedactURL(dbURL), config.RedactURL(redisURL), a.DataDir)
	stop := func() error {
		stopErr := cluster.Stop()
		if dropErr := drop(); dropErr != nil && stopErr == nil {
			return dropErr
		}
		return stopErr
	}
	return servers{shot: b, other: a}, stop, nil
}

// clusterServers is the number of servers of -cluster.
const clusterServers = 2

// parseFlags reads and checks the flags.
func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("clowder-proof", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.page, "page", "/", "path of the page to open, such as /rooms/1; {room:name} is the path of the room of that name in the sidebar of the -as user, {board:name} the path of its board; {post:text} is the path of the first post in those rooms whose text contains text; {conversation:alice,bob} is the path of the conversation of those members in the list of the -as user")
	fs.StringVar(&o.out, "o", "", "path of the PNG to write (required)")
	fs.StringVar(&o.as, "as", "", "demo user to log in as through /login before the page is opened, such as admin or alice")
	fs.IntVar(&o.width, "width", 1280, "viewport width in pixels")
	fs.IntVar(&o.height, "height", 800, "viewport height in pixels")
	fs.DurationVar(&o.wait, "wait", 500*time.Millisecond, "extra time to settle after the page has loaded and after the click")
	fs.BoolVar(&o.invite, "invite", false, "start with an invitation for a new member and put its token in -page where it says "+invitePlaceholder)
	fs.StringVar(&o.reset, "reset", "", "demo user to start with a password reset link for, such as bob; its token goes in -page where it says "+resetPlaceholder)
	fs.Var(&o.fills, "fill", "type text into an element before the click, as selector=value, such as '#username=ana' (repeatable)")
	fs.Var(&o.clicks, "click", "CSS selector of an element to click after the -fill flags; the tool then waits for the page to settle (repeatable: the clicks are made in order, such as to open a menu and then pick from it)")
	fs.StringVar(&o.other.user, "other", "", "a second demo user who acts in a second browser once the page has loaded and its live stream is open, before the screenshot of the first one; see -other-page, -other-fill and -other-click")
	fs.StringVar(&o.other.page, "other-page", "", "path of the page the -other user opens (default /); {room:name}, {board:name} and {post:text} are looked up for that user")
	fs.Var(&o.other.fills, "other-fill", "type text into an element of the -other user's page before the click, as selector=value (repeatable)")
	fs.BoolVar(&o.otherHold, "other-hold", false, "keep the browser of the -other user open until the screenshot is taken, so that the user is online (use a -click on a link to load the page of the first user again, which shows who is online)")
	fs.BoolVar(&o.cluster, "cluster", false, "start two servers on one new PostgreSQL database, one Redis and one data directory (as compose.cluster.yaml runs them); the -other user acts on the first one and the screenshot is taken on the second one, so it shows a change that came through Redis; needs -other, a PostgreSQL server (-postgres-url or CLOWDER_TEST_POSTGRES) and a Redis (-redis-url or CLOWDER_TEST_REDIS), and prints both addresses")
	fs.StringVar(&o.postgresURL, "postgres-url", "", "with -cluster: the PostgreSQL server where the new database is made, postgres://user:password@host:port/postgres; the user may create and drop databases (default: CLOWDER_TEST_POSTGRES)")
	fs.StringVar(&o.redisURL, "redis-url", "", "with -cluster: the Redis server, redis://host:port/0 (default: CLOWDER_TEST_REDIS)")
	fs.StringVar(&o.apiLogin, "api-login", "", "client name: the -as user also logs in through POST /api/v1/auth/login as a client of that name before the page opens, and the session stays, so that the page of the sessions shows it (needs -as)")
	fs.Var(&o.other.clicks, "other-click", "CSS selector of an element the -other user clicks after the -other-fill flags (repeatable, in order)")
	if err := fs.Parse(args); err != nil {
		return options{}, errors.NewErr(tagFlagsInvalid, err)
	}
	switch {
	case fs.NArg() > 0:
		return options{}, errors.New(tagFlagsInvalid, "unexpected arguments", "args", fs.Args())
	case o.out == "":
		return options{}, errors.New(tagFlagsInvalid, "-o is required", "flag", "o")
	case !validPage(o.page):
		return options{}, errors.New(tagFlagsInvalid, "-page must start with /, {room:name}, {board:name}, {post:text} or {conversation:members}", "page", o.page)
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
	case o.as == "" && conversationPlaceholder.MatchString(o.page):
		return options{}, errors.New(tagFlagsInvalid, "{conversation:members} in -page needs -as: the conversation is found in the list of a user",
			"page", o.page)
	case o.apiLogin != "" && o.as == "":
		return options{}, errors.New(tagFlagsInvalid, "-api-login needs -as: the session is the user's", "flag", "api-login")
	case o.other.user == "" && (o.other.page != "" || len(o.other.fills) > 0 || len(o.other.clicks) > 0):
		return options{}, errors.New(tagFlagsInvalid, "-other-page, -other-fill and -other-click need -other", "flag", "other")
	case o.other.user == "" && o.otherHold:
		return options{}, errors.New(tagFlagsInvalid, "-other-hold needs -other", "flag", "other-hold")
	case o.other.user != "" && o.as == "":
		return options{}, errors.New(tagFlagsInvalid, "-other needs -as: the second user acts while the first one watches", "flag", "other")
	case o.other.user != "" && o.other.page != "" && !validPage(o.other.page):
		return options{}, errors.New(tagFlagsInvalid, "-other-page must start with /, {room:name}, {board:name}, {post:text} or {conversation:members}", "page", o.other.page)
	case o.cluster && o.other.user == "":
		return options{}, errors.New(tagFlagsInvalid, "-cluster needs -other: the second user acts on the other server while the first one watches", "flag", "cluster")
	case o.cluster && o.invite:
		return options{}, errors.New(tagFlagsInvalid, "-cluster and -invite cannot be used together", "flag", "cluster")
	case o.cluster && o.reset != "":
		return options{}, errors.New(tagFlagsInvalid, "-cluster and -reset cannot be used together", "flag", "cluster")
	case !o.cluster && (o.postgresURL != "" || o.redisURL != ""):
		return options{}, errors.New(tagFlagsInvalid, "-postgres-url and -redis-url need -cluster", "flag", "cluster")
	case o.invite != strings.Contains(o.page, invitePlaceholder):
		return options{}, errors.New(tagFlagsInvalid, "-invite and "+invitePlaceholder+" in -page go together",
			"invite", o.invite, "page", o.page)
	case (o.reset != "") != strings.Contains(o.page, resetPlaceholder):
		return options{}, errors.New(tagFlagsInvalid, "-reset and "+resetPlaceholder+" in -page go together",
			"reset", o.reset, "page", o.page)
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
// sees of it. The first user is on srvs.shot and the second on srvs.other. The
// browsers are closed before it returns.
func screenshot(ctx context.Context, srvs servers, o options) ([]byte, error) {
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
	if o.apiLogin != "" {
		if err := apiLogin(ctx, srvs.shot.URL, o.as, o.apiLogin); err != nil {
			return nil, err
		}
	}
	url, err := first.open(pageCtx, srvs.shot, o)
	if err != nil {
		return nil, err
	}
	if o.other.user != "" {
		release, err := actAs(ctx, srvs.other, o)
		if err != nil {
			return nil, err
		}
		defer release()
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

// actAs runs the script of the -other user in a browser of their own. The
// browser is closed when the script is done, or, with -other-hold, when the
// returned function is called.
func actAs(ctx context.Context, srv *browsertest.Server, o options) (release func(), err error) {
	browser, cancel, err := browsertest.NewBrowser(ctx, chromedp.WindowSize(o.width, o.height))
	if err != nil {
		return nil, err
	}
	pageCtx, cancelPage := context.WithTimeout(browser, pageTimeout)
	stop := func() {
		cancelPage()
		cancel()
	}
	other := o.other
	url, err := other.open(pageCtx, srv, o)
	if err == nil {
		err = other.interact(pageCtx, url, o)
	}
	if err != nil {
		stop()
		return nil, err
	}
	if !o.otherHold {
		stop()
		return func() {}, nil
	}
	return stop, nil
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
	page, err = resolveConversations(ctx, srv.URL, page)
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

// resolveConversations replaces every {conversation:a,b} in page with the path
// of the conversation whose members are exactly those usernames, found in the
// list of conversations of the user the browser is logged in as. It fails with
// a ConversationNotFound error when there is none. It leaves the browser on the
// list of conversations, which it only reads.
func resolveConversations(ctx context.Context, baseURL, page string) (string, error) {
	matches := conversationPlaceholder.FindAllStringSubmatchIndex(page, -1)
	if len(matches) == 0 {
		return page, nil
	}
	if err := chromedp.Do(ctx, chromedp.Navigate(baseURL+"/messages"), chromedp.WaitReady("body")); err != nil {
		return "", errors.NewErr(tagConversationNotFound, err)
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		href, err := conversationHref(ctx, page[m[2]:m[3]])
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

// conversationHref returns the path of the conversation in the list the browser
// shows whose members have exactly the usernames (a comma-separated list, in
// any order). It fails with a ConversationNotFound error when there is none.
func conversationHref(ctx context.Context, members string) (string, error) {
	names := strings.Split(members, ",")
	for i, n := range names {
		names[i] = strings.ToLower(strings.TrimSpace(n))
	}
	slices.Sort(names)
	want, err := json.Marshal(strings.Join(names, " "))
	if err != nil {
		return "", errors.NewErr(tagConversationNotFound, err).SetMetadata("members", members)
	}
	js := `(() => { for (const li of document.querySelectorAll('#conversation-list li.conversation')) { if (li.dataset.members === ` + string(want) + `) { return li.querySelector('a.conversation-link').getAttribute('href'); } } return ""; })()`
	href, err := chromedp.Run(ctx, chromedp.Evaluate[string](js))
	if err != nil {
		return "", errors.NewErr(tagConversationNotFound, err).SetMetadata("members", members)
	}
	if href == "" {
		return "", errors.New(tagConversationNotFound, "the list of conversations has none with these members", "members", members)
	}
	return href, nil
}

// boardSuffix is what {board:name} adds to the path of the room.
const boardSuffix = "/board"

// pageTimeout bounds loading the page and taking the screenshot.
const pageTimeout = time.Minute

const (
	tagFlagsInvalid      = "FlagsInvalid"
	tagClusterFailed     = "ClusterFailed"
	tagOutputWriteFailed = "OutputWriteFailed"
	tagPageLoadFailed    = "PageLoadFailed"
	tagFillFailed        = "FillFailed"
	tagClickFailed       = "ClickFailed"
	tagScreenshotFailed  = "ScreenshotFailed"
	tagRoomNotFound      = "RoomNotFound"
	tagPostNotFound      = "PostNotFound"

	tagConversationNotFound = "ConversationNotFound"
)
