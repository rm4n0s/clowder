//go:build clowder_browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/errors"
)

// TestLogIn logs in through the login form of the real server, as two users
// in turn, and checks the sidebar names the user of the session.
func TestLogIn(t *testing.T) {
	srv := Start(t)
	tests := []struct {
		name string
		user string
		want string
	}{
		{"a member", "alice", "Alice"},
		{"an admin", "admin", "Admin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			browser := Browser(t, srv)
			ctx, cancel := context.WithTimeout(browser, 60*time.Second)
			defer cancel()
			if err := LogIn(ctx, srv, tt.user); err != nil {
				t.Fatalf("LogIn: %+v", err)
			}
			got, err := chromedp.Run(ctx, chromedp.Text("aside .sidebar-user"))
			if err != nil || got != tt.want {
				t.Errorf("sidebar name = %q, %v; want %q", got, err, tt.want)
			}
		})
	}

	t.Run("an unknown user", func(t *testing.T) {
		browser := Browser(t, srv)
		ctx, cancel := context.WithTimeout(browser, 3*time.Second)
		defer cancel()
		err := LogIn(ctx, srv, "nobody")
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != tagLogInFailed || !appErr.HasRoute("LogIn.LogInFailed") {
			t.Fatalf("LogIn(nobody) = %v, want LogInFailed", err)
		}
		if appErr.Metadata["user"] != "nobody" {
			t.Errorf("metadata = %v", appErr.Metadata)
		}
	})
}
