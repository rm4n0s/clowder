//go:build clowder_browser

package web_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// openBoard opens the board of "general" the way a person does: the room in
// the sidebar, then the Board link of its header.
func openBoard(t *testing.T, ctx context.Context) {
	t.Helper()
	err := chromedp.Do(ctx,
		chromedp.Click(`aside a[data-room="general"]`), chromedp.WaitVisible("#post-editor-new"),
		chromedp.Click(`.room-nav a[href$="/board"]`), chromedp.WaitVisible("#board-columns .board-card"),
	)
	if err != nil {
		t.Fatalf("open the board: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
}

// columnSel is the selector of the column with the name.
func columnSel(name string) string {
	return `.board-column[aria-label=` + strconv.Quote(name) + `]`
}

// cardsIn returns the titles of the cards of the column, top to bottom, joined
// with "|".
func cardsIn(t *testing.T, ctx context.Context, column string) string {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`[...document.querySelectorAll(`+strconv.Quote(columnSel(column)+` .card-title`)+`)].map(e => e.textContent).join('|')`))
	if err != nil {
		t.Fatalf("read the cards of %s: %v", column, err)
	}
	return got
}

// cardJS is the JavaScript that finds the card with the title.
func cardJS(title string) string {
	return `[...document.querySelectorAll('.board-card')].find(c => c.querySelector('.card-title').textContent === ` + strconv.Quote(title) + `)`
}

// cardSel returns the selector of the card with the title, by its id, which a
// live update of the columns leaves as it is.
func cardSel(t *testing.T, ctx context.Context, title string) string {
	t.Helper()
	id, err := chromedp.Run(ctx, chromedp.Evaluate[string](cardJS(title)+`.id`))
	if err != nil {
		t.Fatalf("find the card %q: %v", title, err)
	}
	return "#" + id
}

// dragJS dispatches the events a browser fires when a person drags the card to
// the top of the column (the events the script of the board listens to): the
// drag starts on the card, goes over the list of the column at the height of
// its first card and drops there. With hold the drag is left in the air, with
// neither a drop nor an end. The page's own handlers run; only the mouse is
// replaced.
func dragJS(title, column string, hold bool) string {
	return `(() => {
		const card = ` + cardJS(title) + `;
		const list = document.querySelector(` + strconv.Quote(columnSel(column)+` .board-cards`) + `);
		if (!card || !list) { return false; }
		const hold = ` + strconv.FormatBool(hold) + `;
		const dt = new DataTransfer();
		const fire = (el, type, y) => el.dispatchEvent(new DragEvent(type, {bubbles: true, cancelable: true, dataTransfer: dt, clientX: 20, clientY: y}));
		const first = list.querySelector('.board-card');
		const y = (first || list).getBoundingClientRect().top + 1;
		fire(card, 'dragstart', 0);
		fire(list, 'dragover', y);
		if (!hold) {
			fire(list, 'drop', y);
			fire(card, 'dragend', 0);
		}
		return true;
	})()`
}

// TestAliceDragsACardAndBobSeesIt has alice and bob on the board of "general".
// Alice drags a card from "To do" to the top of "In progress"; the server's
// order shows on her page, and bob's board has the card there without a reload.
// Then bob moves a card with the "Move to…" choice, which alice sees.
func TestAliceDragsACardAndBobSeesIt(t *testing.T) {
	srv := browsertest.Start(t)
	alice := secondBrowser(t, srv, "alice")
	bob := secondBrowser(t, srv, "bob")
	openBoard(t, alice)
	openBoard(t, bob)
	runJS(t, bob, `window.notReloaded = true; true`)

	// The demo's board: three columns, the card is in the first.
	const card = "Tag the release"
	for _, name := range []string{"To do", "In progress", "Done"} {
		if n := countOf(t, alice, columnSel(name)); n != 1 {
			t.Fatalf("alice sees %d columns named %s, want 1", n, name)
		}
	}
	if got := cardsIn(t, alice, "To do"); !strings.Contains(got, card) {
		t.Fatalf("To do holds %q, want %q in it", got, card)
	}
	inProgress := cardsIn(t, alice, "In progress")
	if inProgress == "" || strings.Contains(inProgress, card) {
		t.Fatalf("In progress holds %q", inProgress)
	}
	// The card has an overdue neighbor, in red, and the first column is not done.
	if n := countOf(t, alice, columnSel("To do")+` .task-due.overdue`); n != 1 {
		t.Errorf("To do shows %d overdue dates, want 1", n)
	}

	runJS(t, alice, dragJS(card, "In progress", false))
	want := card + "|" + inProgress
	if err := waitFor(alice, `[...document.querySelectorAll(`+strconv.Quote(columnSel("In progress")+` .card-title`)+`)].map(e => e.textContent).join('|') === `+strconv.Quote(want)); err != nil {
		t.Fatalf("alice's board does not show the card at the top of In progress: %v (%q)", err, cardsIn(t, alice, "In progress"))
	}
	if got := cardsIn(t, alice, "To do"); strings.Contains(got, card) {
		t.Errorf("the card is still in To do: %q", got)
	}
	// The drag left nothing behind: patches are allowed again, no mark remains.
	if n := countOf(t, alice, `#board[data-ignore-morph], .dragging, .drop-over, .drop-before, .drop-end`); n != 0 {
		t.Errorf("%d leftovers of the drag", n)
	}

	// Bob's board shows it there without a reload.
	if err := waitFor(bob, `[...document.querySelectorAll(`+strconv.Quote(columnSel("In progress")+` .card-title`)+`)].map(e => e.textContent).join('|') === `+strconv.Quote(want)); err != nil {
		t.Fatalf("bob does not see the card in In progress: %v (%q)", err, cardsIn(t, bob, "In progress"))
	}
	if ok, err := chromedp.Run(bob, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !ok {
		t.Errorf("bob's page was reloaded (%v)", err)
	}
	if n := countOf(t, bob, `[id^="task-"]`); n != countOf(t, alice, `[id^="task-"]`) {
		t.Errorf("bob sees %d cards, alice %d", n, countOf(t, alice, `[id^="task-"]`))
	}

	// The same without a mouse: bob picks the column in the card's choice.
	const other = "Invite the rest of the team"
	doneID, err := chromedp.Run(bob, chromedp.Evaluate[string](`document.querySelector(`+strconv.Quote(columnSel("Done"))+`).dataset.column`))
	if err != nil {
		t.Fatalf("find the Done column: %v", err)
	}
	err = chromedp.Do(bob,
		chromedp.SetValue(cardSel(t, bob, other)+` select`, doneID),
		chromedp.Click(cardSel(t, bob, other)+` form.card-move button`),
	)
	if err != nil {
		t.Fatalf("bob moves the card by the choice: %v", err)
	}
	if err := waitFor(bob, `document.querySelector(`+strconv.Quote(columnSel("Done"))+`).textContent.includes(`+strconv.Quote(other)+`)`); err != nil {
		t.Fatalf("the card is not in Done on bob's page: %v", err)
	}
	if err := waitFor(alice, `document.querySelector(`+strconv.Quote(columnSel("Done"))+`).textContent.includes(`+strconv.Quote(other)+`)`); err != nil {
		t.Fatalf("the card is not in Done on alice's page: %v", err)
	}
}

// TestBoardHoldsLiveUpdatesWhileACardIsHeld has alice hold a card in the air
// while bob moves another one: her board does not change until she lets go,
// and then it shows bob's move.
func TestBoardHoldsLiveUpdatesWhileACardIsHeld(t *testing.T) {
	srv := browsertest.Start(t)
	alice := secondBrowser(t, srv, "alice")
	bob := secondBrowser(t, srv, "bob")
	openBoard(t, alice)
	openBoard(t, bob)

	const held, moved = "Tag the release", "Invite the rest of the team"
	runJS(t, alice, dragJS(held, "In progress", true))
	if n := countOf(t, alice, `#board[data-ignore-morph]`); n != 1 {
		t.Fatalf("the board of the person who holds a card takes patches")
	}
	before := cardsIn(t, alice, "Done")

	doneID, err := chromedp.Run(bob, chromedp.Evaluate[string](`document.querySelector(`+strconv.Quote(columnSel("Done"))+`).dataset.column`))
	if err != nil {
		t.Fatalf("find the Done column: %v", err)
	}
	err = chromedp.Do(bob,
		chromedp.SetValue(cardSel(t, bob, moved)+` select`, doneID),
		chromedp.Click(cardSel(t, bob, moved)+` form.card-move button`),
	)
	if err != nil {
		t.Fatalf("bob moves a card: %v", err)
	}
	if err := waitFor(bob, `document.querySelector(`+strconv.Quote(columnSel("Done"))+`).textContent.includes(`+strconv.Quote(moved)+`)`); err != nil {
		t.Fatalf("bob's own board does not show his move: %v", err)
	}
	// Alice's stream has had time to deliver it; the board kept still.
	if err := chromedp.Do(alice, chromedp.Sleep(streamSettle*2)); err != nil {
		t.Fatal(err)
	}
	if got := cardsIn(t, alice, "Done"); got != before {
		t.Errorf("alice's board changed while she held a card: Done = %q, was %q", got, before)
	}

	// She lets go without dropping: the board is fetched and shows bob's move.
	runJS(t, alice, `(() => { `+cardJS(held)+`.dispatchEvent(new DragEvent('dragend', {bubbles: true, cancelable: true})); return true; })()`)
	if err := waitFor(alice, `document.querySelector(`+strconv.Quote(columnSel("Done"))+`).textContent.includes(`+strconv.Quote(moved)+`)`); err != nil {
		t.Fatalf("alice's board did not catch up after the drag ended: %v", err)
	}
	if n := countOf(t, alice, `#board[data-ignore-morph], .dragging`); n != 0 {
		t.Errorf("%d leftovers of the drag", n)
	}
}

// TestBoardColumnsAndCardForm has alice add, rename and delete columns, see the
// refusal to delete a column with cards, and edit a card in the form that
// opens beside the columns, which a live move by bob does not close or clear.
func TestBoardColumnsAndCardForm(t *testing.T) {
	srv := browsertest.Start(t)
	alice := secondBrowser(t, srv, "alice")
	bob := secondBrowser(t, srv, "bob")
	openBoard(t, alice)
	openBoard(t, bob)
	runJS(t, alice, `window.confirm = () => true; true`)

	// A column with cards cannot be deleted.
	err := chromedp.Do(alice, chromedp.Click(columnSel("To do")+` form[action$="/delete"] button`))
	if err != nil {
		t.Fatalf("alice deletes To do: %v", err)
	}
	if err := waitFor(alice, `document.querySelector('#board-message .form-error') !== null`); err != nil {
		t.Fatalf("no message after deleting a column with cards: %v", err)
	}
	if got := textOf(t, alice, "#board-message .form-error"); got != "Move its cards first." {
		t.Errorf("message = %q, want %q", got, "Move its cards first.")
	}
	if n := countOf(t, alice, columnSel("To do")); n != 1 {
		t.Errorf("To do is gone")
	}

	// Add a column; bob sees it on the right without a reload.
	err = chromedp.Do(alice, browsertest.Fill("#board-add-name", "Review"), chromedp.Click(`#board-add-form button[type="submit"]`))
	if err != nil {
		t.Fatalf("alice adds a column: %v", err)
	}
	if err := waitFor(alice, `document.querySelectorAll('.board-column').length === 4 && document.querySelector('#board-add-name').value === ''`); err != nil {
		t.Fatalf("alice's board lacks the new column, or its form kept the name: %v", err)
	}
	if err := waitFor(bob, `[...document.querySelectorAll('.board-column')].at(-1).getAttribute('aria-label') === 'Review'`); err != nil {
		t.Fatalf("bob does not see the new column: %v", err)
	}
	// Rename it inline.
	err = chromedp.Do(alice, browsertest.Fill(columnSel("Review")+` input.column-name`, "Reviewed"), chromedp.Click(columnSel("Review")+` form.column-rename button`))
	if err != nil {
		t.Fatalf("alice renames the column: %v", err)
	}
	if err := waitFor(bob, `document.querySelector(`+strconv.Quote(columnSel("Reviewed"))+`) !== null`); err != nil {
		t.Fatalf("bob does not see the new name: %v", err)
	}
	// Move it left, then delete it (it has no cards).
	err = chromedp.Do(alice, chromedp.Click(columnSel("Reviewed")+` form[action$="/move"] button[title="Move left"]`))
	if err != nil {
		t.Fatalf("alice moves the column left: %v", err)
	}
	if err := waitFor(alice, `[...document.querySelectorAll('.board-column')].map(c => c.getAttribute('aria-label')).join('|') === 'To do|In progress|Reviewed|Done'`); err != nil {
		t.Fatalf("the column did not move left: %v", err)
	}
	err = chromedp.Do(alice, chromedp.Click(columnSel("Reviewed")+` form[action$="/delete"] button`))
	if err != nil {
		t.Fatalf("alice deletes the column: %v", err)
	}
	if err := waitFor(bob, `document.querySelectorAll('.board-column').length === 3`); err != nil {
		t.Fatalf("bob still sees the deleted column: %v", err)
	}

	// The card form: opened by Details, in its own panel.
	const card = "Tag the release"
	err = chromedp.Do(alice, chromedp.Click(cardSel(t, alice, card)+` a.card-details`), chromedp.WaitVisible("#board-editor #board-card-form"))
	if err != nil {
		t.Fatalf("alice opens the card: %v", err)
	}
	if got := valueOf(t, alice, `#board-card-form input[name="title"]`); got != card {
		t.Errorf("title in the form = %q, want %q", got, card)
	}
	err = chromedp.Do(alice, browsertest.Fill(`#board-card-form input[name="title"]`, "Tag the release 1.0"))
	if err != nil {
		t.Fatalf("alice types a title: %v", err)
	}
	// Bob moves a card: alice's columns are patched, the form is left alone.
	const bobsCard = "Invite the rest of the team"
	doneID, err := chromedp.Run(bob, chromedp.Evaluate[string](`document.querySelector(`+strconv.Quote(columnSel("Done"))+`).dataset.column`))
	if err != nil {
		t.Fatalf("find the Done column: %v", err)
	}
	err = chromedp.Do(bob, chromedp.SetValue(cardSel(t, bob, bobsCard)+` select`, doneID), chromedp.Click(cardSel(t, bob, bobsCard)+` form.card-move button`))
	if err != nil {
		t.Fatalf("bob moves a card: %v", err)
	}
	if err := waitFor(alice, `document.querySelector(`+strconv.Quote(columnSel("Done"))+`).textContent.includes(`+strconv.Quote(bobsCard)+`)`); err != nil {
		t.Fatalf("alice does not see bob's move: %v", err)
	}
	if got := valueOf(t, alice, `#board-card-form input[name="title"]`); got != "Tag the release 1.0" {
		t.Errorf("the live update took the typed title away: %q", got)
	}
	// An empty title shows its problem in the form; a good one saves and closes it.
	err = chromedp.Do(alice, browsertest.Fill(`#board-card-form input[name="title"]`, " "), chromedp.Click(`#board-card-form button[type="submit"]`))
	if err != nil {
		t.Fatalf("alice saves an empty title: %v", err)
	}
	if err := waitFor(alice, `document.querySelector('#board-card-form .field-error') !== null`); err != nil {
		t.Fatalf("no problem shown for the empty title: %v", err)
	}
	err = chromedp.Do(alice, browsertest.Fill(`#board-card-form input[name="title"]`, "Tag the release 1.0"), chromedp.Click(`#board-card-form button[type="submit"]`))
	if err != nil {
		t.Fatalf("alice saves the title: %v", err)
	}
	if err := waitFor(alice, `document.querySelector('#board-card-form') === null`); err != nil {
		t.Fatalf("the form did not close: %v", err)
	}
	for name, ctx := range map[string]context.Context{"alice": alice, "bob": bob} {
		if err := waitFor(ctx, `document.querySelector(`+strconv.Quote(columnSel("To do"))+`).textContent.includes('Tag the release 1.0')`); err != nil {
			t.Errorf("%s does not see the new title: %v", name, err)
		}
	}
}

// TestBoardOfAPersonWhoHasNotJoined has carol read the board of "general" in
// a room she has not joined: no controls, the note, and the cards link to
// their posts.
func TestBoardOfAPersonWhoHasNotJoined(t *testing.T) {
	srv := browsertest.Start(t)
	carol := secondBrowser(t, srv, "carol")
	err := chromedp.Do(carol, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible("#rooms-panel"))
	if err != nil {
		t.Fatalf("open the rooms: %v", err)
	}
	href, err := chromedp.Run(carol, chromedp.Evaluate[string](`document.querySelector('.room-card[data-room="random"] h2 a').getAttribute('href')`))
	if err != nil {
		t.Fatalf("find random: %v", err)
	}
	err = chromedp.Do(carol, chromedp.Navigate(srv.URL+href+"/board"), chromedp.WaitVisible("#board-columns"))
	if err != nil {
		t.Fatalf("open the board of random: %v", err)
	}
	if got := textOf(t, carol, "#board-note"); got != "Join this room to change the board" {
		t.Errorf("note = %q", got)
	}
	if n := countOf(t, carol, `[draggable], form.card-move, a.card-details, form.column-rename, #board-add-form`); n != 0 {
		t.Errorf("%d controls on a board she may only read", n)
	}
	if n := countOf(t, carol, `.board-column`); n != 3 {
		t.Errorf("%d columns, want 3", n)
	}
}
