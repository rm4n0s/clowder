//go:build clowder_browser

package web_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// controlledBy waits until a service worker controls the page of ctx (the
// worker takes over a page that was open before it was installed: sw.js claims
// its clients).
func controlledBy(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := waitFor(ctx, `'serviceWorker' in navigator && navigator.serviceWorker.controller !== null`); err != nil {
		t.Fatalf("no service worker controls the page: %v", err)
	}
}

// probe runs the async JavaScript function body in the page and returns its
// result as a string: the answer is put in window.probeResult, which is
// polled.
func probe(t *testing.T, ctx context.Context, body string) string {
	t.Helper()
	runJS(t, ctx, `window.probeResult = null; (async () => { `+body+` })().then((v) => { window.probeResult = String(v); }, (e) => { window.probeResult = 'error: ' + e; }); true`)
	if err := waitFor(ctx, `window.probeResult !== null`); err != nil {
		t.Fatalf("the probe did not finish: %v", err)
	}
	got, err := chromedp.Run(ctx, chromedp.Evaluate[string](`window.probeResult`))
	if err != nil {
		t.Fatalf("read the probe: %v", err)
	}
	return got
}

// TestServiceWorkerCachesOnlyStatic: the worker is registered by the pages of a
// logged-in person, takes control of the page, and after the person has visited
// a page, called the API, opened a file and an avatar, the caches hold URLs
// under /static/ and nothing else, in one cache named after the version in
// sw.js. A cache of another version is deleted when the worker activates.
func TestServiceWorkerCachesOnlyStatic(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()
	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)
	controlledBy(t, ctx)

	// A page loaded under the worker asks for its static files through it.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible(".room-card")); err != nil {
		t.Fatal(err)
	}
	controlledBy(t, ctx)
	// The pages, the API, a file and an avatar, all asked for by the page that
	// the worker controls (they go through its fetch event).
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible(".room-card")); err != nil {
		t.Fatal(err)
	}
	room := hrefOf(t, ctx, `.room-card[data-room="general"] h2 a`)
	got := probe(t, ctx, `
		const urls = [];
		const page = await fetch(`+"`"+room+"`"+`); urls.push([`+"`"+room+"`"+`, page.status]);
		const html = await page.text();
		const doc = new DOMParser().parseFromString(html, 'text/html');
		const found = [...doc.querySelectorAll('[src],[href]')].map((e) => e.getAttribute('src') || e.getAttribute('href')).filter((u) => u.startsWith('/files/') || u.startsWith('/avatars/'));
		for (const u of ['/api/v1/me', '/files/1/none.txt', '/avatars/1', '/healthz', ...found]) {
			const r = await fetch(u); urls.push([u, r.status]);
		}
		return JSON.stringify(urls);
	`)
	t.Logf("fetched: %s", got)
	if !strings.Contains(got, "/avatars/") || !strings.Contains(got, "/api/v1/") || !strings.Contains(got, "/files/") {
		t.Fatalf("the probe did not ask for an avatar, a file and the API: %s", got)
	}

	cached := probe(t, ctx, `
		const out = {};
		for (const name of await caches.keys()) {
			const cache = await caches.open(name);
			out[name] = (await cache.keys()).map((r) => new URL(r.url).pathname);
		}
		return JSON.stringify(out);
	`)
	var caches map[string][]string
	if err := json.Unmarshal([]byte(cached), &caches); err != nil {
		t.Fatalf("the caches %q: %v", cached, err)
	}
	if len(caches) != 1 {
		t.Fatalf("the caches are %v, want exactly one", caches)
	}
	for name, urls := range caches {
		if !strings.HasPrefix(name, "clowder-static-") {
			t.Errorf("the cache is named %q", name)
		}
		if len(urls) < 3 {
			t.Errorf("the cache holds only %v: the static files of the page were not kept", urls)
		}
		for _, u := range urls {
			if !strings.HasPrefix(u, "/static/") {
				t.Errorf("the cache holds %s, which is not under /static/", u)
			}
		}
		want := false
		for _, u := range urls {
			want = want || u == "/static/app.css"
		}
		if !want {
			t.Errorf("/static/app.css is not in the cache %v", urls)
		}
		// The name carries the version that sw.js was served with.
		sw := probe(t, ctx, `return await (await fetch('/sw.js')).text();`)
		if !strings.Contains(sw, "const VERSION = '"+strings.TrimPrefix(name, "clowder-static-")+"';") {
			t.Errorf("the cache %q does not carry the version of /sw.js", name)
		}
	}

	// The page names its files with the version of /sw.js, and the cache key is the
	// whole address: two versions of one URL are two entries, so a page of a new
	// release never gets the old file from the cache of an old worker.
	sw := probe(t, ctx, `return await (await fetch('/sw.js')).text();`)
	version := regexp.MustCompile(`const VERSION = '([0-9a-f]+)';`).FindStringSubmatch(sw)
	if version == nil {
		t.Fatalf("no version in /sw.js")
	}
	entries := probe(t, ctx, `
		await fetch('/static/code.css?v=old'); await fetch('/static/code.css?v=new'); await fetch('/static/code.css?v=new');
		const cache = await caches.open('clowder-static-`+version[1]+`');
		return JSON.stringify((await cache.keys()).map((r) => new URL(r.url).pathname + new URL(r.url).search));
	`)
	var keys []string
	if err := json.Unmarshal([]byte(entries), &keys); err != nil {
		t.Fatalf("the keys %q: %v", entries, err)
	}
	count := func(want string) int {
		n := 0
		for _, k := range keys {
			if k == want {
				n++
			}
		}
		return n
	}
	for _, want := range []string{"/static/app.css?v=" + version[1], "/static/datastar.js?v=" + version[1], "/static/code.css?v=old", "/static/code.css?v=new"} {
		if count(want) != 1 {
			t.Errorf("the cache holds %q %d times, want once: %v", want, count(want), keys)
		}
	}
	// The fonts are named by url() in app.css, which cannot carry the version: they
	// are the one unversioned kind, and a new worker drops the old cache.
	for _, k := range keys {
		if strings.HasPrefix(k, "/static/") && !strings.HasPrefix(k, "/static/fonts/") && !strings.Contains(k, "?v=") {
			t.Errorf("the page asked for %q without the version", k)
		}
	}

	// A static file is answered through the worker, from its cache.
	if got := probe(t, ctx, `return (await (await fetch('/static/app.css')).text()).length > 1000;`); got != "true" {
		t.Errorf("app.css from the worker is not the stylesheet: %s", got)
	}
}

// TestServiceWorkerSourceRules runs the source of /sw.js in the page against a
// fake worker scope and fake caches, as the browser would call it: the fetch
// handler answers (respondWith) a GET of this site under /static/ and nothing
// else (pages, /api/, /files/, /avatars/, a stream, another method, another
// site, a range request), and the activate event deletes the caches of other
// versions (and only the caches of this worker, named clowder-static-).
func TestServiceWorkerSourceRules(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/login"), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatal(err)
	}
	got := probe(t, ctx, `
		const src = await (await fetch('/sw.js')).text();
		const handlers = {};
		const events = [];
		const self = { location: { origin: location.origin }, addEventListener: (name, fn) => { handlers[name] = fn; },
			skipWaiting: async () => { events.push('skipWaiting'); }, clients: { claim: async () => { events.push('claim'); } } };
		const store = new Map([['clowder-static-oldversion', new Map()], ['clowder-static-other', new Map()], ['someone-elses', new Map()]]);
		const cacheOf = (m) => ({ match: async (req) => m.get(req.url), put: async (req, res) => { m.set(req.url, res); } });
		const fakeCaches = { keys: async () => [...store.keys()], delete: async (n) => store.delete(n),
			open: async (n) => { if (!store.has(n)) { store.set(n, new Map()); } return cacheOf(store.get(n)); } };
		new Function('self', 'caches', src)(self, fakeCaches);
		const version = /const VERSION = '([0-9a-f]+)'/.exec(src)[1];

		const answered = {};
		const ask = async (label, url, init) => {
			let called = false;
			handlers.fetch({ request: new Request(url, init), respondWith: (p) => { called = true; Promise.resolve(p).catch(() => {}); } });
			answered[label] = called;
		};
		await ask('page', '/rooms/1');
		await ask('home', '/');
		await ask('api', '/api/v1/rooms');
		await ask('file', '/files/1/a.png');
		await ask('message file', '/messages/files/1/a.png');
		await ask('avatar', '/avatars/2?v=1');
		await ask('stream', '/rooms/1/stream');
		await ask('manifest', '/manifest.webmanifest');
		await ask('sw.js', '/sw.js');
		await ask('static post', '/static/app.css', { method: 'POST' });
		await ask('static range', '/static/datastar.js', { headers: { Range: 'bytes=0-9' } });
		await ask('other site', 'https://other.example/static/app.css');
		await ask('static css', '/static/app.css');
		await ask('static font', '/static/fonts/nunito/latin.woff2');

		await new Promise((r) => setTimeout(r, 400));
		// The activate event: the old caches go, the claim happens.
		const waits = [];
		handlers.activate({ waitUntil: (p) => waits.push(p) });
		await Promise.all(waits);
		return JSON.stringify({ version, answered, caches: [...store.keys()], events });
	`)
	var res struct {
		Version  string          `json:"version"`
		Answered map[string]bool `json:"answered"`
		Caches   []string        `json:"caches"`
		Events   []string        `json:"events"`
	}
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatalf("the probe answered %q: %v", got, err)
	}
	for label, answered := range res.Answered {
		want := strings.HasPrefix(label, "static ") && label != "static post" && label != "static range"
		if answered != want {
			t.Errorf("the worker answered %s = %v, want %v", label, answered, want)
		}
	}
	if len(res.Answered) != 14 {
		t.Errorf("%d requests were asked, want 14", len(res.Answered))
	}
	// Only the cache of this version (made when static files were asked for) and
	// the caches that are not the worker's are left.
	wantCaches := map[string]bool{"clowder-static-" + res.Version: true, "someone-elses": true}
	if len(res.Caches) != len(wantCaches) {
		t.Errorf("the caches after activation are %v, want %v", res.Caches, wantCaches)
	}
	for _, name := range res.Caches {
		if !wantCaches[name] {
			t.Errorf("the cache %q is left after activation", name)
		}
	}
	if len(res.Events) != 1 || res.Events[0] != "claim" {
		t.Errorf("events = %v, want the claim of the clients", res.Events)
	}
}

// TestServiceWorkerOnTheLoginPage: a page that is neither the page
// of a logged-in person nor the login page does not register the worker.
func TestServiceWorkerOnTheLoginPage(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{InviteMember: true})
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/invite/"+srv.InviteToken), chromedp.WaitVisible("form")); err != nil {
		t.Fatal(err)
	}
	if got := probe(t, ctx, `await new Promise((r) => setTimeout(r, 1500)); return (await navigator.serviceWorker.getRegistrations()).length;`); got != "0" {
		t.Errorf("the sign-up page registered %s workers, want none", got)
	}
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/login"), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatal(err)
	}
	controlledBy(t, ctx)
}

// TestLiveUpdatesUnderTheServiceWorker: a page that the worker controls still
// gets the live updates of its stream: bob's page, controlled by the worker, shows
// the post alice writes without a reload.
func TestLiveUpdatesUnderTheServiceWorker(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	openGeneral(t, bob)
	controlledBy(t, bob)
	// Load the room again, now under the worker, and keep that page.
	if err := chromedp.Do(bob, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible(".room-card")); err != nil {
		t.Fatal(err)
	}
	openGeneral(t, bob)
	controlledBy(t, bob)
	openGeneral(t, alice)
	runJS(t, bob, `window.notReloaded = true; true`)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	const text = "Written while bob's page is under the service worker"
	alicePostsPlain(t, alice, text)
	first := `#feed-posts article.post:first-child`
	if err := waitFor(bob, `document.querySelector('`+first+` .post-body') !== null && document.querySelector('`+first+` .post-body').textContent.includes('Written while bob')`); err != nil {
		t.Fatalf("bob's page, under the worker, does not show the post: %v", err)
	}
	if ok, err := chromedp.Run(bob, chromedp.Evaluate[bool](`window.notReloaded === true && navigator.serviceWorker.controller !== null`)); err != nil || !ok {
		t.Errorf("bob's page was reloaded or is not under the worker (%v)", err)
	}
}
