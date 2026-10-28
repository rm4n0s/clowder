//go:build clowder_browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/errors"
)

func TestEmulateColorScheme(t *testing.T) {
	browser := Browser(t, nil)
	ctx, cancel := context.WithTimeout(browser, 30*time.Second)
	defer cancel()
	const page = `data:text/html,<p>x</p>`
	const dark = `window.matchMedia('(prefers-color-scheme: dark)').matches`
	tests := []struct {
		name     string
		scheme   string
		wantDark bool
	}{
		{"dark", "dark", true},
		{"light", "light", false},
		{"dark again", "dark", true},
		{"cleared", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := chromedp.Do(ctx, EmulateColorScheme(tt.scheme), chromedp.Navigate(page), chromedp.WaitVisible("p")); err != nil {
				t.Fatalf("EmulateColorScheme: %+v", err)
			}
			got, err := chromedp.Run(ctx, chromedp.Evaluate[bool](dark))
			if err != nil || got != tt.wantDark {
				t.Errorf("prefers dark = %v, %v; want %v", got, err, tt.wantDark)
			}
		})
	}

	t.Run("invalid", func(t *testing.T) {
		err := chromedp.Do(ctx, EmulateColorScheme("sepia"))
		appErr, ok := errors.FromError(err)
		if !ok {
			t.Fatalf("expected *errors.Error, got %T: %v", err, err)
		}
		if appErr.Tag != tagColorSchemeInvalid {
			t.Errorf("tag = %s (route %s), want %s", appErr.Tag, appErr.Route(), tagColorSchemeInvalid)
		}
	})
}
