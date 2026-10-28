//go:build clowder_browser

package web_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/clowder/internal/store"
)

// redisTestEnv names the environment variable with the redis:// URL of a
// server for the tests that need one (SPEC D39); scripts/test-docker.sh sets
// it.
const redisTestEnv = "CLOWDER_TEST_REDIS"

// startTwoServers starts two clowder servers on one new PostgreSQL database,
// one Redis and one data directory, as two processes of compose.cluster.yaml
// are (SPEC D41, §8.1). The test is skipped without both CLOWDER_TEST_POSTGRES
// and CLOWDER_TEST_REDIS.
func startTwoServers(t *testing.T) (a, b *browsertest.Server) {
	t.Helper()
	return startTwoServersWith(t, browsertest.Options{})
}

// startTwoServersWith is startTwoServers with more options for both servers,
// such as Calls.
func startTwoServersWith(t *testing.T, extra browsertest.Options) (a, b *browsertest.Server) {
	t.Helper()
	pgURL, redisURL := os.Getenv(store.PostgresTestEnv), os.Getenv(redisTestEnv)
	if pgURL == "" || redisURL == "" {
		t.Skipf("set %s and %s (scripts/test-docker.sh does) to run the test with two servers", store.PostgresTestEnv, redisTestEnv)
	}
	dbURL, drop, err := store.NewPostgresDatabase(t.Context(), pgURL)
	if err != nil {
		t.Fatalf("a database for the servers: %+v", err)
	}
	// Registered before the servers, so it runs after they stopped.
	t.Cleanup(func() {
		if err := drop(); err != nil {
			t.Errorf("drop the database: %+v", err)
		}
	})
	extra.DatabaseURL, extra.RedisURL = dbURL, redisURL
	c := browsertest.StartClusterWith(t, extra, 2)
	return c.Servers[0], c.Servers[1]
}

// firstPostIs reports, as a JavaScript expression, whether the first post of
// the feed has the text.
func firstPostIs(text string) string {
	first := `#feed-posts article.post:first-child .post-body`
	return `document.querySelector('` + first + `') !== null && document.querySelector('` + first + `').textContent.includes(` + strconv.Quote(text) + `)`
}

// post writes a post in "general" through the editor of the page.
func post(t *testing.T, ctx context.Context, text string) {
	t.Helper()
	err := chromedp.Do(ctx,
		browsertest.Fill("#post-body-new", text),
		chromedp.Click(`#post-editor-new button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("post %q: %v", text, err)
	}
}

// TestClusterPostIsLiveOnTheOtherProcess has alice on server A and bob on
// server B, which share one database, one Redis and one data directory: a
// session made on A works on B, alice's post appears at the top of bob's feed
// without a reload, and bob's answer appears in alice's.
func TestClusterPostIsLiveOnTheOtherProcess(t *testing.T) {
	a, b := startTwoServers(t)
	if a.URL == b.URL {
		t.Fatalf("both servers have the URL %s", a.URL)
	}
	alice := secondBrowser(t, a, "alice")
	bob := secondBrowser(t, b, "bob")

	// The session that A made for alice is valid on B: both read it from the
	// shared database. (Cookies do not tell the ports apart.)
	if err := chromedp.Do(alice, chromedp.Navigate(b.URL+"/"), chromedp.WaitVisible("#signed-in-as")); err != nil {
		t.Fatalf("alice's session from A does not work on B: %v", err)
	}
	if err := chromedp.Do(alice, chromedp.Navigate(a.URL+"/"), chromedp.WaitVisible("#signed-in-as")); err != nil {
		t.Fatalf("alice back on A: %v", err)
	}

	for _, ctx := range []context.Context{alice, bob} {
		openGeneral(t, ctx)
	}
	// The pages must not reload: these markers would be gone.
	runJS(t, alice, `window.notReloaded = true; true`)
	runJS(t, bob, `window.notReloaded = true; true`)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	const fromA = "Written on process A"
	post(t, alice, fromA)
	if err := waitFor(bob, firstPostIs(fromA)); err != nil {
		t.Fatalf("bob, on process B, does not see the post that alice wrote on process A: %v", err)
	}
	first := `#feed-posts article.post:first-child`
	if got := textOf(t, bob, first+" .post-author"); got != "Alice" {
		t.Errorf("author seen by bob = %q", got)
	}
	if n := countOf(t, bob, first+" .post-edit, "+first+" .post-delete"); n != 0 {
		t.Errorf("bob's card of alice's post has %d author buttons", n)
	}

	const fromB = "Answered on process B"
	post(t, bob, fromB)
	if err := waitFor(alice, firstPostIs(fromB)); err != nil {
		t.Fatalf("alice, on process A, does not see the post that bob wrote on process B: %v", err)
	}
	for name, ctx := range map[string]context.Context{"alice": alice, "bob": bob} {
		kept, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`window.notReloaded === true`))
		if err != nil || !kept {
			t.Errorf("%s's page was reloaded (%v)", name, err)
		}
		if n := countOf(t, ctx, `#feed-posts article.post:first-child`); n != 1 {
			t.Errorf("%s sees %d first posts", name, n)
		}
	}
}
