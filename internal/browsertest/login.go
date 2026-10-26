//go:build clowder_browser

package browsertest

import (
	"context"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/errors"
)

// DemoPassword is the password of every user "clowder demo" makes; it is the
// same constant as demoPassword in cmd/clowder.
const DemoPassword = "demo-password-1234"

// LogIn logs the demo user username in through the login page of srv, as a
// person does: it opens /login, types the username and DemoPassword, clicks
// the button and waits until the home page shows. The browser keeps the session
// cookie, so the pages opened next are the user's. ctx is a browser context
// (see NewBrowser). It fails with a LogInFailed error when the page does not
// load, a field is missing or the home page does not appear.
func LogIn(ctx context.Context, srv *Server, username string) error {
	err := chromedp.Do(ctx,
		chromedp.Navigate(srv.URL+"/login"),
		chromedp.WaitVisible("#login-form"),
		Fill("#login-username", username),
		Fill("#login-password", DemoPassword),
		chromedp.Click(`#login-form button[type="submit"]`),
		chromedp.WaitVisible("#signed-in-as"),
	)
	if err != nil {
		if _, tagged := errors.FromError(err); tagged {
			// Fill tagged its own failure.
			return err
		}
		return errors.NewErr(tagLogInFailed, err).SetMetadata("user", username, "url", srv.URL)
	}
	return nil
}

const tagLogInFailed = "LogInFailed"
