package web

import (
	"net/http"
	"testing"
)

func TestRoomStreamPatchesReactionsForEachViewer(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	p := e.writePost("alice", "general", "react to me")
	// Bob reacted with the fire already; carol has not joined the room.
	e.react("bob", p, fireEmoji)
	ss := e.serveStreams()
	seen := "seen=" + idText(newestOf(e, "general"))
	bob := ss.open("bob", e.streamOfRoom("general", seen), asDatastar())
	alice := ss.open("alice", e.streamOfRoom("general", seen), asDatastar())
	carol := ss.open("carol", e.streamOfRoom("general", seen), asDatastar())
	for _, c := range []*streamClient{bob, alice, carol} {
		if c.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", c.status)
		}
	}

	e.react("alice", p, partyEmoji)

	id := `id="reactions-` + idText(p.ID) + `"`
	// Only the bar is patched, with the chips of the viewer: bob put the fire,
	// alice the party.
	got := bob.patches(1)[0]
	requirePatch(t, got, "", "outer",
		[]string{id, `value="` + fireEmoji + `" class="reaction-chip mine" title="Bob"`, `value="` + partyEmoji + `" class="reaction-chip" title="Alice"`},
		// Only the chips: the picker is left alone, so that it stays open.
		[]string{"<article", "post-content", "<details", "reaction-add", "reaction-picker", `class="reaction-chip mine" title="Alice"`})
	got = alice.patches(1)[0]
	requirePatch(t, got, "", "outer",
		[]string{id, `value="` + fireEmoji + `" class="reaction-chip" title="Bob"`, `value="` + partyEmoji + `" class="reaction-chip mine" title="Alice"`}, []string{"<article"})
	// A reader who has not joined sees the chips and cannot react.
	got = carol.patches(1)[0]
	requirePatch(t, got, "", "outer",
		[]string{id, `<ul class="reaction-chips"`, `title="Bob"`, `title="Alice"`}, []string{"<details", `name="emoji"`, "reaction-chip mine"})

	// Taking it back patches again, without the chip.
	e.react("alice", p, partyEmoji)
	got = bob.patches(1)[0]
	requirePatch(t, got, "", "outer", []string{id, fireEmoji}, []string{partyEmoji + `</span>`})
	_ = alice.patches(1)
	_ = carol.patches(1)
	bob.requireQuiet()
}

func TestRoomStreamIgnoresReactionsItDoesNotShow(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	post := e.writePost("alice", "general", "root")
	comment := e.commentOn(post, "bob", "a comment")
	// A post that stays as a placeholder, and one of another room.
	gone := e.writePost("alice", "general", "going")
	e.commentOn(gone, "bob", "keeps it")
	e.react("bob", gone, partyEmoji)
	if err := e.posts.Delete(t.Context(), e.actor(e.alice), gone.ID); err != nil {
		t.Fatal(err)
	}
	elsewhere := e.writePost("alice", "secret", "elsewhere")
	ss := e.serveStreams()
	seen := "seen=" + idText(newestOf(e, "general"))
	c := ss.open("alice", e.streamOfRoom("general", seen), asDatastar())
	old := ss.open("alice", e.streamOfRoom("general", seen+"&before=999"), asDatastar())

	e.react("bob", comment, partyEmoji)   // a comment is not in the feed
	e.react("bob", elsewhere, partyEmoji) // another room's event is not on this topic
	if _, err := e.posts.ToggleReaction(t.Context(), e.actor(e.bob), gone.ID, partyEmoji); err == nil {
		t.Fatal("a deleted post took a reaction")
	}
	c.requireQuiet()
	// A page of older posts is not kept up to date.
	e.react("bob", post, partyEmoji)
	old.requireQuiet()
	// The page that is kept up to date gets the post that did change.
	got := c.patches(1)[0]
	requirePatch(t, got, "", "outer", []string{`id="reactions-` + idText(post.ID) + `"`, "reaction-chip"}, nil)
}

func TestPostStreamPatchesReactions(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	d := e.demoThread()
	e.react("bob", d.first, fireEmoji)
	ss := e.serveStreams()
	newest := idText(max(d.root.ID, d.first.ID, d.second.ID, d.third.ID, d.gone.ID, d.after.ID))
	alice := ss.open("alice", e.streamOfPost(d.root, "seen="+newest), asDatastar())
	carol := ss.open("carol", e.streamOfPost(d.root, "seen="+newest), asDatastar())

	// A comment: its own bar, with this viewer's chips and the page it is on.
	e.react("alice", d.first, partyEmoji)
	got := alice.patches(1)[0]
	requirePatch(t, got, "", "outer",
		[]string{`id="reactions-` + idText(d.first.ID) + `"`, `value="` + partyEmoji + `" class="reaction-chip mine"`,
			`value="` + fireEmoji + `" class="reaction-chip"`, `name="from" value="post"`}, []string{"<article", "post-content"})
	got = carol.patches(1)[0]
	requirePatch(t, got, "", "outer", []string{`id="reactions-` + idText(d.first.ID) + `"`, `<ul class="reaction-chips"`}, []string{"<details", "mine"})

	// The top-level post.
	e.react("alice", d.root, fireEmoji)
	got = alice.patches(1)[0]
	requirePatch(t, got, "", "outer", []string{`id="reactions-` + idText(d.root.ID) + `"`, `reaction-chip mine`}, []string{"<article"})
	_ = carol.patches(1)

	// A reaction that is gone with its post: nothing to patch.
	if _, err := e.posts.ToggleReaction(t.Context(), e.actor(e.alice), d.gone.ID, fireEmoji); err == nil {
		t.Fatal("a deleted comment took a reaction")
	}
	alice.requireQuiet()
}

func TestReactionEventForAPostThatVanishedIsHarmless(t *testing.T) {
	t.Parallel()
	// A comment reacted to and then deleted before the stream handles the event:
	// the thread has no such comment and nothing is sent.
	e := newRoomsEnv(t)
	root := e.writePost("alice", "general", "root")
	comment := e.commentOn(root, "bob", "short lived")
	c := e.serveStreams().open("alice", e.streamOfPost(root, "seen="+idText(comment.ID)), asDatastar())
	e.react("bob", comment, partyEmoji)
	if err := e.posts.Delete(t.Context(), e.actor(e.bob), comment.ID); err != nil {
		t.Fatal(err)
	}
	// Whatever order the streams handle the two events in, the page is left
	// right: the reaction patch (if any) is a bar, and the thread is sent again
	// without the comment.
	for _, p := range c.drain() {
		if p.selector != "" || p.mode != "outer" {
			t.Errorf("unexpected patch %+v", p)
		}
	}
}
