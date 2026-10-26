//go:build clowder_browser

package browsertest

import (
	"context"
	"encoding/json"
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

// Fill empties the first element that matches the CSS selector and types value
// into it with key events, as a person does, so that the page's own handlers
// see it. It sets the value property to empty first because chromedp.Clear
// only sets the attribute, which changes nothing in a field that has been
// typed in or patched by the server. It fails with a FillFailed error when no
// element matches.
func Fill(selector, value string) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		quoted, err := json.Marshal(selector)
		if err != nil {
			return chromedp.Void{}, errors.NewErr(tagFillFailed, err).SetMetadata("selector", selector)
		}
		js := `(() => { const el = document.querySelector(` + string(quoted) + `); if (!el) { return false; } el.value = ""; return true; })()`
		found, err := chromedp.Evaluate[bool](js)(ctx, t)
		if err != nil {
			return chromedp.Void{}, errors.NewErr(tagFillFailed, err).SetMetadata("selector", selector)
		}
		if !found {
			return chromedp.Void{}, errors.New(tagFillFailed, "no element matches the selector", "selector", selector)
		}
		return chromedp.SendKeys(selector, value)(ctx, t)
	}
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
	tagFillFailed         = "FillFailed"
)
