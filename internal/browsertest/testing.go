//go:build clowder_browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

// startTimeout bounds the build and the start of the server of Start.
const startTimeout = 2 * time.Minute

// Start starts a server for the test and stops it when the test ends, even if
// the test fails. It skips the test when Chromium is not installed and fails
// it when the server cannot start.
func Start(t testing.TB) *Server {
	t.Helper()
	return StartWith(t, Options{})
}

// StartWith is Start with options, such as Options{InviteMember: true}. Its
// Stderr is always the log of the test.
func StartWith(t testing.TB, opts Options) *Server {
	t.Helper()
	if _, err := FindChromium(); err != nil {
		t.Skipf("browser test skipped: %v (install chromium-browser, chromium or google-chrome)", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	opts.Stderr = testLogWriter{t}
	srv, err := StartServer(ctx, opts)
	if err != nil {
		t.Fatalf("start the server: %+v", err)
	}
	t.Cleanup(func() {
		if err := srv.Stop(); err != nil {
			t.Errorf("stop the server: %v", err)
		}
	})
	return srv
}

// Browser starts headless Chromium for the test and closes it when the test
// ends. The session is not logged in yet.
// TODO(step 2.4): log in as a user.
func Browser(t testing.TB, _ *Server) context.Context {
	t.Helper()
	ctx, cancel, err := NewBrowser(context.Background())
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == TagChromiumNotFound {
			t.Skipf("browser test skipped: %v", err)
		}
		t.Fatalf("start the browser: %+v", err)
	}
	t.Cleanup(cancel)
	return ctx
}

// testLogWriter writes what the server logs to the log of the test.
type testLogWriter struct{ t testing.TB }

// Write implements io.Writer. A line is one log call.
func (w testLogWriter) Write(p []byte) (int, error) {
	// t.Log after the test ended panics; the server is stopped in Cleanup
	// before that, and Stop waits for the stderr copy, so this is safe.
	w.t.Log("server: " + strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
