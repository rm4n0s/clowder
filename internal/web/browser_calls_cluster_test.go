//go:build clowder_browser

package web_test

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// startCallAcrossProcesses has alice, whose page is on the first process, call
// bob, whose page is on the second, and bob answer with video: the call's home
// is the first process, bob's signalling socket reaches the second and is
// relayed through Redis, and the media of both goes to the first one's port
// (SPEC D65). It returns the two pages, once each plays the other's video.
func startCallAcrossProcesses(t *testing.T, a, b *browsertest.Server) (alice, bob context.Context) {
	t.Helper()
	alice = callBrowser(t, a, "alice")
	bob = callBrowser(t, b, "bob")
	openConversationPage(t, alice, directConversation)
	if err := chromedp.Do(bob, chromedp.Navigate(b.URL+"/search"), chromedp.WaitVisible("#search-form")); err != nil {
		t.Fatalf("bob opens the search page: %v", err)
	}
	aliceCalls(t, alice, "video")
	// The ringing reaches bob's page on the other process through Redis.
	requireWait(t, "bob", bob, "no dialog", dialogUp)
	if err := chromedp.Do(bob, chromedp.Click(`[data-call-answer="video"]`), chromedp.WaitVisible(`#call-panel[data-state="active"]`)); err != nil {
		t.Fatalf("bob answers: %v", err)
	}
	requireWait(t, "bob", bob, "bob does not take part", `document.querySelector('#call-panel').dataset.owner === '1'`)
	requireWait(t, "alice", alice, "alice does not take part", `document.querySelector('#call-panel[data-state="active"]') !== null && document.querySelector('#call-panel').dataset.owner === '1'`)
	requirePlaying(t, "alice", alice, "#call-remote")
	requirePlaying(t, "bob", bob, "#call-remote")
	return alice, bob
}

// TestACallBetweenTwoProcesses has alice and bob, on two processes of one
// cluster, see each other: each page plays the other's video, the marks follow
// the microphone across the relay, and hanging up on the process that does not
// hold the call ends it for both and writes one entry.
func TestACallBetweenTwoProcesses(t *testing.T) {
	a, b := startTwoServersWith(t, browsertest.Options{Calls: true})
	alice, bob := startCallAcrossProcesses(t, a, b)

	// bob's microphone mark crosses from the second process to the first one's
	// call and back to alice's page.
	if err := chromedp.Do(bob, chromedp.Click(`[data-call-control="mic"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "alice", alice, "no microphone mark", `!document.querySelector('.call-mark-mic').hidden`)
	// A screen share of the page on the first process keeps bob's video going.
	if err := chromedp.Do(alice, chromedp.Click(`[data-call-control="screen"]`)); err != nil {
		t.Fatal(err)
	}
	requireWait(t, "alice", alice, "the screen is not shared", `document.querySelector('#call-panel').dataset.sharing === '1'`)
	requirePlaying(t, "bob", bob, "#call-remote")

	// bob hangs up on the second process.
	if err := chromedp.Do(bob, chromedp.Click(`[data-call-hangup]`)); err != nil {
		t.Fatal(err)
	}
	for who, ctx := range map[string]context.Context{"alice": alice, "bob": bob} {
		requireWait(t, who, ctx, "the call view stays", `document.querySelector('#call-panel') === null`)
		requireWait(t, who, ctx, "no call entry", `document.querySelectorAll('#message-list .message-call-entry').length === 1`)
	}
	t.Logf("alice's call log: %v", callLog(t, alice))
	t.Logf("bob's call log: %v", callLog(t, bob))
}

// TestTheCallEndsForBobWhenItsHomeStops stops the process that holds the call
// while bob's page, on the other process, is in it: the home ends its calls as
// it stops, and bob's page leaves the call.
func TestTheCallEndsForBobWhenItsHomeStops(t *testing.T) {
	a, b := startTwoServersWith(t, browsertest.Options{Calls: true})
	_, bob := startCallAcrossProcesses(t, a, b)
	if err := a.Stop(); err != nil {
		t.Fatalf("stopping the first process: %v", err)
	}
	requireWait(t, "bob", bob, "the call view stays after the home stopped", `document.querySelector('#call-panel') === null`)
	requireWait(t, "bob", bob, "no call entry", `document.querySelectorAll('#message-list .message-call-entry').length === 1`)
	if got := callLog(t, bob); len(got) == 0 {
		t.Error("bob's page logged nothing")
	}
}
