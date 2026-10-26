//go:build clowder_browser

package browsertest

import (
	"context"
	"os"
	"os/exec"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/errors"
)

// chromiumNames are the executables tried, in order, to find Chromium on PATH.
var chromiumNames = []string{"chromium-browser", "chromium", "google-chrome"}

// FindChromium returns the path of the first of chromium-browser, chromium and
// google-chrome found on PATH. It fails with TagChromiumNotFound when there is
// none.
func FindChromium() (string, error) {
	for _, name := range chromiumNames {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New(TagChromiumNotFound, "no Chromium found on PATH", "tried", chromiumNames)
}

// NewBrowser starts headless Chromium with a temporary profile and returns a
// chromedp context for it. The returned cancel stops the browser, waits for it
// and removes the profile; call it exactly when the browser is no longer
// needed. extra are added to the allocator options, such as chromedp.WindowSize.
// The browser stops when ctx is done, too.
func NewBrowser(ctx context.Context, extra ...chromedp.ExecAllocatorOption) (context.Context, context.CancelFunc, error) {
	path, err := FindChromium()
	if err != nil {
		return nil, nil, err
	}
	profile, err := os.MkdirTemp("", "clowder-chromium-")
	if err != nil {
		return nil, nil, errors.NewErr(tagTempDirFailed, err)
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.UserDataDir(profile),
		chromedp.WindowSize(defaultWidth, defaultHeight),
	)
	opts = append(opts, extra...)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	cancel := func() {
		cancelBrowser()
		cancelAlloc() // waits until the browser process has exited
		// A profile that cannot be removed is a leftover in the temp dir only.
		_ = os.RemoveAll(profile)
	}
	// Start the browser now, so that a failure is reported here.
	if err := chromedp.Do(browserCtx); err != nil {
		cancel()
		return nil, nil, errors.NewErr(tagBrowserStartFailed, err).SetMetadata("path", path)
	}
	return browserCtx, cancel, nil
}

const (
	// defaultWidth and defaultHeight are the size of the browser window.
	defaultWidth  = 1280
	defaultHeight = 800
)

const (
	// TagChromiumNotFound is the tag of FindChromium and NewBrowser failing
	// because no Chromium is installed.
	TagChromiumNotFound = "ChromiumNotFound"

	tagBrowserStartFailed = "BrowserStartFailed"
)
