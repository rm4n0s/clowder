//go:build clowder_browser

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
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

// options are the flags of the tool.
type options struct {
	page   string
	out    string
	as     string
	width  int
	height int
	wait   time.Duration
}

// run takes one screenshot. It stops the server and the browser it started
// before it returns, also when ctx is canceled (Ctrl-C) or something fails.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	if opts.as != "" {
		_, _ = fmt.Fprintf(stderr, "note: -as %q is ignored until step 2.4 adds the login\n", opts.as)
	}

	srv, err := browsertest.StartServer(ctx, browsertest.Options{})
	if err != nil {
		return err
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
	fs.StringVar(&o.page, "page", "/", "path of the page to open, such as /rooms/1")
	fs.StringVar(&o.out, "o", "", "path of the PNG to write (required)")
	fs.StringVar(&o.as, "as", "", "user to log in as (ignored until step 2.4)")
	fs.IntVar(&o.width, "width", 1280, "viewport width in pixels")
	fs.IntVar(&o.height, "height", 800, "viewport height in pixels")
	fs.DurationVar(&o.wait, "wait", 500*time.Millisecond, "extra time to settle after the page has loaded")
	if err := fs.Parse(args); err != nil {
		return options{}, errors.NewErr(tagFlagsInvalid, err)
	}
	switch {
	case fs.NArg() > 0:
		return options{}, errors.New(tagFlagsInvalid, "unexpected arguments", "args", fs.Args())
	case o.out == "":
		return options{}, errors.New(tagFlagsInvalid, "-o is required", "flag", "o")
	case !strings.HasPrefix(o.page, "/"):
		return options{}, errors.New(tagFlagsInvalid, "-page must start with /", "page", o.page)
	case o.width <= 0 || o.height <= 0:
		return options{}, errors.New(tagFlagsInvalid, "-width and -height must be positive",
			"width", o.width, "height", o.height)
	case o.wait < 0:
		return options{}, errors.New(tagFlagsInvalid, "-wait must not be negative", "wait", o.wait.String())
	}
	return o, nil
}

// screenshot opens the page of srv in a headless Chromium, waits for it to
// settle and returns the PNG of the viewport. The browser is closed before it
// returns.
func screenshot(ctx context.Context, srv *browsertest.Server, o options) ([]byte, error) {
	browser, cancel, err := browsertest.NewBrowser(ctx, chromedp.WindowSize(o.width, o.height))
	if err != nil {
		return nil, err
	}
	defer cancel()

	url := srv.URL + o.page
	// The first run started the browser; this timeout limits only the page.
	pageCtx, cancelPage := context.WithTimeout(browser, pageTimeout)
	defer cancelPage()
	err = chromedp.Do(pageCtx,
		chromedp.EmulateViewport(int64(o.width), int64(o.height)),
		chromedp.Navigate(url),
		chromedp.WaitReady("body"),
		chromedp.Sleep(o.wait),
	)
	if err != nil {
		return nil, errors.NewErr(tagPageLoadFailed, err).SetMetadata("url", url)
	}
	shot, err := chromedp.Run(pageCtx, chromedp.CaptureScreenshot())
	if err != nil {
		return nil, errors.NewErr(tagScreenshotFailed, err).SetMetadata("url", url)
	}
	return shot, nil
}

// pageTimeout bounds loading the page and taking the screenshot.
const pageTimeout = time.Minute

const (
	tagFlagsInvalid      = "FlagsInvalid"
	tagOutputWriteFailed = "OutputWriteFailed"
	tagPageLoadFailed    = "PageLoadFailed"
	tagScreenshotFailed  = "ScreenshotFailed"
)
