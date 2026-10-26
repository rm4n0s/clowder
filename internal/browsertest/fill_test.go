//go:build clowder_browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/errors"
)

func TestFill(t *testing.T) {
	browser := Browser(t, nil)
	ctx, cancel := context.WithTimeout(browser, 30*time.Second)
	defer cancel()
	// "a" has a value and a second Fill must replace it, also after the field
	// was typed in once (when chromedp.Clear would change nothing).
	const page = `data:text/html,<input id="a" value="old"><input id="b"><input id="c" value='q"uote'>`
	if err := chromedp.Do(ctx, chromedp.Navigate(page), chromedp.WaitVisible("#a")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		selector string
		value    string
	}{
		{"replaces an initial value", "#a", "new"},
		{"replaces a typed value", "#a", "newer"},
		{"fills an empty field", "#b", "hello world"},
		{"a selector with quotes", `input[value='q"uote']`, "ok"},
		{"empties with an empty value", "#b", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := chromedp.Do(ctx, Fill(tt.selector, tt.value)); err != nil {
				t.Fatalf("Fill: %+v", err)
			}
			got, err := chromedp.Run(ctx, chromedp.Value(tt.selector))
			if err != nil || got != tt.value {
				t.Errorf("value of %s = %q, %v; want %q", tt.selector, got, err, tt.value)
			}
		})
	}

	t.Run("no element matches", func(t *testing.T) {
		err := chromedp.Do(ctx, Fill("#missing", "x"))
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != tagFillFailed {
			t.Fatalf("error = %v, want tag %s", err, tagFillFailed)
		}
		if appErr.Metadata["selector"] != "#missing" {
			t.Errorf("metadata = %v", appErr.Metadata)
		}
	})
	t.Run("a selector that is not valid CSS", func(t *testing.T) {
		err := chromedp.Do(ctx, Fill("##", "x"))
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != tagFillFailed {
			t.Fatalf("error = %v, want tag %s", err, tagFillFailed)
		}
	})
}
