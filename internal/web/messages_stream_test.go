package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
)

// streamOfConversation is the path of the stream of the conversation, with the
// query.
func streamOfConversation(conv uint64, query string) string {
	path := convPath(conv) + "/stream"
	if query != "" {
		path += "?" + query
	}
	return path
}

// patchesWith returns the patches whose HTML or selector contain text.
func patchesWith(ps []patch, text string) []patch {
	var out []patch
	for _, p := range ps {
		if strings.Contains(p.html, text) || strings.Contains(p.selector, text) {
			out = append(out, p)
		}
	}
	return out
}

// requireOne fails unless exactly one of the patches has text, and returns it.
func requireOne(t *testing.T, ps []patch, text string) patch {
	t.Helper()
	got := patchesWith(ps, text)
	if len(got) != 1 {
		t.Fatalf("%d patches have %q, want 1; patches: %+v", len(got), text, ps)
	}
	return got[0]
}

// countPatchesOf splits the patches into the badges of the messages and the
// patches that are not counts. The sidebar counts come in a pair
// (notifications, then messages).
func countPatchesOf(ps []patch) (messageBadges []string, others []patch) {
	for _, p := range ps {
		switch {
		case strings.Contains(p.html, `id="message-count"`):
			messageBadges = append(messageBadges, p.html)
		case strings.Contains(p.html, `id="notification-count"`):
		default:
			others = append(others, p)
		}
	}
	return messageBadges, others
}

func TestConversationStreamConnects(t *testing.T) {
	t.Parallel()
	t.Run("a group that is up to date sends nothing", func(t *testing.T) {
		e := newRoomsEnv(t)
		crew := e.group("crew", "alice", "bob", "carol")
		last := e.say("alice", crew, "hello")
		c := e.serveStreams().open("bob", streamOfConversation(crew, "seen="+idText(last.ID)), asDatastar())
		if c.status != http.StatusOK || !strings.HasPrefix(c.header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("status = %d, type = %q", c.status, c.header.Get("Content-Type"))
		}
		c.requireQuiet()
	})

	t.Run("a direct conversation sends the state of Seen", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		mine := e.say("alice", conv, "read me")
		// bob has not read it: no message is Seen.
		c := e.serveStreams().open("alice", streamOfConversation(conv, "seen="+idText(mine.ID)), asDatastar())
		got := c.patches(1)
		requirePatch(t, got[0], ".message-seen", "remove", nil, nil)
		c.requireQuiet()

		// bob has read it: the stream says where.
		if err := e.msgs.MarkRead(t.Context(), e.actor(e.bob), conv, mine.ID); err != nil {
			t.Fatal(err)
		}
		c = e.serveStreams().open("alice", streamOfConversation(conv, "seen="+idText(mine.ID)), asDatastar())
		got = c.patches(2)
		requirePatch(t, got[0], ".message-seen", "remove", nil, nil)
		requirePatch(t, got[1], "", "outer", []string{`<div id="msg-seen-` + idText(mine.ID) + `" class="message-seen-slot"><p class="message-seen">Seen</p></div>`}, nil)
	})

	t.Run("a page that is behind gets the messages it lacks, and they are read", func(t *testing.T) {
		e := newRoomsEnv(t)
		crew := e.group("crew", "alice", "bob", "carol")
		old := e.say("alice", crew, "on the page")
		fresh1 := e.say("alice", crew, "came while loading")
		fresh2 := e.say("carol", crew, "and this too")
		if got := e.unreadConversations("bob"); got != 1 {
			t.Fatalf("bob's unread = %d, want 1", got)
		}
		c := e.serveStreams().open("bob", streamOfConversation(crew, "seen="+idText(old.ID)), asDatastar())
		got := c.drain()
		requireContains(t, got[0].html, []string{`<li id="msg-` + idText(fresh1.ID) + `"`, "came while loading"}, []string{"on the page"})
		if got[0].selector != "#message-list" || got[0].mode != "append" {
			t.Errorf("first patch = %+v", got[0])
		}
		requirePatch(t, got[1], "#msg-"+idText(fresh1.ID)+" ~ #msg-"+idText(fresh1.ID), "remove", nil, nil)
		requireContains(t, got[2].html, []string{`<li id="msg-` + idText(fresh2.ID) + `"`, "and this too", "Carol"}, nil)
		requirePatch(t, got[3], "#msg-"+idText(fresh2.ID)+" ~ #msg-"+idText(fresh2.ID), "remove", nil, nil)
		// The messages were read when the stream showed them (D33), and the
		// sidebar count says so.
		if got := e.unreadConversations("bob"); got != 0 {
			t.Errorf("bob's unread = %d, want 0", got)
		}
		badges, _ := countPatchesOf(got[4:])
		if len(badges) == 0 || badges[len(badges)-1] != messageBadge(0) {
			t.Errorf("message badges = %v, want the last to be empty", badges)
		}
	})

	t.Run("a page of older messages gets nothing", func(t *testing.T) {
		e := newRoomsEnv(t)
		crew := e.group("crew", "alice", "bob", "carol")
		old := e.say("alice", crew, "old")
		e.say("alice", crew, "newer")
		c := e.serveStreams().open("bob", streamOfConversation(crew, "seen="+idText(old.ID)+"&before="+idText(old.ID+1)), asDatastar())
		c.requireQuiet()
		e.say("carol", crew, "newest")
		// Not the new message; only the count of the sidebar.
		badges, others := countPatchesOf(c.drain())
		if len(others) != 0 || len(badges) == 0 {
			t.Errorf("badges = %v, others = %+v", badges, others)
		}
		// And it was not read.
		if got := e.unreadConversations("bob"); got != 1 {
			t.Errorf("bob's unread = %d, want 1", got)
		}
	})

	t.Run("a reconnection sends everything the page shows that can be stale", func(t *testing.T) {
		e := newRoomsEnv(t)
		crew := e.group("Launch crew", "alice", "bob", "carol")
		first := e.say("alice", crew, "first")
		second := e.say("carol", crew, "second")
		c := e.serveStreams().open("bob", streamOfConversation(crew, "seen="+idText(first.ID)), asDatastar(), withHeader("Last-Event-ID", "1"))
		got := c.drain()
		requirePatch(t, got[0], "#sidebar", "inner", []string{`data-room="general"`, messageBadge(1)}, nil)
		requirePatch(t, got[1], "", "outer", []string{`id="conversation-header"`, "<h1>Launch crew</h1>"}, nil)
		requirePatch(t, got[2], "", "outer", []string{`id="conversation-members"`}, nil)
		// The message the page had is patched in place, the other put at the end.
		requirePatch(t, got[3], "", "outer", []string{`<li id="msg-` + idText(first.ID) + `"`, "first"}, nil)
		requirePatch(t, got[4], "#message-list", "append", []string{`<li id="msg-` + idText(second.ID) + `"`, "second"}, nil)
		requirePatch(t, got[5], "#msg-"+idText(second.ID)+" ~ #msg-"+idText(second.ID), "remove", nil, nil)
	})

	t.Run("the person who left is sent to the list", func(t *testing.T) {
		e := newRoomsEnv(t)
		crew := e.group("crew", "alice", "bob", "carol")
		e.leave("carol", crew)
		c := e.serveStreams().open("carol", streamOfConversation(crew, "seen=0"), asDatastar())
		if c.status != http.StatusOK {
			t.Fatalf("status = %d", c.status)
		}
		requireRedirect(t, patchOf(t, c.next()), convPath(crew))
	})
}

func TestConversationStreamPutsNewMessagesAtTheEnd(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	crew := e.group("crew", "alice", "bob", "carol")
	seen := e.say("alice", crew, "before")
	ss := e.serveStreams()
	bob := ss.open("bob", streamOfConversation(crew, "seen="+idText(seen.ID)), asDatastar())
	alice := ss.open("alice", streamOfConversation(crew, "seen="+idText(seen.ID)), asDatastar())
	bob.requireQuiet()
	alice.requireQuiet()

	// carol writes: both see it, and each stream marks it read (D33).
	reply := e.sayIn("carol", crew, app.MessageInput{Body: "Hello <everyone>", ReplyToID: seen.ID})
	id := idText(reply.ID)
	for name, c := range map[string]*streamClient{"bob": bob, "alice": alice} {
		got := c.drain()
		first := requireOne(t, got, `<li id="msg-`+id+`"`)
		requireContains(t, first.html, []string{
			`class="message"`, `Hello &lt;everyone&gt;`, `<span class="message-author">Carol</span>`, `data-quoted="` + idText(seen.ID) + `"`,
			`>Reply</a>`,
		}, []string{`class="message own"`, "message-edit-link", "Hello <everyone>"})
		if first.selector != "#message-list" || first.mode != "append" {
			t.Errorf("%s: patch = %+v", name, first)
		}
		requireOne(t, got, "#msg-"+id+" ~ #msg-"+id)
		badges, _ := countPatchesOf(got)
		if len(badges) == 0 || badges[len(badges)-1] != messageBadge(0) {
			t.Errorf("%s: message badges = %v, want the last to be empty", name, badges)
		}
	}
	for _, who := range []string{"alice", "bob"} {
		if got := e.unreadConversations(who); got != 0 {
			t.Errorf("%s's unread = %d, want 0: the open page counts as read", who, got)
		}
	}

	// A person's own message, written in another tab, comes as an own message
	// with its buttons, and needs no marking.
	mine := e.say("bob", crew, "from my phone")
	got := bob.drain()
	requireContains(t, requireOne(t, got, `<li id="msg-`+idText(mine.ID)+`"`).html, []string{`class="message own"`, "message-edit-link"}, nil)
	requireOne(t, got, "#msg-"+idText(mine.ID)+" ~ #msg-"+idText(mine.ID))
}

func TestConversationStreamOrdersTheInsertionBeforeTheRemoval(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	c := e.serveStreams().open("alice", streamOfConversation(conv, "seen=0"), asDatastar())
	c.patches(1) // the state of Seen
	msg := e.say("bob", conv, "ordered")
	id := idText(msg.ID)
	got := c.patches(2)
	requirePatch(t, got[0], "#message-list", "append", []string{`id="msg-` + id + `"`}, nil)
	requirePatch(t, got[1], "#msg-"+id+" ~ #msg-"+id, "remove", nil, nil)
}

func TestConversationStreamShowsEditsDeletionsAndReactions(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	msg := e.sayIn("alice", conv, app.MessageInput{Body: "original", Uploads: []app.Upload{e.uploadMessage("alice", "f.txt")}})
	id := idText(msg.ID)
	ss := e.serveStreams()
	alice := ss.open("alice", streamOfConversation(conv, "seen="+id), asDatastar())
	bob := ss.open("bob", streamOfConversation(conv, "seen="+id), asDatastar())
	alice.patches(1)
	bob.patches(1)

	t.Run("an edit replaces the content, with the buttons of each reader", func(t *testing.T) {
		if _, err := e.msgs.Edit(t.Context(), e.actor(e.alice), msg.ID, "edited <words>"); err != nil {
			t.Fatal(err)
		}
		a := requireOne(t, alice.drain(), `id="msg-content-`+id+`"`)
		requireContains(t, a.html, []string{"edited &lt;words&gt;", ">edited</span>", "message-edit-link", `class="action message-delete"`}, []string{"original"})
		b := requireOne(t, bob.drain(), `id="msg-content-`+id+`"`)
		requireContains(t, b.html, []string{"edited &lt;words&gt;", ">edited</span>"}, []string{"message-edit-link", "message-delete", "original"})
		if a.selector != "" || a.mode != "outer" {
			t.Errorf("patch = %+v, want the content replaced by its id", a)
		}
	})

	t.Run("a reaction patches the chips alone, as each reader has them", func(t *testing.T) {
		if _, err := e.msgs.React(t.Context(), e.actor(e.bob), msg.ID, "\U0001F44D"); err != nil {
			t.Fatal(err)
		}
		for name, c := range map[string]*streamClient{"alice": alice, "bob": bob} {
			got := c.drain()
			p := requireOne(t, got, `id="msg-reactions-`+id+`"`)
			requireContains(t, p.html, []string{`class="reaction-count">1</span>`}, []string{"reaction-picker"})
			mine := name == "bob"
			if has := strings.Contains(p.html, `aria-pressed="true"`); has != mine {
				t.Errorf("%s: chip highlighted = %v, want %v", name, has, mine)
			}
			if len(patchesWith(got, "msg-reaction-picker-")) != 0 {
				t.Errorf("%s: the picker was patched: %+v", name, got)
			}
		}
	})

	t.Run("a deletion leaves the placeholder", func(t *testing.T) {
		if err := e.msgs.Delete(t.Context(), e.actor(e.alice), msg.ID); err != nil {
			t.Fatal(err)
		}
		for _, c := range []*streamClient{alice, bob} {
			got := c.drain()
			p := requireOne(t, got, `id="msg-content-`+id+`"`)
			requireContains(t, p.html, []string{"message deleted"}, []string{"edited &lt;words&gt;", "f.txt", "reaction-bar", "message-edit-link"})
		}
	})

	t.Run("a reaction to a deleted message shows nothing", func(t *testing.T) {
		// The service refuses it, so publish the event the way a race would.
		e.hub.Publish(live.Event{Topic: live.ConversationTopic(conv), Kind: live.KindMessageReaction, ConversationID: conv, MessageID: msg.ID})
		for _, c := range []*streamClient{alice, bob} {
			if got := patchesWith(c.drain(), "msg-reactions-"); len(got) != 0 {
				t.Errorf("patches = %+v", got)
			}
		}
	})
}

func TestConversationStreamMovesSeen(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	first := e.say("alice", conv, "one")
	e.say("bob", conv, "from bob")
	second := e.say("alice", conv, "two")
	ss := e.serveStreams()
	alice := ss.open("alice", streamOfConversation(conv, "seen="+idText(second.ID)), asDatastar())
	// bob has read up to his own message, which is above her second.
	got := alice.patches(2)
	requirePatch(t, got[0], ".message-seen", "remove", nil, nil)
	requireContains(t, got[1].html, []string{`id="msg-seen-` + idText(first.ID) + `"`, "Seen"}, nil)

	// bob opens the conversation: he reads her second message.
	if err := e.msgs.MarkRead(t.Context(), e.actor(e.bob), conv, second.ID); err != nil {
		t.Fatal(err)
	}
	// The count of alice's sidebar is also patched (the event is on her topic
	// only when she read).
	_, others := countPatchesOf(alice.drain())
	if len(others) != 2 {
		t.Fatalf("patches = %+v, want the removal of the old mark and the new one", others)
	}
	requirePatch(t, others[0], ".message-seen", "remove", nil, nil)
	requireContains(t, others[1].html, []string{`<div id="msg-seen-` + idText(second.ID) + `" class="message-seen-slot"><p class="message-seen">Seen</p></div>`}, nil)

	// Her own reading changes nothing on her page but the count.
	third := e.say("bob", conv, "three")
	alice.drain()
	if err := e.msgs.MarkRead(t.Context(), e.actor(e.alice), conv, third.ID); err != nil {
		t.Fatal(err)
	}
	if _, others := countPatchesOf(alice.drain()); len(others) != 0 {
		t.Errorf("patches = %+v, want only the counts", others)
	}

	// Seen is under a message that is not deleted: deleting it moves the mark.
	if err := e.msgs.Delete(t.Context(), e.actor(e.alice), second.ID); err != nil {
		t.Fatal(err)
	}
	patches := alice.drain()
	requireOne(t, patches, `id="msg-content-`+idText(second.ID)+`"`)
	requirePatch(t, requireOne(t, patches, ".message-seen"), ".message-seen", "remove", nil, nil)
	requireOne(t, patches, `<div id="msg-seen-`+idText(first.ID)+`" class="message-seen-slot"><p class="message-seen">Seen</p></div>`)
}

func TestConversationStreamUpdatesTheHeaderAndMembers(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	crew := e.group("crew", "alice", "bob", "carol")
	last := e.say("alice", crew, "x")
	c := e.serveStreams().open("bob", streamOfConversation(crew, "seen="+idText(last.ID)), asDatastar())
	c.requireQuiet()

	// A rename.
	if err := e.msgs.Rename(t.Context(), e.actor(e.alice), crew, "Release crew"); err != nil {
		t.Fatal(err)
	}
	got := c.drain()
	requireContains(t, requireOne(t, got, `id="conversation-header"`).html, []string{"<h1>Release crew</h1>", "With Alice, Carol"}, nil)
	requireContains(t, requireOne(t, got, `id="conversation-members"`).html, []string{`name="name" type="text" value="Release crew"`}, nil)
	if len(patchesWith(got, `id="sidebar"`)) != 0 {
		t.Error("the sidebar was patched for a rename")
	}

	// A member is added.
	if err := e.msgs.AddMembers(t.Context(), e.actor(e.alice), crew, []uint64{e.admin.ID}); err != nil {
		t.Fatal(err)
	}
	got = c.drain()
	requireContains(t, requireOne(t, got, `id="conversation-header"`).html, []string{"With Alice, Carol, Admin"}, nil)
	requireContains(t, requireOne(t, got, `id="conversation-members"`).html, []string{`data-username="admin"`}, nil)

	// Someone leaves.
	e.leave("carol", crew)
	got = c.drain()
	requireContains(t, requireOne(t, got, `id="conversation-header"`).html, []string{"With Alice, Admin"}, []string{"Carol"})
}

func TestConversationStreamEndsForWhoLeaves(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	crew := e.group("crew", "alice", "bob", "carol")
	last := e.say("alice", crew, "x")
	ss := e.serveStreams()
	carol := ss.open("carol", streamOfConversation(crew, "seen="+idText(last.ID)), asDatastar())
	bob := ss.open("bob", streamOfConversation(crew, "seen="+idText(last.ID)), asDatastar())
	carol.requireQuiet()

	e.leave("carol", crew)
	var redirected bool
	for _, p := range carol.drainUntilEnd() {
		if strings.Contains(p.html, redirectScript("/messages")) {
			redirected = true
		}
	}
	if !redirected {
		t.Error("carol's stream did not send her to the list")
	}
	// bob stays.
	if got := patchesWith(bob.drain(), `id="conversation-header"`); len(got) != 1 {
		t.Errorf("bob's header patches = %d, want 1", len(got))
	}

	t.Run("a departure that was not announced is found by the next message", func(t *testing.T) {
		e := newRoomsEnv(t)
		crew := e.group("crew", "alice", "bob", "carol")
		carol := e.serveStreams().open("carol", streamOfConversation(crew, "seen=0"), asDatastar())
		carol.requireQuiet()
		// A service that publishes nothing: the stream is not told.
		fs, err := files.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		quiet := app.NewMessages(e.st, e.clk, fs, app.MessagesConfig{}, nil)
		if err := quiet.Leave(t.Context(), e.actor(e.carol), crew); err != nil {
			t.Fatal(err)
		}
		carol.requireQuiet()
		e.say("alice", crew, "later")
		var redirected bool
		for _, p := range carol.drainUntilEnd() {
			redirected = redirected || strings.Contains(p.html, redirectScript("/messages"))
		}
		if !redirected {
			t.Error("carol's stream did not send her to the list")
		}
	})
}

func TestConversationStreamKeepsTheCountsLive(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	here := e.direct("alice", "bob")
	elsewhere := e.direct("alice", "carol")
	last := e.say("alice", here, "here")
	c := e.serveStreams().open("bob", streamOfConversation(here, "seen="+idText(last.ID)), asDatastar())
	c.patches(1)
	c.requireQuiet()

	// A message in another conversation raises the badge: only the counts are
	// patched, not the sidebar and not this conversation.
	e.say("carol", elsewhere, "hello alice")
	if got := e.unreadConversations("alice"); got != 1 {
		t.Fatalf("alice's unread = %d, want 1", got)
	}
	// bob, for whom nothing is unread, gets the counts of conversation.changed.
	_, others := countPatchesOf(c.drain())
	if len(others) != 0 {
		t.Errorf("patches = %+v, want only the counts", others)
	}

	// A second person gets a badge of 1 for the other conversation, live.
	carol := e.serveStreams().open("carol", streamOfConversation(elsewhere, "seen=0"), asDatastar())
	carol.drain()
	e.say("alice", e.direct("alice", "bob"), "a second one")
	// carol's own conversation is not this one: her count does not change.
	for _, p := range carol.drain() {
		if strings.Contains(p.html, `id="message-count"`) && p.html != messageBadge(0) {
			t.Errorf("carol's badge = %s, want empty", p.html)
		}
	}
}

func TestPageStreamsKeepTheMessageCountLive(t *testing.T) {
	t.Parallel()
	// The streams of the pages that have their own keep the count of unread
	// conversations of the sidebar live too (SPEC D31).
	tests := []struct {
		name string
		path func(e *roomsEnv, root app.Post) string
	}{
		{"room", func(e *roomsEnv, root app.Post) string { return e.streamOfRoom("general", "seen="+idText(root.ID)) }},
		{"thread", func(e *roomsEnv, root app.Post) string {
			return "/posts/" + idText(root.ID) + "/stream?seen=" + idText(root.ID)
		}},
		{"board", func(e *roomsEnv, root app.Post) string {
			return e.streamOfBoard("general", "v="+e.boardVersion("bob", "general"))
		}},
		{"me", func(*roomsEnv, app.Post) string { return "/me/stream" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			root := e.writePost("alice", "general", "root")
			conv := e.direct("alice", "bob")
			c := e.serveStreams().open("bob", tt.path(e, root), asDatastar())
			if tt.name == "me" {
				requireCounts(t, c, 1) // the addition to "secret"
			}
			c.requireQuiet()

			e.say("alice", conv, "hello")
			got := c.drain()
			badges, _ := countPatchesOf(got)
			if len(badges) == 0 || badges[len(badges)-1] != messageBadge(1) {
				t.Fatalf("message badges = %v, want the last to show 1", badges)
			}

			// Reading it lowers the count.
			last := e.history("bob", conv)[0]
			if err := e.msgs.MarkRead(t.Context(), e.actor(e.bob), conv, last.ID); err != nil {
				t.Fatal(err)
			}
			badges, _ = countPatchesOf(c.drain())
			if len(badges) == 0 || badges[len(badges)-1] != messageBadge(0) {
				t.Fatalf("message badges after reading = %v, want the last to be empty", badges)
			}

			// Someone else's conversation is not for this stream.
			other := e.direct("alice", "carol")
			e.say("alice", other, "for carol")
			for _, p := range c.drain() {
				if strings.Contains(p.html, `id="message-count"`) {
					t.Errorf("a count patch for someone else's conversation: %+v", p)
				}
			}
		})
	}
}

func TestMessagesPageStreamKeepsTheListLive(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	e.say("alice", conv, "first")
	ss := e.serveStreams()

	t.Run("the counts and the list come first", func(t *testing.T) {
		c := ss.open("bob", "/me/stream?messages=1", asDatastar())
		got := c.patches(3)
		requireCount(t, got[0], 1)
		requireMessageCount(t, got[1], 1)
		requirePatch(t, got[2], "", "outer", []string{`<div id="conversation-list"`, `class="conversation unread"`, `data-members="alice bob"`, "Alice: first"}, nil)
		c.requireQuiet()
	})

	t.Run("a message and its reading change the list", func(t *testing.T) {
		c := ss.open("bob", "/me/stream?messages=1", asDatastar())
		c.patches(3)
		e.say("alice", conv, "second")
		got := c.drain()
		list := requireOne(t, got, `id="conversation-list"`)
		requireContains(t, list.html, []string{"Alice: second", `class="conversation unread"`}, []string{"Alice: first"})
		if err := e.msgs.MarkRead(t.Context(), e.actor(e.bob), conv, e.history("bob", conv)[1].ID); err != nil {
			t.Fatal(err)
		}
		list = requireOne(t, c.drain(), `id="conversation-list"`)
		requireContains(t, list.html, []string{"Alice: second", `class="conversation"`}, []string{"unread"})
	})

	t.Run("the notifications page has no list of conversations", func(t *testing.T) {
		c := ss.open("bob", "/me/stream", asDatastar())
		c.patches(2)
		e.say("alice", conv, "third")
		if got := patchesWith(c.drain(), `id="conversation-list"`); len(got) != 0 {
			t.Errorf("patches = %+v", got)
		}
	})

	t.Run("a reconnection sends the sidebar and the list", func(t *testing.T) {
		c := ss.open("bob", "/me/stream?messages=1", asDatastar(), withHeader("Last-Event-ID", "1"))
		got := c.patches(2)
		requirePatch(t, got[0], "#sidebar", "inner", []string{`data-room="general"`}, nil)
		requireContains(t, got[1].html, []string{`id="conversation-list"`, "Alice: third"}, nil)
	})
}

func TestConversationStreamKeepAliveAndEnd(t *testing.T) {
	t.Parallel()
	t.Run("pings", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.group("crew", "alice", "bob", "carol")
		e.srv.pingInterval = 20 * time.Millisecond
		c := e.serveStreams().open("alice", streamOfConversation(conv, "seen=0"), asDatastar())
		for range 2 {
			if got := c.comment(); got != "ping" {
				t.Errorf("comment = %q, want ping", got)
			}
		}
	})
	t.Run("the session ends", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.group("crew", "alice", "bob", "carol")
		e.srv.pingInterval = 20 * time.Millisecond
		c := e.serveStreams().open("bob", streamOfConversation(conv, "seen=0"), asDatastar())
		e.disable("bob")
		var last patch
		for _, p := range c.drainUntilEnd() {
			last = p
		}
		requireRedirect(t, last, "/login")
	})
	t.Run("the hub closes", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.group("crew", "alice", "bob", "carol")
		c := e.serveStreams().open("alice", streamOfConversation(conv, "seen=0"), asDatastar())
		e.hub.Close()
		c.requireEnd()
		e.serveStreams().open("alice", streamOfConversation(conv, "seen=0"), asDatastar()).requireEnd()
	})
	t.Run("nobody", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.group("crew", "alice", "bob", "carol")
		c := e.serveStreams().open("", streamOfConversation(conv, "seen=0"), asDatastar())
		requireRedirect(t, patchOf(t, c.next()), "/login")
		c.requireEnd()
	})
}

func TestMessagesAreNotSentThroughTheRoomStreams(t *testing.T) {
	t.Parallel()
	// Messages are not rooms' events: the feed of a room stream stays quiet.
	e := newRoomsEnv(t)
	root := e.writePost("alice", "general", "root")
	c := e.serveStreams().open("bob", e.streamOfRoom("general", "seen="+idText(root.ID)), asDatastar())
	c.requireQuiet()
	e.say("alice", e.direct("alice", "bob"), "private")
	for _, p := range c.drain() {
		if strings.Contains(p.html, "private") || strings.Contains(p.selector, "feed") {
			t.Errorf("a room stream carries a message: %+v", p)
		}
	}
}
