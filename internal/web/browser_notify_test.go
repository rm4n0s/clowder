//go:build clowder_browser

package web_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// ringEntry is one decision that notify.js logged in a tab.
type ringEntry struct {
	Kind     string `json:"kind"`
	Sound    string `json:"sound"`
	Notified bool   `json:"notified"`
	Played   bool   `json:"played"`
}

// startRingLog makes the tab keep the log of notify.js and lets it count the
// tab as touched, as a person's first click does (the browser plays nothing
// before that).
func startRingLog(t *testing.T, ctx context.Context) {
	t.Helper()
	runJS(t, ctx, `window.__clowderNotifyLog = []; true`)
	if err := chromedp.Do(ctx, chromedp.MouseClickXY(5, 5)); err != nil {
		t.Fatalf("touch the page: %v", err)
	}
}

// ringLog reads the log of the tab.
func ringLog(t *testing.T, ctx context.Context) []ringEntry {
	t.Helper()
	raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](`JSON.stringify(window.__clowderNotifyLog || [])`))
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	var out []ringEntry
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode the log %q: %v", raw, err)
	}
	return out
}

// ringsAcross returns the decisions of all the tabs together.
func ringsAcross(t *testing.T, tabs ...context.Context) []ringEntry {
	t.Helper()
	var all []ringEntry
	for _, tab := range tabs {
		all = append(all, ringLog(t, tab)...)
	}
	return all
}

// waitForRings waits until the tabs together have logged n decisions.
func waitForRings(t *testing.T, n int, tabs ...context.Context) []ringEntry {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		got := ringsAcross(t, tabs...)
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// requireNoRing fails when a tab logs a decision within the time.
func requireNoRing(t *testing.T, what string, wait time.Duration, tabs ...context.Context) {
	t.Helper()
	time.Sleep(wait)
	if got := ringsAcross(t, tabs...); len(got) != 0 {
		t.Errorf("%s: %d decisions were logged, want none: %+v", what, len(got), got)
	}
}

// resetRingLogs empties the log of the tabs.
func resetRingLogs(t *testing.T, tabs ...context.Context) {
	t.Helper()
	for _, tab := range tabs {
		runJS(t, tab, `window.__clowderNotifyLog.length = 0; true`)
	}
}

// alicePostsPlainly has alice write a post in "general" that mentions nobody.
func alicePostsPlainly(t *testing.T, alice context.Context, text string) {
	t.Helper()
	err := chromedp.Do(alice,
		browsertest.Fill("#post-body-new", text),
		chromedp.Click(`#post-editor-new button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("alice posts: %v", err)
	}
}

// TestRingsAreOnePerEventAndFollowThePreferences has bob with two tabs of the
// same browser and alice writing: one tab rings (the log of notify.js has one
// decision, with the sound bob chose), a muted room rings in none, and the
// conversation that is open in the focused tab rings in none.
func TestRingsAreOnePerEventAndFollowThePreferences(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	openGeneral(t, alice)

	// Bob chooses, on the settings page: notify about all new activity, the
	// sound Chirp.
	openSettings(t, bob, srv)
	err := chromedp.Do(bob,
		chromedp.Click(`#notify-all`),
		chromedp.Click(`#sound-chirp`),
		chromedp.Click(`#notifications-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("bob saves the notification settings: %v", err)
	}
	if err := waitFor(bob, `document.querySelector('#notifications .saved') !== null`); err != nil {
		t.Fatalf("the notifications section does not say Saved: %v", err)
	}
	t.Logf("bob chose: all new activity, sound %s", textOf(t, bob, `label[for="sound-chirp"]`))

	// Two tabs of bob's browser: the home page and the search page. Neither
	// shows "general", so a post there rings in one of them.
	if err := chromedp.Do(bob, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#notification-count")); err != nil {
		t.Fatalf("bob opens the home page: %v", err)
	}
	tab2, cancelTab2 := chromedp.NewContext(bob)
	defer cancelTab2()
	if err := chromedp.Do(tab2, chromedp.Navigate(srv.URL+"/search"), chromedp.WaitVisible("#search-form")); err != nil {
		t.Fatalf("bob's second tab: %v", err)
	}
	startRingLog(t, bob)
	startRingLog(t, tab2)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	// 1. A post in "general": both tabs hear it, one of them rings.
	alicePostsPlainly(t, alice, "news for the whole room")
	waitForRings(t, 1, bob, tab2)
	time.Sleep(900 * time.Millisecond)
	got := ringsAcross(t, bob, tab2)
	t.Logf("a post in general, two tabs open: %+v", got)
	if len(got) != 1 {
		t.Fatalf("%d decisions for a post in two tabs, want exactly 1: %+v", len(got), got)
	}
	if got[0].Kind != "post.created" || got[0].Sound != "chirp" {
		t.Errorf("the decision = %+v, want a post.created with the sound chirp", got[0])
	}

	// 2. Bob mutes "general" in the dialog of the room page; the next post
	// rings in no tab.
	openGeneral(t, tab2)
	startRingLog(t, tab2)
	err = chromedp.Do(tab2,
		chromedp.Click(`#place-prefs-open`),
		chromedp.WaitVisible(`#place-prefs-dialog[open] #place-muted`),
		chromedp.Click(`#place-muted`),
		chromedp.Click(`#place-prefs-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("bob mutes the room: %v", err)
	}
	if err := waitFor(tab2, `document.querySelector('#place-prefs-form .saved') !== null`); err != nil {
		t.Fatalf("the dialog does not say Saved: %v", err)
	}
	if err := chromedp.Do(tab2, chromedp.Click(`#place-prefs-dialog .place-prefs-close button`)); err != nil {
		t.Fatalf("bob closes the dialog: %v", err)
	}
	if err := waitFor(tab2, `!document.getElementById('place-prefs-dialog').open`); err != nil {
		t.Fatalf("the dialog stays open: %v", err)
	}
	// The sidebar dims the room and moves it to the end, in both tabs.
	for name, tab := range map[string]context.Context{"home": bob, "general": tab2} {
		if err := waitFor(tab, `(function(){var li=document.querySelector('#sidebar-rooms li.muted a[data-room="general"]'); return !!li && li.parentElement===li.parentElement.parentElement.lastElementChild;})()`); err != nil {
			t.Errorf("the %s tab does not show the muted room last: %v", name, err)
		}
	}
	resetRingLogs(t, bob, tab2)
	alicePostsPlainly(t, alice, "news nobody hears")
	requireNoRing(t, "a post in a muted room", 1500*time.Millisecond, bob, tab2)
	t.Logf("a post in the muted room general: %+v", ringsAcross(t, bob, tab2))

	// 3. The direct conversation is open in the focused tab: a message there
	// rings in no tab; a message of the group, which is not open, rings once.
	// Headless tabs have no window focus of their own, so the test says which
	// tab has it, as the browser would.
	runJS(t, bob, `document.hasFocus = function () { return false; }; true`)
	openConversation(t, tab2, directConversation)
	runJS(t, tab2, `document.hasFocus = function () { return true; }; true`)
	startRingLog(t, tab2)
	resetRingLogs(t, bob, tab2)
	if err := chromedp.Do(tab2, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	openConversation(t, alice, directConversation)
	sendMessage(t, alice, "are you there, bob?")
	requireNoRing(t, "a message of the conversation that is open in the focused tab", 1500*time.Millisecond, bob, tab2)
	t.Logf("a message in the direct conversation open in the focused tab: %+v", ringsAcross(t, bob, tab2))

	openConversation(t, alice, crewConversation)
	sendMessage(t, alice, "crew, assemble")
	waitForRings(t, 1, bob, tab2)
	time.Sleep(900 * time.Millisecond)
	got = ringsAcross(t, bob, tab2)
	t.Logf("a message in the group, which is not open: %+v", got)
	if len(got) != 1 || got[0].Kind != "message.created" || got[0].Sound != "chirp" {
		t.Errorf("decisions = %+v, want one message.created with the sound chirp", got)
	}
	for _, e := range got {
		if strings.Contains(e.Kind, "crew, assemble") {
			t.Error("the body is in the log")
		}
	}
}

// TestBrowserNotificationsAskForThePermission has carol turn the browser
// notifications on: the page asks the browser, and when the browser does not
// give the permission (a headless one never does) the box is unchecked again
// and says why. The Play buttons of the sounds do not break the page.
func TestBrowserNotificationsAskForThePermission(t *testing.T) {
	srv := browsertest.Start(t)
	carol := secondBrowser(t, srv, "carol")
	openSettings(t, carol, srv)
	runJS(t, carol, `window.__clowderNotifyLog = []; true`)

	for _, name := range []string{"purr", "chirp", "bell", "meow"} {
		if err := chromedp.Do(carol, chromedp.Click(`[data-play-sound="`+name+`"]`)); err != nil {
			t.Fatalf("play %s: %v", name, err)
		}
	}
	if err := chromedp.Do(carol, chromedp.Click(`#notify-browser`)); err != nil {
		t.Fatalf("turn the browser notifications on: %v", err)
	}
	if err := waitFor(carol, `document.getElementById('notify-browser').checked === false`); err != nil {
		t.Fatalf("the box stays checked although the browser gave no permission: %v", err)
	}
	hint := textOf(t, carol, "#notify-browser-hint")
	if !strings.Contains(hint, "permission") && !strings.Contains(hint, "blocked") {
		t.Errorf("the hint = %q, want the reason", hint)
	}
	if got := ringLog(t, carol); len(got) != 0 {
		t.Errorf("playing a sound logged a decision: %+v", got)
	}
}
