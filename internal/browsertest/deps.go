//go:build clowder_browser

package browsertest

import (
	// Kept until step 0.4 uses it. chromedp is built only with the
	// clowder_browser tag, never into the default build.
	_ "github.com/chromedp/chromedp"
)
