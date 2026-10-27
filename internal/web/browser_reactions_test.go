//go:build clowder_browser

package web_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// chipSelector is the selector of the chip of the emoji, a button of a member,
// in the reaction bar of the card that the selector card finds.
func chipSelector(card, emoji string) string {
	return card + ` .reaction-chips [name="emoji"][value="` + emoji + `"]`
}

// chipState reads the chip of the emoji in the card: its count, followed by
// " mine" when the person put it, and empty when there is no such chip.
func chipState(ctx context.Context, card, emoji string) (string, error) {
	return chromedp.Run(ctx, chromedp.Evaluate[string](
		`(() => { const c = document.querySelector(`+strconv.Quote(chipSelector(card, emoji))+`);
		return c ? c.querySelector('.reaction-count').textContent + (c.classList.contains('mine') ? ' mine' : '') : ''; })()`))
}

// waitChip waits until the chip of the emoji in the card is in the state: see
// chipState.
func waitChip(ctx context.Context, card, emoji, want string) error {
	return waitFor(ctx, `(() => { const c = document.querySelector(`+strconv.Quote(chipSelector(card, emoji))+`);
		return c !== null && c.querySelector('.reaction-count').textContent + (c.classList.contains('mine') ? ' mine' : '') === `+strconv.Quote(want)+`; })()`)
}

// TestLiveReactionsReachOtherPages has alice, bob and carol on "general":
// alice reacts to the newest post with a party popper from the picker, and bob
// and carol see the chip appear without a reload, while bob's open picker and
// the text in it are left alone. Bob clicks it: the count is 2
// and the chip is his on his page only. Alice then reacts with an emoji typed in
// the field, and bob sees that too; bob takes his reaction back.
func TestLiveReactionsReachOtherPages(t *testing.T) {
	const party, unicorn = "\U0001F389", "\U0001F984"
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	carol := secondBrowser(t, srv, "carol")
	alice := secondBrowser(t, srv, "alice")
	for _, ctx := range []context.Context{bob, carol, alice} {
		openGeneral(t, ctx)
	}
	runJS(t, bob, `window.notReloaded = true; true`)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	card := `#feed-posts article.post:first-child`
	// The demo's own reactions are on the post; the party popper is not.
	if got, err := chipState(bob, card, party); err != nil || got != "" {
		t.Fatalf("bob sees the party popper before alice reacts: %q, %v", got, err)
	}
	if n := countOf(t, bob, card+` .reaction-chips [name="emoji"]`); n != 1 {
		t.Fatalf("the newest post has %d demo reactions, want 1", n)
	}

	// Bob opens his picker and starts typing in its field: a reaction made by
	// someone else must not close it or clear the text.
	err := chromedp.Do(bob,
		chromedp.Click(card+` .reaction-add summary`),
		chromedp.WaitVisible(card+` .reaction-custom input[name="emoji"]`),
		browsertest.Fill(card+` .reaction-custom input[name="emoji"]`, "half"),
	)
	if err != nil {
		t.Fatalf("bob opens his picker: %v", err)
	}

	// Alice opens the picker and clicks the party popper.
	err = chromedp.Do(alice,
		chromedp.Click(card+` .reaction-add summary`),
		chromedp.WaitVisible(card+` button.reaction-option[value="`+party+`"]`),
		chromedp.Click(card+` button.reaction-option[value="`+party+`"]`),
	)
	if err != nil {
		t.Fatalf("alice reacts: %v", err)
	}
	if err := waitChip(alice, card, party, "1 mine"); err != nil {
		t.Fatalf("alice does not see her own chip: %v", err)
	}
	if err := waitChip(bob, card, party, "1"); err != nil {
		t.Fatalf("bob does not see alice's reaction: %v", err)
	}
	if title, err := chromedp.Run(bob, chromedp.Evaluate[string](
		`document.querySelector(`+strconv.Quote(chipSelector(card, party))+`).title`)); err != nil || title != "Alice" {
		t.Errorf("the title of the chip = %q, %v, want the name of the person", title, err)
	}
	if err := waitChip(carol, card, party, "1"); err != nil {
		t.Fatalf("carol does not see alice's reaction: %v", err)
	}

	// Bob's picker is still open, with his text.
	if open, err := chromedp.Run(bob, chromedp.Evaluate[bool](`document.querySelector(`+strconv.Quote(card+` .reaction-add`)+`).open`)); err != nil || !open {
		t.Errorf("bob's picker was closed by alice's reaction (%v)", err)
	}
	if got := valueOf(t, bob, card+` .reaction-custom input[name="emoji"]`); got != "half" {
		t.Errorf("bob's field holds %q after alice's reaction, want his text", got)
	}

	// Bob clicks the chip: two reactions, and his chip is highlighted.
	if err := chromedp.Do(bob, chromedp.Click(chipSelector(card, party))); err != nil {
		t.Fatalf("bob clicks the chip: %v", err)
	}
	if err := waitChip(bob, card, party, "2 mine"); err != nil {
		t.Fatalf("bob's chip is not his with two reactions: %v", err)
	}
	// Alice's chip counts two and stays hers; carol's counts two and is not hers.
	if err := waitChip(alice, card, party, "2 mine"); err != nil {
		t.Fatalf("alice's chip did not count bob: %v", err)
	}
	if err := waitChip(carol, card, party, "2"); err != nil {
		t.Fatalf("carol's chip is not 2, and not hers: %v", err)
	}
	if title, err := chromedp.Run(carol, chromedp.Evaluate[string](
		`document.querySelector(`+strconv.Quote(chipSelector(card, party))+`).title`)); err != nil || title != "Alice, Bob" {
		t.Errorf("the title with two people = %q, %v, want both names", title, err)
	}

	// Alice types an emoji in the field of the picker.
	err = chromedp.Do(alice,
		chromedp.Click(card+` .reaction-add summary`),
		chromedp.WaitVisible(card+` .reaction-custom input[name="emoji"]`),
		browsertest.Fill(card+` .reaction-custom input[name="emoji"]`, unicorn),
		chromedp.Click(card+` .reaction-custom button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("alice types an emoji: %v", err)
	}
	if err := waitChip(bob, card, unicorn, "1"); err != nil {
		t.Fatalf("bob does not see the typed emoji: %v", err)
	}

	// Bob takes his back: one reaction is left, and it is not his.
	if err := chromedp.Do(bob, chromedp.Click(chipSelector(card, party))); err != nil {
		t.Fatalf("bob clicks the chip again: %v", err)
	}
	if err := waitChip(bob, card, party, "1"); err != nil {
		t.Fatalf("bob's chip after taking it back: %v", err)
	}
	if err := waitChip(alice, card, party, "1 mine"); err != nil {
		t.Fatalf("alice's chip after bob took his back: %v", err)
	}
	if ok, err := chromedp.Run(bob, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !ok {
		t.Errorf("bob's page was reloaded (%v)", err)
	}
}
