//go:build clowder_browser

package web_test

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// callTestTimeout bounds one test of the calls: two browsers, a call and the
// waits for the media.
const callTestTimeout = 3 * time.Minute

// callBrowser starts a Chromium with fake devices (browsertest.FakeMedia) for
// the user, logged in.
func callBrowser(t *testing.T, srv *browsertest.Server, user string) context.Context {
	t.Helper()
	browser, cancel, err := browsertest.NewBrowser(context.Background(), browsertest.FakeMedia()...)
	if err != nil {
		t.Fatalf("browser of %s: %v", user, err)
	}
	t.Cleanup(cancel)
	ctx, cancelCtx := context.WithTimeout(browser, callTestTimeout)
	t.Cleanup(cancelCtx)
	// Every page this browser opens keeps the logs of the scripts of the calls
	// and of the ringing, which a test reads.
	logs := func(ctx context.Context, tg *chromedp.Target) (chromedp.Void, error) {
		_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
			Source: "window.__clowderCallLog = []; window.__clowderNotifyLog = [];",
		})
		return chromedp.Void{}, err
	}
	if err := chromedp.Do(ctx, chromedp.Action[chromedp.Void](logs)); err != nil {
		t.Fatalf("log of %s: %v", user, err)
	}
	if err := browsertest.LogIn(ctx, srv, user); err != nil {
		t.Fatalf("LogIn %s: %+v", user, err)
	}
	return ctx
}

// openConversationPage navigates to the conversation of the members from the
// Messages page and waits for its call area.
func openConversationPage(t *testing.T, ctx context.Context, members string) {
	t.Helper()
	openConversation(t, ctx, members)
	if err := waitFor(ctx, `document.getElementById('call-area') !== null`); err != nil {
		t.Fatalf("the conversation has no call area: %v", err)
	}
	runJS(t, ctx, `window.__clowderCallLog = window.__clowderCallLog || []; true`)
}

// callLog returns what call.js logged in the page.
func callLog(t *testing.T, ctx context.Context) []string {
	t.Helper()
	raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](`JSON.stringify(window.__clowderCallLog || [])`))
	if err != nil {
		t.Fatalf("read the call log: %v", err)
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode the call log %q: %v", raw, err)
	}
	return out
}

// requireWait waits for the expression and fails with the call log when it does
// not become true.
func requireWait(t *testing.T, who string, ctx context.Context, what, expr string) {
	t.Helper()
	if err := waitFor(ctx, expr); err != nil {
		t.Fatalf("%s: %s (%v); call log: %v", who, what, err, callLog(t, ctx))
	}
}

// playing is the expression that is true when the video element plays: it has
// a picture and its clock advances between two readings.
func playing(selector string) string {
	return `(() => {
		const v = document.querySelector(` + strconv.Quote(selector) + `);
		if (!v || !v.videoWidth || v.paused) { window.__t0 = undefined; return false; }
		if (window.__t0 === undefined || window.__t0.sel !== ` + strconv.Quote(selector) + `) { window.__t0 = {sel: ` + strconv.Quote(selector) + `, t: v.currentTime}; return false; }
		return v.currentTime > window.__t0.t + 0.2;
	})()`
}

// requirePlaying waits until the remote video of the page plays.
func requirePlaying(t *testing.T, who string, ctx context.Context, selector string) {
	t.Helper()
	runJS(t, ctx, `window.__t0 = undefined; true`)
	requireWait(t, who, ctx, "the video "+selector+" does not play", playing(selector))
}

// TestAliceVideoCallsBob has alice call bob with video from their conversation
// while bob is on another page: his page shows the dialog, he answers with
// video, each page plays the other's video, the marks follow the microphone and
// the camera, a screen share keeps the video going, and hanging up leaves the
// entry of the call in both conversations.
func TestAliceVideoCallsBob(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	openConversationPage(t, alice, directConversation)
	if err := chromedp.Do(bob, chromedp.Navigate(srv.URL+"/search"), chromedp.WaitVisible("#search-form")); err != nil {
		t.Fatalf("bob opens the search page: %v", err)
	}
	runJS(t, bob, `window.__clowderNotifyLog = []; true`)

	// Alice calls with video: "Calling Bob…" with Cancel replaces the buttons.
	if got := countOf(t, alice, `#call-buttons [data-call-start]`); got != 2 {
		t.Fatalf("the conversation has %d call buttons, want 2", got)
	}
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-start="video"]`), chromedp.WaitVisible(`#call-panel[data-state="ringing"] [data-call-cancel]`)); err != nil {
		t.Fatalf("alice calls: %v", err)
	}
	if got := textOf(t, alice, `.call-calling-line`); !strings.HasPrefix(got, "Calling Bob") {
		t.Errorf("alice's panel says %q, want Calling Bob…", got)
	}
	if got := countOf(t, alice, `#call-buttons [data-call-start]`); got != 0 {
		t.Errorf("the call buttons stay while the call rings (%d)", got)
	}

	// Bob's page has the dialog, modal, with the three choices.
	requireWait(t, "bob", bob, "no dialog", `(() => { const d = document.querySelector('dialog.call-dialog'); return d !== null && d.open; })()`)
	if got := textOf(t, bob, `#call-dialog-title`); got != "Alice is calling you" {
		t.Errorf("the dialog says %q", got)
	}
	runJS(t, bob, `document.querySelector('dialog.call-dialog').matches(':modal')`)
	for _, sel := range []string{`[data-call-answer="video"]`, `[data-call-answer="audio"]`, `[data-call-decline]`} {
		if countOf(t, bob, `dialog.call-dialog `+sel) != 1 {
			t.Errorf("the dialog lacks %s", sel)
		}
	}
	if got := textOf(t, bob, `.call-dialog-kind`); !strings.Contains(got, "Video call") {
		t.Errorf("the dialog kind is %q", got)
	}

	// Bob answers with video; his page opens the conversation with the call.
	if err := chromedp.Do(bob, chromedp.Click(`[data-call-answer="video"]`), chromedp.WaitVisible(`#call-panel[data-state="active"]`)); err != nil {
		t.Fatalf("bob answers: %v", err)
	}
	requireWait(t, "bob", bob, "bob does not take part", `document.querySelector('#call-panel').dataset.owner === '1'`)
	requireWait(t, "alice", alice, "alice's panel is not the call view", `document.querySelector('#call-panel[data-state="active"]') !== null`)
	requireWait(t, "alice", alice, "alice does not take part", `document.querySelector('#call-panel').dataset.owner === '1'`)
	if countOf(t, bob, `dialog.call-dialog`) != 0 {
		t.Errorf("the dialog stays after bob answered")
	}

	// Each side plays the other's video.
	requirePlaying(t, "alice", alice, "#call-remote")
	requirePlaying(t, "bob", bob, "#call-remote")
	if got := textOf(t, alice, `.call-name`); got != "Bob" {
		t.Errorf("alice's call view names %q", got)
	}
	requireWait(t, "alice", alice, "the duration does not move", `/^\d\d:\d\d$/.test(document.querySelector('[data-call-duration]').textContent) && document.querySelector('[data-call-duration]').textContent !== '00:00'`)

	// Alice mutes her microphone: bob's page marks it. Then she turns the
	// camera off: the mark shows and bob's page shows her avatar.
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-control="mic"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "bob", bob, "no microphone mark", `!document.querySelector('.call-mark-mic').hidden`)
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-control="camera"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "bob", bob, "no camera mark", `!document.querySelector('.call-mark-cam').hidden && document.querySelector('#call-panel').dataset.peerVideo === '0'`)
	if got := textOf(t, alice, `[data-call-control="camera"]`); got != "" && countOf(t, alice, `[data-call-control="camera"][aria-pressed="false"]`) != 1 {
		t.Errorf("alice's camera button does not show that the camera is off")
	}
	// She unmutes and turns the camera on again.
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-control="mic"]`), chromedp.Click(`[data-call-control="camera"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "bob", bob, "the marks stay", `document.querySelector('.call-mark-mic').hidden && document.querySelector('.call-mark-cam').hidden`)
	requirePlaying(t, "bob", bob, "#call-remote")

	// Alice shares her screen: bob keeps receiving video.
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-control="screen"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "alice", alice, "the screen is not shared", `document.querySelector('#call-panel').dataset.sharing === '1'`)
	requirePlaying(t, "bob", bob, "#call-remote")
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-control="screen"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "alice", alice, "the sharing does not stop", `document.querySelector('#call-panel').dataset.sharing === '0'`)
	requirePlaying(t, "bob", bob, "#call-remote")

	// Hanging up ends the call in both pages and writes its entry.
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-hangup]`)); err != nil {
		t.Fatal(err)
	}
	for who, ctx := range map[string]context.Context{"alice": alice, "bob": bob} {
		requireWait(t, who, ctx, "the call view stays", `document.querySelector('#call-panel') === null`)
		requireWait(t, who, ctx, "no call entry", `(() => { const e = document.querySelector('#message-list .message-call-entry'); return e !== null && /^Video call · \d+ s$/.test(e.textContent); })()`)
		if countOf(t, ctx, `#call-buttons [data-call-start]`) != 2 {
			t.Errorf("%s: the call buttons do not come back", who)
		}
	}
	t.Logf("alice's call log: %v", callLog(t, alice))
	t.Logf("bob's call log: %v", callLog(t, bob))
}

// dialogUp is the expression that is true when the ringing dialog of the page
// is open.
const dialogUp = `(() => { const d = document.querySelector('dialog.call-dialog'); return d !== null && d.open; })()`

// dialogGone is the expression that is true when the page has no dialog.
const dialogGone = `document.querySelector('dialog.call-dialog') === null`

// aliceCalls has alice press the button of the call in her conversation with
// bob and wait for "Calling Bob…".
func aliceCalls(t *testing.T, alice context.Context, way string) {
	t.Helper()
	err := chromedp.Do(alice,
		chromedp.Click(`[data-call-start="`+way+`"]`),
		chromedp.WaitVisible(`#call-panel[data-state="ringing"] [data-call-cancel]`),
	)
	if err != nil {
		t.Fatalf("alice calls (%s): %v", way, err)
	}
}

// onSearchPage opens a page of Clowder that is not the conversation.
func onSearchPage(t *testing.T, srv *browsertest.Server, ctx context.Context) {
	t.Helper()
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/search"), chromedp.WaitVisible("#search-form")); err != nil {
		t.Fatalf("open the search page: %v", err)
	}
}

// entryText is the expression that reads the text of the newest call entry of
// the list.
const entryText = `(() => { const all = document.querySelectorAll('#message-list .message-call-entry'); return all.length ? all[all.length - 1].textContent : ''; })()`

// TestBobDeclinesAndAliceSeesIt has bob decline from the dialog on another page:
// the dialog goes, alice's "Calling Bob…" goes with it, and both conversations
// get the declined entry.
func TestBobDeclinesAndAliceSeesIt(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	openConversationPage(t, alice, directConversation)
	onSearchPage(t, srv, bob)

	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	if err := chromedp.Do(bob, chromedp.Click(`[data-call-decline]`)); err != nil {
		t.Fatalf("bob declines: %v", err)
	}
	requireWait(t, "bob", bob, "the dialog stays", dialogGone)
	requireWait(t, "alice", alice, "the panel stays", `document.querySelector('#call-panel') === null`)
	requireWait(t, "alice", alice, "no declined entry", entryText+` === 'Declined video call'`)
	if countOf(t, alice, `#call-buttons [data-call-start]`) != 2 {
		t.Errorf("alice's buttons do not come back")
	}
	openConversationPage(t, bob, directConversation)
	requireWait(t, "bob", bob, "no declined entry", entryText+` === 'Declined video call'`)
}

// TestAnUnansweredCallIsMissed has a call ring for 3 seconds and nobody answer:
// the dialog goes away by itself, alice sees "No answer", and bob has the missed
// call in the colour of a warning.
func TestAnUnansweredCallIsMissed(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true, CallRingTimeout: 3 * time.Second})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	openConversationPage(t, alice, directConversation)
	onSearchPage(t, srv, bob)

	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	requireWait(t, "bob", bob, "the dialog does not go when the call is missed", dialogGone)
	requireWait(t, "alice", alice, "the panel stays", `document.querySelector('#call-panel') === null`)
	requireWait(t, "alice", alice, "no entry for alice", entryText+` === 'No answer'`)
	openConversationPage(t, bob, directConversation)
	requireWait(t, "bob", bob, "no missed call", entryText+` === 'Missed video call'`)
	requireWait(t, "bob", bob, "the missed call is not in the colour of a warning", `(() => {
		const e = document.querySelector('#message-list .call-entry-missed .message-call-entry');
		if (!e) { return false; }
		const probe = document.createElement('span');
		probe.style.color = 'var(--c-danger)';
		document.body.appendChild(probe);
		const want = getComputedStyle(probe).color;
		probe.remove();
		return getComputedStyle(e).color === want;
	})()`)
}

// TestAnAudioCallShowsAvatarsAndTheCameraCanComeOn has alice call with audio
// only: both pages show the other's avatar and no video, and when alice turns
// her camera on, bob's page plays it on the same connection.
func TestAnAudioCallShowsAvatarsAndTheCameraCanComeOn(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	openConversationPage(t, alice, directConversation)
	onSearchPage(t, srv, bob)

	aliceCalls(t, alice, "audio")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	if got := textOf(t, bob, `.call-dialog-kind`); !strings.Contains(got, "Audio call") {
		t.Errorf("the dialog kind is %q", got)
	}
	if err := chromedp.Do(bob, chromedp.Click(`[data-call-answer="audio"]`), chromedp.WaitVisible(`#call-panel[data-state="active"]`)); err != nil {
		t.Fatalf("bob answers: %v", err)
	}
	for who, ctx := range map[string]context.Context{"alice": alice, "bob": bob} {
		requireWait(t, who, ctx, "the call is not connected", `document.querySelector('#call-panel[data-owner="1"][data-connected="1"]') !== null`)
		requireWait(t, who, ctx, "the other side is not an avatar", `document.querySelector('#call-panel').dataset.peerVideo === '0' && getComputedStyle(document.querySelector('.call-remote-avatar')).display !== 'none' && getComputedStyle(document.querySelector('#call-remote')).display === 'none'`)
		if countOf(t, ctx, `.call-panel[data-local-video="1"]`) != 0 {
			t.Errorf("%s: the user's own video shows in an audio call", who)
		}
		if countOf(t, ctx, `[data-call-control="camera"][aria-pressed="false"]`) != 1 {
			t.Errorf("%s: the camera button does not show that the camera is off", who)
		}
	}
	// The audio goes both ways: the remote audio track of each page is live.
	requireWait(t, "alice", alice, "no audio from bob", `(() => { const v = document.querySelector('#call-remote'); const t = v.srcObject && v.srcObject.getAudioTracks()[0]; return !!t && t.readyState === 'live' && !t.muted; })()`)

	// Alice turns the camera on.
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-control="camera"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "bob", bob, "the camera of alice does not show", `document.querySelector('#call-panel').dataset.peerVideo === '1'`)
	requirePlaying(t, "bob", bob, "#call-remote")
	requireWait(t, "alice", alice, "her own picture does not show", `document.querySelector('#call-panel').dataset.localVideo === '1'`)
	t.Logf("alice's call log: %v", callLog(t, alice))
}

// TestTheRingtoneFollowsThePreferences reads what notify.js decided in bob's
// tab: with the defaults the ringtone *Purr ring* plays, and it does for a user
// whose notification sound is off; for a muted conversation the dialog shows and
// nothing plays.
func TestTheRingtoneFollowsThePreferences(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	openConversationPage(t, alice, directConversation)
	convPath := textOfURL(t, alice)
	onSearchPage(t, srv, bob)
	post := func(path, body string) {
		t.Helper()
		runJS(t, bob, `(() => { const x = new XMLHttpRequest(); x.open('POST', `+strconv.Quote(path)+`, false); x.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded'); x.send(`+strconv.Quote(body)+`); return x.status < 400; })()`)
	}
	waitLog := func(want func(e ringEntry) bool) []ringEntry {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var got []ringEntry
			for _, e := range ringLog(t, bob) {
				if e.Kind == "call.ringing" {
					got = append(got, e)
				}
			}
			if len(got) > 0 && want(got[0]) || time.Now().After(deadline) {
				return got
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	cancel := func() {
		t.Helper()
		if err := chromedp.Do(alice, chromedp.Click(`[data-call-cancel]`), chromedp.WaitVisible(`#call-buttons [data-call-start]`)); err != nil {
			t.Fatalf("alice cancels: %v", err)
		}
		requireWait(t, "bob", bob, "the dialog stays", dialogGone)
	}

	// The defaults: the ringtone plays, and the dialog is open and modal.
	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	got := waitLog(func(e ringEntry) bool { return e.Played })
	if len(got) != 1 || got[0].Sound != "purr-ring" || !got[0].Played {
		t.Fatalf("the ringtone did not play: %+v", got)
	}
	if modal, err := chromedp.Run(bob, chromedp.Evaluate[bool](`document.querySelector('dialog.call-dialog').matches(':modal')`)); err != nil || !modal {
		t.Errorf("the dialog is not modal (%v, %v)", modal, err)
	}
	if focused, err := chromedp.Run(bob, chromedp.Evaluate[string](`document.activeElement && document.activeElement.getAttribute('data-call-answer')`)); err != nil || focused != "video" {
		t.Errorf("the focus is on %q, want Answer with video (%v)", focused, err)
	}
	cancel()

	// A user who turned the notification sound off hears it all the same.
	post("/me/settings/notifications", "notify=mentions&soundName=purr")
	runJS(t, bob, `window.__clowderNotifyLog.length = 0; true`)
	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	if got := waitLog(func(e ringEntry) bool { return e.Played }); len(got) != 1 || !got[0].Played {
		t.Fatalf("the ringtone did not play with the sound off: %+v", got)
	}
	cancel()

	// A muted conversation: the dialog shows and nothing plays.
	post(convPath+"/notifications", "muted=on")
	time.Sleep(300 * time.Millisecond)
	runJS(t, bob, `window.__clowderNotifyLog.length = 0; true`)
	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog for the muted conversation", dialogUp)
	got = waitLog(func(e ringEntry) bool { return true })
	if len(got) != 1 || got[0].Sound != "" || got[0].Played {
		t.Fatalf("a muted conversation rang: %+v", got)
	}
	time.Sleep(1500 * time.Millisecond)
	if got := ringLog(t, bob); len(got) != 1 || got[0].Played {
		t.Errorf("a muted conversation rang later: %+v", got)
	}
	cancel()
}

// textOfURL returns the path of the page the browser has open.
func textOfURL(t *testing.T, ctx context.Context) string {
	t.Helper()
	p, err := chromedp.Run(ctx, chromedp.Evaluate[string](`location.pathname`))
	if err != nil {
		t.Fatalf("read the path: %v", err)
	}
	return p
}

// TestEscapeLeavesTheDialogAsACardInTheCorner has bob dismiss the dialog: the
// call still rings, the dialog stays as a card that does not block the page, and
// it goes when the call ends.
func TestEscapeLeavesTheDialogAsACardInTheCorner(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	openConversationPage(t, alice, directConversation)
	onSearchPage(t, srv, bob)
	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	if err := chromedp.Do(bob, chromedp.KeyEvent(kb.Escape)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "bob", bob, "the dialog is not a card", `(() => { const d = document.querySelector('dialog.call-dialog'); return d !== null && d.open && !d.matches(':modal'); })()`)
	// The page works under it.
	if err := chromedp.Do(bob, chromedp.Click(`#search-form input[name="q"]`)); err != nil {
		t.Errorf("the page is blocked by the card: %v", err)
	}
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-cancel]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "bob", bob, "the card stays", dialogGone)
}

// TestTheCallFitsAPhone has the call view at 390 by 844: nothing is wider than
// the screen, the controls are at least 44 px and fill the bottom, and the
// other side fills the screen.
func TestTheCallFitsAPhone(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	if err := chromedp.Do(alice, chromedp.EmulateViewport(390, 844)); err != nil {
		t.Fatal(err)
	}
	openConversationPage(t, alice, directConversation)
	onSearchPage(t, srv, bob)
	checkPhonePage(t, alice, "the conversation with the call buttons", `document.querySelector('[data-call-start="video"]') !== null`)
	aliceCalls(t, alice, "video")
	checkPhonePage(t, alice, "Calling Bob…", `document.querySelector('.call-calling-line') !== null`)
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	if err := chromedp.Do(bob, chromedp.Click(`[data-call-answer="video"]`), chromedp.WaitVisible(`#call-panel[data-state="active"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "alice", alice, "the call is not connected", `document.querySelector('#call-panel[data-owner="1"][data-connected="1"]') !== null`)
	requirePlaying(t, "alice", alice, "#call-remote")
	checkPhonePage(t, alice, "the call view", `document.querySelector('#call-panel[data-connected="1"]') !== null`)
	// The call fills the screen, and the controls sit at the bottom.
	box, err := chromedp.Run(alice, chromedp.Evaluate[string](`(() => { const p = document.querySelector('#call-panel').getBoundingClientRect(); const c = document.querySelector('.call-controls').getBoundingClientRect(); return [Math.round(p.width), Math.round(p.height), Math.round(c.bottom)].join(','); })()`))
	if err != nil || box != "390,844,844" {
		t.Errorf("the call view is %s (width, height, bottom of the controls), want 390,844,844 (%v)", box, err)
	}
	// The conversation is under the call: the button at the left of the controls
	// shrinks the call to a bar, and the conversation shows.
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-control="minimize"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "alice", alice, "the call does not shrink", `document.querySelector('#call-panel').dataset.minimized === '1' && document.querySelector('#call-panel').getBoundingClientRect().height < 200`)
	checkPhonePage(t, alice, "the call in a bar", `document.querySelector('#call-panel[data-minimized="1"]') !== null`)
	requirePlaying(t, "bob", bob, "#call-remote")
}

// refuseSoundBeforeATouch is a script that makes the AudioContext of the page
// behave as a browser with its autoplay rules does: it starts suspended and
// resume() is refused until the page was touched. (The headless Chromium of the
// tests plays sound at once whatever its autoplay policy says, so the refusal
// has to be made by the page.)
const refuseSoundBeforeATouch = `(() => {
	const Real = window.AudioContext;
	let touched = false;
	for (const name of ['pointerdown', 'keydown', 'touchstart']) {
		window.addEventListener(name, () => { touched = true; }, true);
	}
	window.AudioContext = function () {
		const ctx = new Real();
		const resume = ctx.resume.bind(ctx);
		ctx.suspend();
		ctx.resume = () => touched ? resume() : Promise.reject(new DOMException('not allowed', 'NotAllowedError'));
		return ctx;
	};
})();`

// TestTheRingtoneWaitsForATouchWhenTheBrowserRefusesSound records what happens
// when the browser keeps its autoplay rules (no sound before the page was
// touched): the dialog shows at once, the ringtone does not start, and it starts
// at the first touch of the page.
func TestTheRingtoneWaitsForATouchWhenTheBrowserRefusesSound(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	err := chromedp.Do(bob, chromedp.Action[chromedp.Void](func(ctx context.Context, tg *chromedp.Target) (chromedp.Void, error) {
		_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: refuseSoundBeforeATouch})
		return chromedp.Void{}, err
	}))
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	openConversationPage(t, alice, directConversation)
	onSearchPage(t, srv, bob)
	runJS(t, bob, `window.__clowderNotifyLog = []; true`)

	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	time.Sleep(2 * time.Second)
	state, err := chromedp.Run(bob, chromedp.Evaluate[string](`new AudioContext().state`))
	if err != nil || state != "suspended" {
		t.Fatalf("the page does not refuse sound (%q, %v)", state, err)
	}
	if got := ringLog(t, bob); len(got) != 0 {
		t.Fatalf("the ringtone played before the page was touched: %+v", got)
	}
	if !dialogShown(t, bob) {
		t.Fatal("the dialog is not shown while the browser refuses sound")
	}
	// The first touch starts it.
	if err := chromedp.Do(bob, chromedp.MouseClickXY(5, 5)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		for _, e := range ringLog(t, bob) {
			if e.Kind == "call.ringing" && e.Played {
				t.Logf("the ringtone started at the first touch: %+v", e)
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ringtone did not start after the touch; log: %+v", ringLog(t, bob))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// dialogShown reports whether the page shows the ringing dialog.
func dialogShown(t *testing.T, ctx context.Context) bool {
	t.Helper()
	ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](dialogUp))
	if err != nil {
		t.Fatalf("read the dialog: %v", err)
	}
	return ok
}

// grantNotifications lets the origin of srv show browser notifications.
func grantNotifications(t *testing.T, ctx context.Context, srv *browsertest.Server) {
	t.Helper()
	err := chromedp.Do(ctx, chromedp.Action[chromedp.Void](func(ctx context.Context, tg *chromedp.Target) (chromedp.Void, error) {
		_, err := cdp.Call(ctx, tg, browser.SetPermission, browser.SetPermissionParams{
			Permission: &browser.PermissionDescriptor{Name: "notifications"}, Setting: browser.PermissionSettingGranted, Origin: srv.URL,
		})
		return chromedp.Void{}, err
	}))
	if err != nil {
		t.Fatalf("grant the notifications: %v", err)
	}
}

// callRings returns the decisions of notify.js about calls, in all the tabs.
func callRings(t *testing.T, tabs ...context.Context) []ringEntry {
	t.Helper()
	var out []ringEntry
	for _, e := range ringsAcross(t, tabs...) {
		if e.Kind == "call.ringing" {
			out = append(out, e)
		}
	}
	return out
}

// TestACallNotifiesTheBrowserOnlyWhenNoPageHasTheFocus has bob, with the browser
// notifications on, in two tabs: the call rings in one of them, and shows the
// notification "Bob is calling you" only when no tab has the focus.
func TestACallNotifiesTheBrowserOnlyWhenNoPageHasTheFocus(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	openConversationPage(t, alice, directConversation)
	grantNotifications(t, bob, srv)
	runJS(t, bob, `(() => { const x = new XMLHttpRequest(); x.open('POST', '/me/settings/notifications', false); x.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded'); x.send('notify=mentions&soundName=purr&sound=on&browserNotifications=on'); return x.status < 400; })()`)
	onSearchPage(t, srv, bob)
	tab2, cancelTab2 := chromedp.NewContext(bob)
	t.Cleanup(cancelTab2)
	if err := chromedp.Do(tab2, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible("#sidebar")); err != nil {
		t.Fatalf("the second tab: %v", err)
	}
	// Headless tabs have no window focus of their own: the test says which tab has it.
	runJS(t, bob, `window.__clowderNotifyLog = []; document.hasFocus = () => false; true`)
	runJS(t, tab2, `window.__clowderNotifyLog = []; document.hasFocus = () => false; true`)
	cancel := func() {
		t.Helper()
		if err := chromedp.Do(alice, chromedp.Click(`[data-call-cancel]`), chromedp.WaitVisible(`#call-buttons [data-call-start]`)); err != nil {
			t.Fatalf("alice cancels: %v", err)
		}
		requireWait(t, "bob", bob, "the dialog stays", dialogGone)
		requireWait(t, "bob", tab2, "the dialog stays", dialogGone)
	}

	// No tab has the focus: both show the dialog, one rings, and the browser is notified.
	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	requireWait(t, "bob", tab2, "no dialog in the second tab", dialogUp)
	deadline := time.Now().Add(10 * time.Second)
	var got []ringEntry
	for len(got) < 1 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		got = callRings(t, bob, tab2)
	}
	time.Sleep(time.Second)
	got = callRings(t, bob, tab2)
	if len(got) != 1 || !got[0].Notified || !got[0].Played || got[0].Sound != "purr-ring" {
		t.Fatalf("decisions with no focus = %+v, want one that notified and rang", got)
	}
	cancel()
	runJS(t, bob, `window.__clowderNotifyLog.length = 0; true`)
	runJS(t, tab2, `window.__clowderNotifyLog.length = 0; true`)

	// A tab has the focus: the call rings, the browser is not notified.
	runJS(t, tab2, `document.hasFocus = () => true; true`)
	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	deadline = time.Now().Add(10 * time.Second)
	for len(callRings(t, bob, tab2)) < 1 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(time.Second)
	got = callRings(t, bob, tab2)
	if len(got) != 1 || got[0].Notified || !got[0].Played {
		t.Fatalf("decisions with a focused tab = %+v, want one that rang and did not notify", got)
	}
	cancel()
}

// TestTheCallReconnectsItsSocket has the signalling socket of alice's page close
// in the middle of the call (a client may close with a code of its own, which
// the script treats as a lost connection): the script opens it again, the server
// offers again, and both sides play the other's video once more.
func TestTheCallReconnectsItsSocket(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	// The page keeps the sockets it opens, so that the test can close one.
	err := chromedp.Do(alice, chromedp.Action[chromedp.Void](func(ctx context.Context, tg *chromedp.Target) (chromedp.Void, error) {
		_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
			Source: `(() => { const Real = window.WebSocket; window.__sockets = []; window.WebSocket = function (...a) { const s = new Real(...a); window.__sockets.push(s); return s; }; window.WebSocket.OPEN = Real.OPEN; })();`,
		})
		return chromedp.Void{}, err
	}))
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	openConversationPage(t, alice, directConversation)
	onSearchPage(t, srv, bob)
	aliceCalls(t, alice, "video")
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	if err := chromedp.Do(bob, chromedp.Click(`[data-call-answer="video"]`), chromedp.WaitVisible(`#call-panel[data-state="active"]`)); err != nil {
		t.Fatal(err)
	}
	requirePlaying(t, "alice", alice, "#call-remote")
	requirePlaying(t, "bob", bob, "#call-remote")

	runJS(t, alice, `window.__sockets[window.__sockets.length - 1].close(4000, 'test'); true`)
	requireWait(t, "alice", alice, "the socket is not opened again", `window.__sockets.length === 2 && window.__sockets[1].readyState === 1`)
	requireWait(t, "alice", alice, "the media does not connect again", `(window.__clowderCallLog.filter(e => e === 'pc-connected').length >= 2)`)
	requirePlaying(t, "alice", alice, "#call-remote")
	requirePlaying(t, "bob", bob, "#call-remote")
	if got := countOf(t, alice, `#call-panel[data-owner="1"]`); got != 1 {
		t.Errorf("alice no longer takes part in the call")
	}
	t.Logf("alice's call log: %v", callLog(t, alice))
}

// TestAnotherTabOfTheSameConversationShowsTheCallElsewhere has alice open the
// conversation in a second tab while the call runs in the first: the second tab
// shows that the call is in another window and has Hang up, and the call is not
// taken over by it.
func TestAnotherTabOfTheSameConversationShowsTheCallElsewhere(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{Calls: true})
	alice := callBrowser(t, srv, "alice")
	bob := callBrowser(t, srv, "bob")
	openConversationPage(t, alice, directConversation)
	tab2, cancelTab2 := chromedp.NewContext(alice)
	t.Cleanup(cancelTab2)
	if err := chromedp.Do(tab2, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#signed-in-as")); err != nil {
		t.Fatalf("the second tab: %v", err)
	}
	openConversationPage(t, tab2, directConversation)
	onSearchPage(t, srv, bob)

	aliceCalls(t, alice, "video")
	// The other tab shows the call as another window's, with Cancel.
	requireWait(t, "alice", tab2, "the second tab has no panel", `document.querySelector('#call-panel[data-state="ringing"][data-owner="0"]') !== null`)
	if got := callLog(t, tab2); len(got) != 0 && slices.Contains(got, "ringback-start") {
		t.Errorf("the second tab plays the ringback: %v", got)
	}
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	if err := chromedp.Do(bob, chromedp.Click(`[data-call-answer="video"]`), chromedp.WaitVisible(`#call-panel[data-state="active"]`)); err != nil {
		t.Fatal(err)
	}
	requirePlaying(t, "alice", alice, "#call-remote")
	requireWait(t, "alice", tab2, "the second tab takes part", `document.querySelector('#call-panel[data-state="active"][data-owner="0"]') !== null`)
	if got := callLog(t, tab2); slices.Contains(got, "socket-open") {
		t.Errorf("the second tab opened a socket: %v", got)
	}
	// It cannot control the call, but it can hang up.
	if got := countOf(t, tab2, `.call-panel[data-owner="0"] .call-controls > :not(.call-hangup-form)`); got == 0 {
		t.Fatal("the second tab has no controls to hide")
	}
	if shown, err := chromedp.Run(tab2, chromedp.Evaluate[bool](`getComputedStyle(document.querySelector('[data-call-control="mic"]')).display === 'none' && getComputedStyle(document.querySelector('[data-call-hangup]')).display !== 'none'`)); err != nil || !shown {
		t.Errorf("the second tab does not hide the controls and keep Hang up (%v, %v)", shown, err)
	}
	requirePlaying(t, "bob", bob, "#call-remote")
	if err := chromedp.Do(tab2, chromedp.Click(`[data-call-hangup]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "alice", alice, "the call does not end", `document.querySelector('#call-panel') === null`)
	requireWait(t, "bob", bob, "the call does not end", `document.querySelector('#call-panel') === null`)
}
