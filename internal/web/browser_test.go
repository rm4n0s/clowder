//go:build clowder_browser

package web_test

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// TestHomeDatastar opens the home page in Chromium and waits until datastar.js
// has run: its data-text shows the value of the data-signals.
func TestHomeDatastar(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, 30*time.Second)
	defer cancel()

	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#datastar-check")); err != nil {
		t.Fatalf("open the home page: %v", err)
	}
	const want = "Datastar is running"
	_, pollErr := chromedp.Run(ctx, chromedp.Poll[chromedp.Void](
		`document.getElementById('datastar-check').textContent === '`+want+`'`,
		chromedp.WithPollingTimeout(10*time.Second)))
	got, err := chromedp.Run(ctx, chromedp.Text("#datastar-check"))
	if err != nil {
		t.Fatalf("read #datastar-check: %v", err)
	}
	if pollErr != nil || got != want {
		t.Errorf("#datastar-check text = %q (wait: %v), want %q", got, pollErr, want)
	}
}
