package app_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
)

// The emoji of the tests.
const (
	emojiThumbsUp = "\U0001F44D"
	emojiParty    = "\U0001F389"
	emojiFire     = "\U0001F525"
	// emojiHeart is a heart with the emoji presentation selector.
	emojiHeart = "\u2764\uFE0F"
)

func TestCleanEmoji(t *testing.T) {
	picker := []string{
		"\U0001F44D", "\U0001F44E", "\U0001F600", "\U0001F602", "\U0001F389", "\u2764\uFE0F", "\U0001F64F", "\U0001F440",
		"\U0001F525", "\U0001F680", "\u2705", "\u274C", "\U0001F914", "\U0001F62E", "\U0001F622", "\U0001F621",
		"\U0001F4AF", "\U0001F44F", "\U0001F64C", "\U0001F4A1", "\u2B50", "\U0001F355", "\u2615", "\U0001F41B",
	}
	valid := map[string]string{
		"a skin tone":                 "\U0001F44D\U0001F3FD",
		"a family of four":            "\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466",
		"a couple with a heart":       "\U0001F469\u200D\u2764\uFE0F\u200D\U0001F468",
		"a flag":                      "\U0001F1EC\U0001F1F7",
		"the flag of England":         "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F",
		"a keycap one":                "1\uFE0F\u20E3",
		"a keycap hash":               "#\uFE0F\u20E3",
		"a keycap without selector":   "*\u20E3",
		"copyright with selector":     "\u00A9\uFE0F",
		"copyright":                   "\u00A9",
		"trade mark":                  "\u2122",
		"wavy dash":                   "\u3030",
		"an arrow":                    "\u27A1\uFE0F",
		"a standalone skin tone":      "\U0001F3FB",
		"a skin tone with a selector": "\u261D\uFE0F\U0001F3FD",
		"trimmed":                     "  \U0001F389 \n",
	}
	invalid := map[string]string{
		"empty":                          "",
		"spaces":                         "   ",
		"a letter":                       "a",
		"letters":                        "ok",
		"a word":                         "thumbs up",
		"a digit":                        "1",
		"digits":                         "42",
		"a hash":                         "#",
		"a keycap base and a space":      "1 \u20E3",
		"a keycap with no base":          "\u20E3",
		"a keycap after a joiner":        "\U0001F468\u200D1\u20E3",
		"a script tag":                   "<script>",
		"an emoji in a script tag":       "<\U0001F389>",
		"punctuation":                    "!",
		"a dash":                         "-",
		"two emoji":                      "\U0001F44D\U0001F389",
		"two flags":                      "\U0001F1EC\U0001F1F7\U0001F1EB\U0001F1F7",
		"three regional indicators":      "\U0001F1EC\U0001F1F7\U0001F1EB",
		"a lone regional indicator":      "\U0001F1EC",
		"two emoji joined, then a third": "\U0001F468\u200D\U0001F469\U0001F467",
		"a keycap and an emoji":          "1\uFE0F\u20E3\U0001F389",
		"two keycaps":                    "1\u20E3" + "2\u20E3",
		"a keycap with a wrong selector": "1\uFE0E\u20E3",
		"two skin tones":                 "\U0001F44D\U0001F3FD\U0001F3FD",
		"a skin tone before a selector":  "\u261D\U0001F3FD\uFE0F",
		"two selectors":                  "\u2764\uFE0F\uFE0F",
		"a lone skin tone and an emoji":  "\U0001F3FB\U0001F44D",
		"tags with no cancel":            "\U0001F3F4\U000E0067\U000E0062",
		"tags with no tag characters":    "\U0001F3F4\U000E007F",
		"tags after another emoji":       "\U0001F44D\U000E0067\U000E007F",
		"a letter among the tags":        "\U0001F3F4\U000E0067a\U000E007F",
		"a tag sequence and an emoji":    "\U0001F3F4\U000E0067\U000E007F\U0001F389",
		"an emoji and a letter":          "\U0001F389a",
		"an emoji and a space":           "\U0001F389 \U0001F44D",
		"a joiner alone":                 "\u200D",
		"a joiner first":                 "\u200D\U0001F468",
		"a joiner last":                  "\U0001F468\u200D",
		"two joiners in a row":           "\U0001F468\u200D\u200D\U0001F469",
		"five joined parts":              "\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466\u200D\U0001F476",
		"a selector alone":               "\uFE0F",
		"a selector first":               "\uFE0F\U0001F389",
		"a tag character alone":          "\U000E0067",
		"a skin tone after a joiner":     "\U0001F468\u200D\U0001F3FB",
		"too long":                       strings.Repeat("\U0001F389", 9),
		"not utf-8":                      "\xff\xfe",
		"a cyrillic letter":              "\u0416",
		"a control character":            "\u0007",
		"a zero width space":             "\u200B",
	}
	for _, e := range picker {
		valid["picker "+e] = e
	}
	for name, in := range valid {
		t.Run("valid "+name, func(t *testing.T) {
			got, ok := app.CleanEmoji(in)
			if !ok {
				t.Fatalf("CleanEmoji(%q) rejected it", in)
			}
			if got != strings.TrimSpace(in) {
				t.Errorf("CleanEmoji(%q) = %q, want it trimmed", in, got)
			}
		})
	}
	for name, in := range invalid {
		t.Run("invalid "+name, func(t *testing.T) {
			if got, ok := app.CleanEmoji(in); ok {
				t.Errorf("CleanEmoji(%q) = %q, accepted", in, got)
			}
		})
	}
	// The flag of England (28 bytes) is allowed, a tag sequence of exactly 32
	// bytes is allowed, and one with another tag character (36) is too long.
	england := "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F"
	if len(england) != 28 {
		t.Fatalf("test data: %d bytes", len(england))
	}
	if _, ok := app.CleanEmoji(england); !ok {
		t.Error("the flag of England was refused")
	}
	full := "\U0001F3F4" + strings.Repeat("\U000E0067", 6) + "\U000E007F"
	if len(full) != 32 {
		t.Fatalf("test data: %d bytes", len(full))
	}
	if _, ok := app.CleanEmoji(full); !ok {
		t.Error("32 bytes were refused")
	}
	if _, ok := app.CleanEmoji("\U0001F3F4" + strings.Repeat("\U000E0067", 7) + "\U000E007F"); ok {
		t.Error("36 bytes were accepted")
	}
}

// react toggles the emoji as the user and fails the test on an error.
func (e *postsEnv) react(user string, postID uint64, emoji string) app.Reactions {
	e.t.Helper()
	got, err := e.posts.ToggleReaction(e.t.Context(), e.as(user), postID, emoji)
	if err != nil {
		e.t.Fatalf("ToggleReaction %q by %s: %+v", emoji, user, err)
	}
	return got
}

func TestToggleReactionAddsAndRemoves(t *testing.T) {
	e := newPostsEnv(t)
	p := e.write("bob", "lobby", "react to me")
	if got := e.react("bob", p.ID, emojiParty); !reflect.DeepEqual([]app.ReactionCount(got),
		[]app.ReactionCount{{Emoji: emojiParty, Count: 1, Mine: true, Names: []string{"Bob"}}}) {
		t.Errorf("after the add: %+v", got)
	}
	if got := e.react("bob", p.ID, emojiParty); len(got) != 0 {
		t.Errorf("after the removal: %+v", got)
	}
	if n, err := e.st.ReactionsOfPosts(t.Context(), []uint64{p.ID}); err != nil || len(n) != 0 {
		t.Errorf("rows after the removal = %v, %v", n, err)
	}
	// Surrounding space is not part of the emoji.
	e.react("bob", p.ID, " "+emojiParty+" ")
	if got := e.react("bob", p.ID, emojiParty); len(got) != 0 {
		t.Errorf("a trimmed toggle did not remove the reaction: %+v", got)
	}
}

func TestReactionsOfPostsAreCountedPerActor(t *testing.T) {
	e := newPostsEnv(t)
	// lobby has admin and bob; make alice a member too.
	if err := e.rooms.Join(t.Context(), e.as("alice"), e.lobby.ID); err != nil {
		t.Fatal(err)
	}
	p := e.write("bob", "lobby", "popular")
	other := e.write("bob", "lobby", "quiet")
	c := e.say("admin", p.ID, "a comment")
	// Order of use: party (bob), fire (admin), thumbs up (alice), then party
	// again (alice) and fire (bob).
	e.react("bob", p.ID, emojiParty)
	e.react("admin", p.ID, emojiFire)
	e.react("alice", p.ID, emojiThumbsUp)
	e.react("alice", p.ID, emojiParty)
	e.react("bob", p.ID, emojiFire)
	e.react("admin", c.ID, emojiHeart)

	want := func(mineFor string) []app.ReactionCount {
		mine := func(users ...string) bool {
			for _, u := range users {
				if u == mineFor {
					return true
				}
			}
			return false
		}
		return []app.ReactionCount{
			{Emoji: emojiParty, Count: 2, Mine: mine("bob", "alice"), Names: []string{"Bob", "Alice"}},
			{Emoji: emojiFire, Count: 2, Mine: mine("admin", "bob"), Names: []string{"Admin", "Bob"}},
			{Emoji: emojiThumbsUp, Count: 1, Mine: mine("alice"), Names: []string{"Alice"}},
		}
	}
	for _, user := range []string{"bob", "admin", "alice", "carol"} {
		t.Run("Get as "+user, func(t *testing.T) {
			got, err := e.posts.Get(t.Context(), e.as(user), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Reactions, want(user)) {
				t.Errorf("reactions as %s:\n got  %+v\n want %+v", user, got.Reactions, want(user))
			}
		})
	}
	t.Run("Feed", func(t *testing.T) {
		page, err := e.posts.Feed(t.Context(), e.as("alice"), e.lobby.ID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Posts) != 2 || page.Posts[0].ID != other.ID {
			t.Fatalf("feed = %+v", page.Posts)
		}
		if got := page.Posts[0].Reactions; got == nil || len(got) != 0 {
			t.Errorf("a post without reactions has %#v, want an empty list", got)
		}
		if !reflect.DeepEqual(page.Posts[1].Reactions, want("alice")) {
			t.Errorf("reactions in the feed = %+v", page.Posts[1].Reactions)
		}
	})
	t.Run("Thread", func(t *testing.T) {
		th := e.thread("admin", p.ID)
		if !reflect.DeepEqual(th.Root.Reactions, want("admin")) {
			t.Errorf("root reactions = %+v", th.Root.Reactions)
		}
		if len(th.Comments) != 1 ||
			!reflect.DeepEqual(th.Comments[0].Reactions, []app.ReactionCount{{Emoji: emojiHeart, Count: 1, Mine: true, Names: []string{"Admin"}}}) {
			t.Errorf("comment reactions = %+v", th.Comments)
		}
		th = e.thread("bob", p.ID)
		if th.Comments[0].Reactions[0].Mine {
			t.Error("bob's view of the comment's reaction is marked as his")
		}
	})
}

func TestReactionsLeaveWhenAPostIsDeleted(t *testing.T) {
	e := newPostsEnv(t)
	p := e.write("bob", "lobby", "going")
	e.say("admin", p.ID, "keeps the thread")
	e.react("admin", p.ID, emojiParty)
	if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.posts.Get(t.Context(), e.as("admin"), p.ID)
	if err != nil || !got.Deleted || len(got.Reactions) != 0 || got.CanReact {
		t.Errorf("deleted post = %+v, %v", got, err)
	}
}

func TestPostCanReactIsForMembers(t *testing.T) {
	e := newPostsEnv(t)
	p := e.write("bob", "lobby", "hello")
	for user, want := range map[string]bool{"bob": true, "admin": true, "carol": false, "root": false} {
		got, err := e.posts.Get(t.Context(), e.as(user), p.ID)
		if err != nil || got.CanReact != want {
			t.Errorf("%s: CanReact = %v, %v, want %v", user, got.CanReact, err, want)
		}
		th := e.thread(user, p.ID)
		if th.Root.CanReact != want {
			t.Errorf("%s: thread CanReact = %v, want %v", user, th.Root.CanReact, want)
		}
	}
}

func TestToggleReactionPermissions(t *testing.T) {
	routes := map[string]string{
		app.TagNotFound:  "Posts.ToggleReaction.func1->" + notFoundPrivate,
		app.TagForbidden: "Posts.ToggleReaction.func1.Forbidden",
	}
	cases := []postCase{
		{"bob", "lobby", ""}, {"admin", "lobby", ""},
		{"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden}, {"root", "lobby", app.TagForbidden},
		{"bob", "den", ""}, {"alice", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	// The same table for a post and for a comment under it.
	runPostPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p app.Post) error {
		_, err := e.posts.ToggleReaction(t.Context(), a, p.ID, emojiParty)
		return err
	})
	runCommentPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, _, c app.Post) error {
		_, err := e.posts.ToggleReaction(t.Context(), a, c.ID, emojiParty)
		return err
	})
}

func TestToggleReactionFailures(t *testing.T) {
	t.Run("a missing post", func(t *testing.T) {
		e := newPostsEnv(t)
		_, err := e.posts.ToggleReaction(t.Context(), e.as("bob"), 9999, emojiParty)
		_ = requireTag(t, err, app.TagNotFound, "Posts.ToggleReaction.func1->Store.PostByID.NotFound")
	})
	t.Run("a deleted post", func(t *testing.T) {
		e := newPostsEnv(t)
		p := e.write("bob", "lobby", "gone soon")
		e.say("admin", p.ID, "reply")
		if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
			t.Fatal(err)
		}
		_, err := e.posts.ToggleReaction(t.Context(), e.as("bob"), p.ID, emojiParty)
		appErr := requireTag(t, err, app.TagNotFound, "Posts.ToggleReaction.func1.NotFound")
		if appErr.Metadata["reason"] != "deleted" {
			t.Errorf("metadata = %v", appErr.Metadata)
		}
	})
	t.Run("not an emoji", func(t *testing.T) {
		e := newPostsEnv(t)
		p := e.write("bob", "lobby", "react")
		for _, in := range []string{"", "a", "1", "<script>", "thumbs up", strings.Repeat(emojiParty, 9)} {
			_, err := e.posts.ToggleReaction(t.Context(), e.as("bob"), p.ID, in)
			appErr := requireTag(t, err, app.TagInvalidInput, "Posts.ToggleReaction.func1.InvalidInput")
			if got := fieldsOf(t, appErr)[app.FieldEmoji]; got == "" {
				t.Errorf("%q: no message for %s: %v", in, app.FieldEmoji, appErr.Metadata)
			}
		}
		if rows, err := e.st.ReactionsOfPosts(t.Context(), []uint64{p.ID}); err != nil || len(rows) != 0 {
			t.Errorf("rows after the refusals = %v, %v", rows, err)
		}
	})
	t.Run("permission comes before the emoji", func(t *testing.T) {
		e := newPostsEnv(t)
		p := e.write("alice", "den", "private")
		_, err := e.posts.ToggleReaction(t.Context(), e.as("carol"), p.ID, "x")
		_ = requireTag(t, err, app.TagNotFound, "Posts.ToggleReaction.func1->"+notFoundPrivate)
	})
}

func TestToggleReactionWritesNoAuditEvent(t *testing.T) {
	e := newPostsEnv(t)
	p := e.write("bob", "lobby", "react")
	before := len(e.events())
	e.react("bob", p.ID, emojiParty)
	e.react("bob", p.ID, emojiParty)
	if _, err := e.posts.ToggleReaction(t.Context(), e.as("carol"), p.ID, emojiParty); err == nil {
		t.Fatal("carol could react")
	}
	if n := len(e.events()); n != before {
		t.Errorf("reactions wrote %d audit events", n-before)
	}
}

func reactionEvts(room, post, thread uint64) []live.Event {
	return []live.Event{
		{Topic: live.RoomTopic(room), Kind: live.KindReactionChanged, RoomID: room, PostID: post},
		{Topic: live.PostTopic(thread), Kind: live.KindReactionChanged, RoomID: room, PostID: post},
	}
}

func TestLiveReactionsPublish(t *testing.T) {
	runLiveCases(t, []liveCase{
		{"add on a post", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			_, err := e.posts.ToggleReaction(e.t.Context(), e.as("admin"), p.ID, emojiParty)
			return liveResult{err: err, want: reactionEvts(e.lobby.ID, p.ID, p.ID)}
		}},
		{"remove from a post", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.react("admin", p.ID, emojiParty)
			e.rec.Reset()
			_, err := e.posts.ToggleReaction(e.t.Context(), e.as("admin"), p.ID, emojiParty)
			return liveResult{err: err, want: reactionEvts(e.lobby.ID, p.ID, p.ID)}
		}},
		{"on a comment goes to the thread of its root", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			c1 := e.say("admin", p.ID, "one")
			c2 := e.say("bob", c1.ID, "two")
			e.rec.Reset()
			_, err := e.posts.ToggleReaction(e.t.Context(), e.as("admin"), c2.ID, emojiFire)
			return liveResult{err: err, want: reactionEvts(e.lobby.ID, c2.ID, p.ID)}
		}},
		{"in a private room", func(e *liveEnv) liveResult {
			p := e.write("alice", "den", "post")
			e.rec.Reset()
			_, err := e.posts.ToggleReaction(e.t.Context(), e.as("bob"), p.ID, emojiParty)
			return liveResult{err: err, want: reactionEvts(e.den.ID, p.ID, p.ID)}
		}},
		{"not an emoji publishes nothing", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			_, err := e.posts.ToggleReaction(e.t.Context(), e.as("bob"), p.ID, "no")
			return liveResult{err: err, tag: app.TagInvalidInput}
		}},
		{"without having joined publishes nothing", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			_, err := e.posts.ToggleReaction(e.t.Context(), e.as("carol"), p.ID, emojiParty)
			return liveResult{err: err, tag: app.TagForbidden}
		}},
		{"in a private room of others publishes nothing", func(e *liveEnv) liveResult {
			p := e.write("alice", "den", "post")
			e.rec.Reset()
			_, err := e.posts.ToggleReaction(e.t.Context(), e.as("carol"), p.ID, emojiParty)
			return liveResult{err: err, tag: app.TagNotFound}
		}},
		{"a deleted post publishes nothing", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.say("admin", p.ID, "keeps it")
			if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
				e.t.Fatal(err)
			}
			e.rec.Reset()
			_, err := e.posts.ToggleReaction(e.t.Context(), e.as("bob"), p.ID, emojiParty)
			return liveResult{err: err, tag: app.TagNotFound}
		}},
		{"a missing post publishes nothing", func(e *liveEnv) liveResult {
			_, err := e.posts.ToggleReaction(e.t.Context(), e.as("bob"), 9999, emojiParty)
			return liveResult{err: err, tag: app.TagNotFound}
		}},
	})
}
