//go:build clowder_browser

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/errors"
)

// Limits of -render-icon.
const (
	defaultIconSize = 32
	minIconSize     = 16
	maxIconSize     = 1024
	maxIconPadding  = 40
)

// transparentBackground is Emulation.setDefaultBackgroundColorOverride with a
// color of alpha 0. The command of cdproto cannot send it: its alpha field is
// left out when it is zero, and the browser then takes 1.
var transparentBackground = cdp.Command[transparentBackgroundParams, cdp.Empty]{Method: "Emulation.setDefaultBackgroundColorOverride"}

// transparentBackgroundParams are the parameters of transparentBackground: the
// color 0, 0, 0 with alpha 0.
type transparentBackgroundParams struct {
	Color struct {
		R int     `json:"r"`
		G int     `json:"g"`
		B int     `json:"b"`
		A float64 `json:"a"`
	} `json:"color"`
}

// hexColor is what -icon-bg may be: #rrggbb.
var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// checkRenderIcon checks the flags of -render-icon: it needs no server, so
// none of the flags that start one or log in may be set.
func (o options) checkRenderIcon() (options, error) {
	if o.iconSize == 0 {
		o.iconSize = defaultIconSize
	}
	switch {
	case o.iconSize < minIconSize || o.iconSize > maxIconSize:
		return options{}, errors.New(tagFlagsInvalid, "-icon-size must be between 16 and 1024", "size", o.iconSize)
	case o.iconPadding < 0 || o.iconPadding > maxIconPadding:
		return options{}, errors.New(tagFlagsInvalid, "-icon-pad must be between 0 and 40", "pad", o.iconPadding)
	case o.iconBackground != "" && !hexColor.MatchString(o.iconBackground):
		return options{}, errors.New(tagFlagsInvalid, "-icon-bg must be #rrggbb", "bg", o.iconBackground)
	case o.as != "" || o.other.user != "" || o.cluster || o.invite || o.reset != "" || o.ssh || o.sshCode != "" || o.apiLogin != "":
		return options{}, errors.New(tagFlagsInvalid, "-render-icon starts no server: it cannot be used with -as, -other, -cluster, -invite, -reset, -ssh, -ssh-code or -api-login", "flag", "render-icon")
	}
	return o, nil
}

// iconPage is the page that shows the icon: the SVG fills the window, with
// padding percent of its size around it, on bg (transparent when empty).
func iconPage(svg []byte, size, padding int, bg string) string {
	background := "transparent"
	if bg != "" {
		background = bg
	}
	inner := 100 - 2*padding
	return fmt.Sprintf(`<!doctype html><meta charset="utf-8"><style>html,body{margin:0;width:%dpx;height:%dpx;background:%s}body{display:flex;align-items:center;justify-content:center}img{width:%d%%;height:%d%%;display:block}</style><img alt="" src="data:image/svg+xml;base64,%s">`,
		size, size, background, inner, inner, base64.StdEncoding.EncodeToString(svg))
}

// renderIcon renders the SVG file of -render-icon to the PNG of -o, in a
// headless Chromium with a transparent default background, and says so on
// stdout. The icons of the installed app are made the same way (scripts/README.md).
func renderIcon(ctx context.Context, o options, stdout io.Writer) error {
	svg, err := os.ReadFile(o.renderIcon)
	if err != nil {
		return errors.NewErr(tagIconRenderFailed, err).SetMetadata("path", o.renderIcon)
	}
	browser, cancel, err := browsertest.NewBrowser(ctx, chromedp.WindowSize(o.iconSize, o.iconSize))
	if err != nil {
		return err
	}
	defer cancel()
	pageCtx, cancelPage := context.WithTimeout(browser, pageTimeout)
	defer cancelPage()

	url := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(iconPage(svg, o.iconSize, o.iconPadding, o.iconBackground)))
	transparent := func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		_, err := cdp.Call(ctx, t, transparentBackground, transparentBackgroundParams{})
		return chromedp.Void{}, err
	}
	err = chromedp.Do(pageCtx,
		transparent,
		chromedp.EmulateViewport(int64(o.iconSize), int64(o.iconSize)),
		chromedp.Navigate(url),
	)
	if err != nil {
		return errors.NewErr(tagIconRenderFailed, err).SetMetadata("path", o.renderIcon, "size", strconv.Itoa(o.iconSize))
	}
	// The page has loaded, so the image is decoded. Do not wait here: a
	// headless Chromium that has sat idle on a window this small never
	// answers Page.captureScreenshot.
	loaded, err := chromedp.Run(pageCtx, chromedp.Evaluate[bool](`document.images.length === 1 && document.images[0].complete && document.images[0].naturalWidth > 0`))
	if err != nil {
		return errors.NewErr(tagIconRenderFailed, err).SetMetadata("path", o.renderIcon)
	}
	if !loaded {
		return errors.New(tagIconRenderFailed, "the browser could not draw the SVG", "path", o.renderIcon)
	}
	capture := func(ctx context.Context, t *chromedp.Target) ([]byte, error) {
		shot, err := cdp.Call(ctx, t, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatPng})
		if err != nil {
			return nil, err
		}
		return shot.Data, nil
	}
	png, err := chromedp.Run(pageCtx, capture)
	if err != nil {
		return errors.NewErr(tagIconRenderFailed, err).SetMetadata("path", o.renderIcon, "size", strconv.Itoa(o.iconSize))
	}
	if err := os.MkdirAll(filepath.Dir(o.out), outputDirMode); err != nil {
		return errors.NewErr(tagOutputWriteFailed, err).SetMetadata("path", o.out)
	}
	if err := os.WriteFile(o.out, png, outputFileMode); err != nil {
		return errors.NewErr(tagOutputWriteFailed, err).SetMetadata("path", o.out)
	}
	_, _ = fmt.Fprintf(stdout, "saved %s (%d bytes, %dx%d)\n", o.out, len(png), o.iconSize, o.iconSize)
	return nil
}

const tagIconRenderFailed = "IconRenderFailed"
