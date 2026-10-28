//go:build clowder_browser

package browsertest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
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

// FakeMedia returns the allocator options that give a browser a camera, a
// microphone and a screen to share without any hardware: Chromium's fake
// devices (a moving test picture and a beep), its permission prompts answered
// "allow" for the camera, the microphone and the screen, sound that plays
// without a click on the page, and the host candidates of WebRTC on the
// loopback address and not hidden behind mDNS names, so that a call to a
// server on 127.0.0.1 connects. Pass them to NewBrowser. The tests and proofs
// of the calls use them (step 22.2).
func FakeMedia() []chromedp.ExecAllocatorOption {
	return []chromedp.ExecAllocatorOption{
		chromedp.Flag("use-fake-device-for-media-stream", true),
		chromedp.Flag("use-fake-ui-for-media-stream", true),
		chromedp.Flag("auto-select-desktop-capture-source", "Entire screen"),
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
		chromedp.Flag("allow-loopback-in-peer-connection", true),
		chromedp.Flag("disable-features", "Translate,BlinkGenPropertyTrees,WebRtcHideLocalIpsWithMdns"),
	}
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

// EmulateColorScheme makes the browser report scheme ("light" or "dark") for
// the CSS media feature prefers-color-scheme, as a system setting would, so
// that a page without a data-theme shows the palette of that scheme. An empty
// scheme clears the emulation. It fails with a ColorSchemeInvalid error for any
// other value. The setting belongs to the tab: run it before the page opens or
// reload the page after it.
func EmulateColorScheme(scheme string) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		var features []*emulation.MediaFeature
		switch scheme {
		case "":
		case "light", "dark":
			features = []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: scheme}}
		default:
			return chromedp.Void{}, errors.New(tagColorSchemeInvalid, "the color scheme must be light or dark", "scheme", scheme)
		}
		if _, err := cdp.Call(ctx, t, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Features: features}); err != nil {
			return chromedp.Void{}, errors.NewErr(tagColorSchemeFailed, err).SetMetadata("scheme", scheme)
		}
		return chromedp.Void{}, nil
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
	tagColorSchemeInvalid = "ColorSchemeInvalid"
	tagColorSchemeFailed  = "ColorSchemeFailed"
)
