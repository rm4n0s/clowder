//go:build clowder_browser

package web_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// directConversation is the data-members of the direct conversation of alice
// and bob in the demo; crew is that of the group "Launch crew".
const (
	directConversation = "alice bob"
	crewConversation   = "alice bob carol"
)

// openConversation opens the Messages page from the sidebar, clicks the
// conversation with the members (the data-members of its entry) and waits for
// its page and the composer.
func openConversation(t *testing.T, ctx context.Context, members string) {
	t.Helper()
	err := chromedp.Do(ctx,
		chromedp.Click(`aside a.sidebar-messages`),
		chromedp.WaitVisible(`#conversation-list li.conversation`),
		chromedp.Click(`#conversation-list li.conversation[data-members="`+members+`"] a.conversation-link`),
		chromedp.WaitVisible(`#message-composer-form`),
	)
	if err != nil {
		t.Fatalf("open the conversation of %s: %v", members, err)
	}
	// The page scrolls to the newest message when it opens.
	if err := waitFor(ctx, `document.getElementById('message-pane').scrollTop + document.getElementById('message-pane').clientHeight >= document.getElementById('message-pane').scrollHeight - 5`); err != nil {
		t.Fatalf("the conversation does not open at the bottom: %v", err)
	}
}

// sendMessage types the text in the composer and clicks Send.
func sendMessage(t *testing.T, ctx context.Context, text string) {
	t.Helper()
	err := chromedp.Do(ctx,
		browsertest.Fill("#message-body", text),
		chromedp.Click(`#message-composer-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("send %q: %v", text, err)
	}
}

// lastMessage is the selector of the newest message of the list.
const lastMessage = `#message-list li.message:last-child`

// waitForLastMessage waits until the newest message of the page has the text.
func waitForLastMessage(t *testing.T, ctx context.Context, text string) {
	t.Helper()
	expr := `(() => { const li = document.querySelector('` + lastMessage + ` .message-body'); return li !== null && li.textContent.includes(` + strconv.Quote(text) + `); })()`
	if err := waitFor(ctx, expr); err != nil {
		t.Fatalf("the newest message does not say %q: %v", text, err)
	}
}

// messageBadgeOf returns the number on the badge of the Messages link, 0 for
// none.
func messageBadgeOf(t *testing.T, ctx context.Context) int {
	t.Helper()
	n, err := chromedp.Run(ctx, chromedp.Evaluate[int](`parseInt(document.querySelector('#message-count').textContent || '0', 10)`))
	if err != nil {
		t.Fatalf("read the badge: %v", err)
	}
	return n
}

// waitForMessageBadge waits until the Messages badge shows n.
func waitForMessageBadge(t *testing.T, ctx context.Context, n int) {
	t.Helper()
	if err := waitFor(ctx, `parseInt(document.querySelector('#message-count').textContent || '0', 10) === `+strconv.Itoa(n)); err != nil {
		t.Fatalf("the Messages badge does not show %d (it shows %d): %v", n, messageBadgeOf(t, ctx), err)
	}
}

// notReloaded sets a marker that a reload would remove, and gives the page's
// stream a moment to open.
func notReloaded(t *testing.T, ctx context.Context) {
	t.Helper()
	runJS(t, ctx, `window.notReloaded = true; true`)
	if err := chromedp.Do(ctx, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
}

// requireNotReloaded fails when the page was loaded again since notReloaded.
func requireNotReloaded(t *testing.T, ctx context.Context, who string) {
	t.Helper()
	if ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !ok {
		t.Errorf("%s's page was reloaded (%v)", who, err)
	}
}

// TestAliceMessagesBobWhoHasTheConversationOpen has alice and bob in their
// direct conversation: alice sends a message and bob sees it at the end without
// a reload, already read, and alice sees "Seen" move under it; Enter sends and
// Shift+Enter does not.
func TestAliceMessagesBobWhoHasTheConversationOpen(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	openConversation(t, bob, directConversation)
	openConversation(t, alice, directConversation)
	notReloaded(t, bob)
	notReloaded(t, alice)

	// The demo's three messages are there, oldest first; bob has read them by
	// opening the conversation, so alice's page shows Seen under her last one.
	if n := countOf(t, bob, "#message-list li.message"); n != 3 {
		t.Fatalf("bob sees %d messages, want the 3 of the demo", n)
	}
	if err := waitFor(alice, `document.querySelectorAll('#message-list .message-seen').length === 1 && document.querySelector('`+lastMessage+` .message-seen') !== null`); err != nil {
		t.Fatalf("alice does not see Seen under her last message: %v", err)
	}

	const text = "Live from alice: lunch at one?"
	sendMessage(t, alice, text)
	waitForLastMessage(t, bob, text)
	if n := countOf(t, bob, "#message-list li.message"); n != 4 {
		t.Errorf("bob sees %d messages, want 4", n)
	}
	if got := textOf(t, bob, lastMessage+" .message-author"); got != "Alice" {
		t.Errorf("author seen by bob = %q", got)
	}
	// Bob's is not an own message: no Edit and no Delete.
	if n := countOf(t, bob, lastMessage+" .message-edit-link, "+lastMessage+" .message-delete"); n != 0 {
		t.Errorf("bob's copy of alice's message has %d author buttons", n)
	}
	// Alice sees hers once, her composer is empty again, and the message is at
	// the bottom.
	waitForLastMessage(t, alice, text)
	if n := countOf(t, alice, "#message-list li.message"); n != 4 {
		t.Errorf("alice sees %d messages, want 4", n)
	}
	if got := valueOf(t, alice, "#message-body"); got != "" {
		t.Errorf("alice's composer still has %q", got)
	}
	if n := countOf(t, alice, lastMessage+" .message-edit-link"); n != 1 {
		t.Errorf("alice's message has %d Edit links, want 1", n)
	}
	// Bob has the conversation open, so it counts as read: his badge stays as
	// it was after he opened it, and alice sees Seen move to the new message.
	waitForMessageBadge(t, bob, 0)
	if err := waitFor(alice, `document.querySelectorAll('#message-list .message-seen').length === 1 && document.querySelector('`+lastMessage+` .message-seen') !== null`); err != nil {
		t.Fatalf("Seen did not move under alice's new message: %v", err)
	}
	requireNotReloaded(t, bob, "bob")
	requireNotReloaded(t, alice, "alice")

	// Enter sends; Shift+Enter makes a new line and sends nothing.
	if err := chromedp.Do(alice, browsertest.Fill("#message-body", "sent by enter"), chromedp.SendKeys("#message-body", kb.Enter)); err != nil {
		t.Fatalf("alice presses Enter: %v", err)
	}
	waitForLastMessage(t, bob, "sent by enter")
	// Her own composer is replaced by an empty one when the answer comes.
	waitForLastMessage(t, alice, "sent by enter")
	if err := waitFor(alice, `document.getElementById('message-body').value === ''`); err != nil {
		t.Fatalf("alice's composer was not cleared: %v", err)
	}
	runJS(t, alice, `(() => {
		const el = document.getElementById('message-body');
		el.value = 'first line';
		el.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', shiftKey: true, bubbles: true, cancelable: true}));
		return true;
	})()`)
	if err := chromedp.Do(alice, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, bob, "#message-list li.message"); n != 5 {
		t.Errorf("bob sees %d messages, want 5: Shift+Enter sent one", n)
	}
	if got := valueOf(t, alice, "#message-body"); got != "first line" {
		t.Errorf("alice's composer = %q after Shift+Enter", got)
	}
}

// TestBobsMessagesBadgeGoesUpWhileHeReadsARoom has bob on "general" (the
// conversation closed) while alice writes in the group "Launch crew": his badge
// goes up without a reload. Then he opens the Messages page, where the list
// shows the new message and its unread mark live.
func TestBobsMessagesBadgeGoesUpWhileHeReadsARoom(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	openGeneral(t, bob)
	openConversation(t, alice, crewConversation)
	// The demo leaves bob one conversation unread: alice's lunch invitation.
	waitForMessageBadge(t, bob, 1)
	notReloaded(t, bob)

	sendMessage(t, alice, "Standup moved to ten")
	waitForMessageBadge(t, bob, 2)
	requireNotReloaded(t, bob, "bob")
	// The badge says what it counts.
	if got, err := chromedp.Run(bob, chromedp.Evaluate[string](`document.querySelector('#message-count').title`)); err != nil || got != "2 conversations with unread messages" {
		t.Errorf("badge title = %q (%v)", got, err)
	}

	// The Messages page: the list marks them, and follows the next message.
	err := chromedp.Do(bob, chromedp.Click(`aside a.sidebar-messages`), chromedp.WaitVisible(`#conversation-list li.conversation`))
	if err != nil {
		t.Fatalf("bob opens the Messages page: %v", err)
	}
	if got := locationOf(t, bob); got != "/messages" {
		t.Errorf("location = %q", got)
	}
	crew := `#conversation-list li.conversation[data-members="` + crewConversation + `"]`
	if err := waitFor(bob, `document.querySelector('`+crew+`.unread .conversation-unread') !== null`); err != nil {
		t.Fatalf("the group is not marked unread: %v", err)
	}
	if got := textOf(t, bob, crew+" .conversation-preview"); got != "Alice: Standup moved to ten" {
		t.Errorf("preview = %q", got)
	}
	notReloaded(t, bob)
	sendMessage(t, alice, "and lunch is on me")
	if err := waitFor(bob, `document.querySelector('`+crew+` .conversation-preview').textContent === 'Alice: and lunch is on me'`); err != nil {
		t.Fatalf("the list did not follow the message: %v", err)
	}
	// Reading the group in another tab clears its mark here.
	requireNotReloaded(t, bob, "bob")
	if err := chromedp.Do(bob, chromedp.Click(crew+` a.conversation-link`), chromedp.WaitVisible(`#message-composer-form`)); err != nil {
		t.Fatalf("bob opens the group: %v", err)
	}
	waitForMessageBadge(t, bob, 1)
}

// TestBobRepliesReactsEditsAndDeletes has bob in the conversation with alice
// open on both sides: he replies to her last message (the quote shows in the
// composer first), reacts to her first, edits his reply and deletes it, and
// alice sees each change live.
func TestBobRepliesReactsEditsAndDeletes(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	openConversation(t, bob, directConversation)
	openConversation(t, alice, directConversation)
	notReloaded(t, alice)
	runJS(t, bob, `window.confirm = () => true; true`)

	// Reply: the quote appears above the field, with a cancel button.
	err := chromedp.Do(bob, chromedp.Click(lastMessage+` a.message-reply-link`), chromedp.WaitVisible(`#message-reply .composer-quote`))
	if err != nil {
		t.Fatalf("bob clicks Reply: %v", err)
	}
	// (The stylesheet shows the label in capitals.)
	if got := textOf(t, bob, "#message-reply .composer-quote"); !strings.Contains(strings.ToLower(got), "replying to") || !strings.Contains(got, "Alice") || !strings.Contains(got, "Lunch at noon?") {
		t.Errorf("composer quote = %q", got)
	}
	// Cancel takes it away, Reply brings it back.
	if err := chromedp.Do(bob, chromedp.Click(`#message-reply a.quote-cancel`)); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, bob, "#message-reply .composer-quote"); n != 0 {
		t.Fatalf("the quote is still in the composer after Cancel")
	}
	err = chromedp.Do(bob, chromedp.Click(lastMessage+` a.message-reply-link`), chromedp.WaitVisible(`#message-reply .composer-quote`))
	if err != nil {
		t.Fatalf("bob clicks Reply again: %v", err)
	}
	sendMessage(t, bob, "Noon works")
	waitForLastMessage(t, alice, "Noon works")
	if got := textOf(t, alice, lastMessage+" blockquote.message-quote"); !strings.Contains(got, "Alice") || !strings.Contains(got, "Lunch at noon?") {
		t.Errorf("alice sees the quote %q", got)
	}
	if err := waitFor(bob, `document.querySelectorAll('#message-reply .composer-quote').length === 0 && document.getElementById('message-body').value === ''`); err != nil {
		t.Errorf("the composer was not cleared: %v", err)
	}

	// React to her first message with the picker.
	first := `#message-list li.message:first-child`
	err = chromedp.Do(bob,
		chromedp.Click(first+` .reaction-add summary`),
		chromedp.WaitVisible(first+` .reaction-option[value="🎉"]`),
		chromedp.Click(first+` .reaction-option[value="🎉"]`),
	)
	if err != nil {
		t.Fatalf("bob reacts: %v", err)
	}
	chip := first + ` .reaction-chip`
	if err := waitFor(alice, `document.querySelectorAll('`+chip+`').length === 1 && document.querySelector('`+chip+`').textContent.includes('🎉')`); err != nil {
		t.Fatalf("alice does not see the reaction: %v", err)
	}
	if n := countOf(t, alice, chip+".mine"); n != 0 {
		t.Errorf("alice's chip is highlighted as hers")
	}
	if err := waitFor(bob, `document.querySelectorAll('`+chip+`.mine').length === 1`); err != nil {
		t.Errorf("bob's chip is not highlighted: %v", err)
	}
	// The picker closed after his own reaction.
	if n := countOf(t, bob, first+" .reaction-add[open]"); n != 0 {
		t.Errorf("the picker is still open")
	}

	// Edit his own reply in place.
	err = chromedp.Do(bob, chromedp.Click(lastMessage+` a.message-edit-link`), chromedp.WaitVisible(lastMessage+` textarea[name="body"]`))
	if err != nil {
		t.Fatalf("bob opens the editor: %v", err)
	}
	err = chromedp.Do(bob,
		browsertest.Fill(lastMessage+` textarea[name="body"]`, "Noon works, see you there"),
		chromedp.Click(lastMessage+` .message-edit-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("bob saves: %v", err)
	}
	waitForLastMessage(t, alice, "Noon works, see you there")
	if err := waitFor(alice, `document.querySelector('`+lastMessage+` .message-edited') !== null`); err != nil {
		t.Errorf("alice does not see 'edited': %v", err)
	}
	if err := waitFor(bob, `document.querySelector('`+lastMessage+` .message-edited') !== null && document.querySelector('`+lastMessage+` textarea') === null`); err != nil {
		t.Errorf("bob's editor did not give way to the message: %v", err)
	}

	// Delete it: the place stays, as "message deleted".
	if err := chromedp.Do(bob, chromedp.Click(lastMessage+` .message-delete button`)); err != nil {
		t.Fatalf("bob deletes: %v", err)
	}
	for who, ctx := range map[string]context.Context{"alice": alice, "bob": bob} {
		if err := waitFor(ctx, `(() => { const li = document.querySelector('`+lastMessage+`'); return li !== null && li.querySelector('.message-deleted-note') !== null && li.querySelector('.message-body') === null && li.querySelector('.message-footer') === null; })()`); err != nil {
			t.Errorf("%s does not see the placeholder: %v", who, err)
		}
	}
	if got := textOf(t, alice, lastMessage+" .message-deleted-note"); got != "message deleted" {
		t.Errorf("placeholder = %q", got)
	}
	if n := countOf(t, alice, "#message-list li.message"); n != 4 {
		t.Errorf("alice sees %d messages, want 4 (the placeholder keeps its place)", n)
	}
	requireNotReloaded(t, alice, "alice")
}

// TestMessageButtonOfARoomOpensTheConversation has bob click "Message" beside
// alice in the member list of "general": he lands in the direct conversation
// the demo made, with its messages.
func TestMessageButtonOfARoomOpensTheConversation(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	openGeneral(t, bob)
	// The button is beside the other members, not beside bob.
	if n := countOf(t, bob, `#members-panel li[data-username="bob"] form.message-form`); n != 0 {
		t.Errorf("bob has a Message button for himself")
	}
	err := chromedp.Do(bob,
		chromedp.WaitVisible(`#members-panel li[data-username="alice"] form.message-form button`),
		chromedp.Click(`#members-panel li[data-username="alice"] form.message-form button`),
		chromedp.WaitVisible(`#message-composer-form`),
	)
	if err != nil {
		t.Fatalf("bob clicks Message: %v", err)
	}
	if got := locationOf(t, bob); !strings.HasPrefix(got, "/messages/") {
		t.Errorf("location = %q, want a conversation", got)
	}
	if got := textOf(t, bob, "#conversation-header h1"); got != "Alice" {
		t.Errorf("title = %q", got)
	}
	if n := countOf(t, bob, "#message-list li.message"); n != 3 {
		t.Errorf("%d messages, want the 3 of the demo conversation", n)
	}
}

// TestOlderMessagesLoadOnScrollingUp has a conversation of more than a page:
// it opens at the bottom, the sentinel at the top loads the older page when it
// is scrolled to and keeps the place, and a new message scrolls into view only
// while the person is near the bottom.
func TestOlderMessagesLoadOnScrollingUp(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	openConversation(t, alice, directConversation)
	// 60 more messages, sent as the composer's form is (without Datastar).
	conv := strings.TrimPrefix(locationOf(t, alice), "/messages/")
	_, err := chromedp.Run(alice, chromedp.Evaluate[bool](`(async () => {
		for (let i = 1; i <= 60; i++) {
			await fetch('/messages/`+conv+`', {method: 'POST', headers: {'Content-Type': 'application/x-www-form-urlencoded'}, body: 'body=' + encodeURIComponent('filler ' + i)});
		}
		return true;
	})()`, chromedp.EvalAwaitPromise))
	if err != nil {
		t.Fatalf("alice sends the fillers: %v", err)
	}

	openConversation(t, bob, directConversation)
	if n := countOf(t, bob, "#message-list li.message"); n != 50 {
		t.Fatalf("bob sees %d messages, want a page of 50", n)
	}
	if n := countOf(t, bob, "#messages-older[data-on-intersect__once]"); n != 1 {
		t.Fatalf("no sentinel at the top")
	}
	if got := textOf(t, bob, lastMessage+" .message-body"); strings.TrimSpace(got) != "filler 60" {
		t.Errorf("newest = %q", got)
	}
	// The sentinel loads nothing while it is out of view.
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, bob, "#message-list li.message"); n != 50 {
		t.Fatalf("%d messages after waiting, want 50: the sentinel fired out of view", n)
	}
	notReloaded(t, bob)

	// Scrolling to the top loads the older 13, on top, and the place is kept:
	// the message that was first is still at the top of the pane.
	runJS(t, bob, `window.firstBefore = document.querySelector('#message-list li.message').id; true`)
	runJS(t, bob, `(() => { document.getElementById('message-pane').scrollTop = 0; return true; })()`)
	if err := waitFor(bob, `document.querySelectorAll('#message-list li.message').length === 63`); err != nil {
		t.Fatalf("the older messages did not load: %v (%d)", err, countOf(t, bob, "#message-list li.message"))
	}
	if n := countOf(t, bob, "#messages-older[data-on-intersect__once]"); n != 0 {
		t.Errorf("the sentinel is still there after the last page")
	}
	if got := textOf(t, bob, "#message-list li.message:first-child .message-body"); strings.TrimSpace(got) != "Hi Bob, did you find the design notes?" {
		t.Errorf("oldest message = %q", got)
	}
	if err := waitFor(bob, `(() => {
		const pane = document.getElementById('message-pane');
		const li = document.getElementById(window.firstBefore);
		const gap = li.getBoundingClientRect().top - pane.getBoundingClientRect().top;
		return pane.scrollTop > 0 && gap >= -2 && gap < 60;
	})()`); err != nil {
		t.Errorf("the place was not kept after the older messages came: %v", err)
	}
	requireNotReloaded(t, bob, "bob")

	// A new message while bob reads the old ones does not move the page ...
	before, err := chromedp.Run(bob, chromedp.Evaluate[float64](`document.getElementById('message-pane').scrollTop`))
	if err != nil {
		t.Fatal(err)
	}
	sendMessage(t, alice, "while you were reading up")
	if err := waitFor(bob, `document.querySelector('`+lastMessage+` .message-body').textContent.trim() === 'while you were reading up'`); err != nil {
		t.Fatalf("the new message did not come: %v", err)
	}
	after, err := chromedp.Run(bob, chromedp.Evaluate[float64](`document.getElementById('message-pane').scrollTop`))
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the page moved from %v to %v when a message came while reading older ones", before, after)
	}
	// ... and one that comes while he is at the bottom scrolls into view.
	runJS(t, bob, `(() => { const p = document.getElementById('message-pane'); p.scrollTop = p.scrollHeight; return true; })()`)
	sendMessage(t, alice, "now you are back")
	if err := waitFor(bob, `(() => { const p = document.getElementById('message-pane'); return document.querySelector('`+lastMessage+` .message-body').textContent.trim() === 'now you are back' && p.scrollHeight - p.scrollTop - p.clientHeight < 5; })()`); err != nil {
		t.Errorf("the new message is not in view at the bottom: %v", err)
	}
}

// TestAliceSearchesHerMessages has alice search the Messages page for a word
// that is in both her conversations (the demo's "notes"): the results replace
// the list with the word highlighted, a click on a result opens the group at
// that message (the page scrolls to it and marks it), and "Clear" brings the
// list back.
func TestAliceSearchesHerMessages(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()
	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	err := chromedp.Do(ctx,
		chromedp.Click(`aside a.sidebar-messages`),
		chromedp.WaitVisible(`#conversation-list li.conversation`),
		browsertest.Fill("#message-search-q", "notes"),
		chromedp.Click(`#message-search button[type="submit"]`),
		chromedp.WaitVisible(`#message-search-results .message-result`),
	)
	if err != nil {
		t.Fatalf("search the messages: %v", err)
	}
	if got := locationOf(t, ctx); got != "/messages?q=notes" {
		t.Errorf("location = %q, want /messages?q=notes", got)
	}
	// Three messages in two conversations, newest first, with the word marked,
	// and no list of conversations.
	if n := countOf(t, ctx, "#message-search-results .message-result"); n != 3 {
		t.Fatalf("results = %d, want 3", n)
	}
	if n := countOf(t, ctx, "#conversation-list"); n != 0 {
		t.Errorf("the list of conversations is still on the page (%d)", n)
	}
	if n := countOf(t, ctx, "#message-search-results .search-snippet mark"); n != 3 {
		t.Errorf("marks = %d, want 3", n)
	}
	if got := textOf(t, ctx, "#message-search-results .message-result:first-child .search-room"); got != "Launch crew" {
		t.Errorf("the newest result is in %q, want Launch crew", got)
	}
	if got := textOf(t, ctx, "#message-search-results .message-result:last-child .search-room"); got != "Bob" {
		t.Errorf("the oldest result is in %q, want Bob", got)
	}

	// A result opens the group at its message.
	err = chromedp.Do(ctx,
		chromedp.Click(`#message-search-results .message-result:first-child a.search-open`),
		chromedp.WaitVisible(`#message-composer-form`),
	)
	if err != nil {
		t.Fatalf("open the message: %v", err)
	}
	if got := locationOf(t, ctx); !strings.HasPrefix(got, "/messages/") || !strings.Contains(got, "?msg=") {
		t.Errorf("location = %q, want a conversation with ?msg=", got)
	}
	id, err := chromedp.Run(ctx, chromedp.Evaluate[string](`location.hash.replace('#', '')`))
	if err != nil {
		t.Fatalf("read the fragment: %v", err)
	}
	if !strings.HasPrefix(id, "msg-") {
		t.Fatalf("the address has the fragment %q, want msg-<id>", id)
	}
	if got := textOf(t, ctx, "#"+id+" .message-body"); !strings.Contains(got, "release notes") {
		t.Errorf("the message at the fragment says %q, want the one with the release notes", got)
	}
	if err := waitFor(ctx, `document.querySelector('#`+id+`:target') !== null`); err != nil {
		t.Errorf("the message is not the target of the page: %v", err)
	}
	// The message is on the screen (inside the pane, not scrolled away).
	if err := waitFor(ctx, `(() => { const r = document.getElementById(`+strconv.Quote(id)+`).getBoundingClientRect(); return r.top >= 0 && r.bottom <= window.innerHeight; })()`); err != nil {
		t.Errorf("the message is not in view: %v", err)
	}

	// Back to the list: the Messages link and Clear.
	err = chromedp.Do(ctx,
		chromedp.Navigate(srv.URL+"/messages?q=notes"),
		chromedp.WaitVisible(`#message-search-results a.search-clear`),
		chromedp.Click(`#message-search-results a.search-clear`),
		chromedp.WaitVisible(`#conversation-list li.conversation`),
	)
	if err != nil {
		t.Fatalf("clear the search: %v", err)
	}
	if got := valueOf(t, ctx, "#message-search-q"); got != "" {
		t.Errorf("the box still says %q", got)
	}
}
