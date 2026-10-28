package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// The emoji of the tests.
const (
	partyEmoji = "\U0001F389"
	fireEmoji  = "\U0001F525"
)

// pickerEmojiForTests are the 24 emoji of the picker of SPEC §7.4.
var pickerEmojiForTests = []string{
	"\U0001F44D", "\U0001F44E", "\U0001F600", "\U0001F602", "\U0001F389", "❤️", "\U0001F64F", "\U0001F440",
	"\U0001F525", "\U0001F680", "✅", "❌", "\U0001F914", "\U0001F62E", "\U0001F622", "\U0001F621",
	"\U0001F4AF", "\U0001F44F", "\U0001F64C", "\U0001F4A1", "⭐", "\U0001F355", "☕", "\U0001F41B",
}

// reactPath is the path that reacts to the post.
func reactPath(p app.Post) string { return postPathOf(p, "reactions") }

// react toggles the emoji as the user through the service and fails the test
// on an error.
func (e *roomsEnv) react(user string, p app.Post, emoji string) {
	e.t.Helper()
	if _, err := e.posts.ToggleReaction(e.t.Context(), e.actor(e.users(user)), p.ID, emoji); err != nil {
		e.t.Fatalf("ToggleReaction %q by %s: %+v", emoji, user, err)
	}
}

// reactionsOf returns the reactions of the post as the user sees them.
func (e *roomsEnv) reactionsOf(user string, p app.Post) []app.ReactionCount {
	e.t.Helper()
	got, err := e.posts.Get(e.t.Context(), e.actor(e.users(user)), p.ID)
	if err != nil {
		e.t.Fatalf("Get: %+v", err)
	}
	return got.Reactions
}

func TestPostReact(t *testing.T) {
	t.Parallel()
	t.Run("a member reacts and takes it back", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			p := e.writePost("bob", "general", "react to this")
			form := url.Values{"emoji": {partyEmoji}}
			rec := e.post("alice", reactPath(p), form, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{
					`id="reactions-` + idText(p.ID) + `"`, `name="emoji" value="` + partyEmoji + `"`,
					`class="reaction-chip mine"`, `title="Alice"`, `<span class="reaction-count">1</span>`,
					`aria-pressed="true"`,
					// The picker is patched too: the person's own reaction closes it.
					`id="reaction-picker-` + idText(p.ID) + `"`, `<details class="reaction-add">`,
				}, "<article", "post-content", "form-error", "<details class=\"reaction-add\" open")
			} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/rooms/"+idText(e.general.ID)+"#post-"+idText(p.ID) {
				t.Fatalf("answer = %d %q, want 303 to the post in the room", rec.Code, rec.Header().Get("Location"))
			}
			got := e.reactionsOf("alice", p)
			want := []app.ReactionCount{{Emoji: partyEmoji, Count: 1, Mine: true, Names: []string{"Alice"}}}
			if len(got) != 1 || got[0].Emoji != want[0].Emoji || got[0].Count != 1 || !got[0].Mine {
				t.Fatalf("reactions = %+v, want %+v", got, want)
			}

			// The same emoji again removes it.
			rec = e.post("alice", reactPath(p), form, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{`id="reactions-` + idText(p.ID) + `"`}, partyEmoji+`</span>`, "reaction-chip")
			}
			if got := e.reactionsOf("alice", p); len(got) != 0 {
				t.Errorf("reactions after the second toggle = %+v", got)
			}
		})
	})
	t.Run("counts, names and the chip of each viewer", func(t *testing.T) {
		e := newRoomsEnv(t)
		p := e.writePost("bob", "general", "popular")
		e.react("alice", p, partyEmoji)
		e.react("bob", p, fireEmoji)
		e.react("bob", p, partyEmoji)
		rec := e.post("alice", reactPath(p), url.Values{"emoji": {fireEmoji}}, true)
		requireSSE(t, rec)
		body := rec.Body.String()
		// Party first (first used), then fire; alice has both now.
		requireOrder(t, body, `value="`+partyEmoji+`" class="reaction-chip mine" title="Alice, Bob"`, `value="`+fireEmoji+`" class="reaction-chip mine" title="Bob, Alice"`)
		requireBody(t, rec, []string{`<span class="reaction-count">2</span>`})
		// Bob's page: he put both.
		page := e.getAs("admin", postPathOf(p, ""), false).Body.String()
		requireContains(t, article(t, page, p), []string{
			`value="` + partyEmoji + `" class="reaction-chip" title="Alice, Bob"`,
			`value="` + fireEmoji + `" class="reaction-chip" title="Bob, Alice"`,
		}, []string{"reaction-chip mine"})
	})
	t.Run("a comment, on its page", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			d := e.demoThread()
			form := url.Values{"emoji": {partyEmoji}, "from": {"post"}}
			rec := e.post("alice", reactPath(d.first), form, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{`id="reactions-` + idText(d.first.ID) + `"`, `reaction-chip mine`, `name="from" value="post"`})
			} else if want := "/posts/" + idText(d.root.ID) + "#post-" + idText(d.first.ID); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
				t.Fatalf("answer = %d %q, want 303 to %s", rec.Code, rec.Header().Get("Location"), want)
			}
			if got := e.reactionsOf("bob", d.first); len(got) != 1 || got[0].Mine {
				t.Errorf("bob sees %+v, want one reaction that is not his", got)
			}
		})
	})
	t.Run("on the page of the post a plain post goes back there", func(t *testing.T) {
		e := newRoomsEnv(t)
		p := e.writePost("bob", "general", "mine")
		rec := e.post("alice", reactPath(p), url.Values{"emoji": {partyEmoji}, "from": {"post"}}, false)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/posts/"+idText(p.ID) {
			t.Errorf("answer = %d %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("a reaction in a private room by a member", func(t *testing.T) {
		e := newRoomsEnv(t)
		p := e.writePost("alice", "secret", "hush")
		rec := e.post("bob", reactPath(p), url.Values{"emoji": {fireEmoji}}, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{"reaction-chip mine", `title="Bob"`})
	})
}

func TestPostReactNotAnEmoji(t *testing.T) {
	t.Parallel()
	for _, emoji := range []string{"", "a", "42", "<script>alert(1)</script>", "thumbs up", strings.Repeat(partyEmoji, 9)} {
		t.Run("datastar "+emoji, func(t *testing.T) {
			e := newRoomsEnv(t)
			p := e.writePost("bob", "general", "react")
			rec := e.post("alice", reactPath(p), url.Values{"emoji": {emoji}}, true)
			requireSSE(t, rec)
			// The picker alone, open, with the problem next to the field and
			// what was typed kept (escaped); the chips are not patched.
			requireBody(t, rec, []string{
				`id="reaction-picker-` + idText(p.ID) + `"`, `<details class="reaction-add" open>`, `aria-invalid="true"`,
				`class="field-error"`, "Use an emoji",
			}, "<script>", `id="reactions-`)
			if len(emoji) <= typedEmojiMax && emoji != "" {
				requireBody(t, rec, []string{`value="` + htmlEscape(emoji) + `"`})
			}
			if got := e.reactionsOf("alice", p); len(got) != 0 {
				t.Errorf("a reaction was stored: %+v", got)
			}
		})
	}
	t.Run("plain", func(t *testing.T) {
		e := newRoomsEnv(t)
		p := e.writePost("bob", "general", "react")
		rec := e.post("alice", reactPath(p), url.Values{"emoji": {"nope"}}, false)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		requireBody(t, rec, []string{"Use an emoji", `id="post-` + idText(p.ID) + `"`})
		if got := e.reactionsOf("alice", p); len(got) != 0 {
			t.Errorf("a reaction was stored: %+v", got)
		}
	})
	t.Run("a missing field", func(t *testing.T) {
		e := newRoomsEnv(t)
		p := e.writePost("bob", "general", "react")
		rec := e.post("alice", reactPath(p), url.Values{}, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{"Use an emoji", `id="reaction-picker-` + idText(p.ID) + `"`})
	})
}

// htmlEscape escapes the characters that templ escapes in an attribute.
func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}

func TestPostReactByANonMember(t *testing.T) {
	t.Parallel()
	t.Run("a visible room: the message above the bar, nothing stored", func(t *testing.T) {
		e := newRoomsEnv(t)
		p := e.writePost("bob", "general", "visible")
		e.react("alice", p, partyEmoji)
		rec := e.post("carol", reactPath(p), url.Values{"emoji": {fireEmoji}}, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			`id="reactions-` + idText(p.ID) + `"`, "You are not allowed to do that", `<ul class="reaction-chips"`, `title="Alice"`,
		}, "<details", "<form", `name="emoji"`)
		if got := e.reactionsOf("alice", p); len(got) != 1 || got[0].Emoji != partyEmoji {
			t.Errorf("reactions = %+v", got)
		}
	})
	t.Run("a visible room, plain post", func(t *testing.T) {
		e := newRoomsEnv(t)
		p := e.writePost("bob", "general", "visible")
		rec := e.post("carol", reactPath(p), url.Values{"emoji": {fireEmoji}}, false)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		requireBody(t, rec, []string{"You are not allowed to do that"})
		if got := e.reactionsOf("alice", p); len(got) != 0 {
			t.Errorf("reactions = %+v", got)
		}
	})
	t.Run("a private room is not found, admin too", func(t *testing.T) {
		e := newRoomsEnv(t)
		p := e.writePost("alice", "secret", "hush")
		for _, user := range []string{"carol", "admin"} {
			rec := e.post(user, reactPath(p), url.Values{"emoji": {fireEmoji}}, false)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: status = %d, want 404", user, rec.Code)
			}
			// A Datastar client is sent to the page, which says so.
			rec = e.post(user, reactPath(p), url.Values{"emoji": {fireEmoji}}, true)
			requireSSE(t, rec)
			requireBody(t, rec, []string{redirectScript("/posts/" + idText(p.ID))})
		}
		if got := e.reactionsOf("alice", p); len(got) != 0 {
			t.Errorf("reactions = %+v", got)
		}
	})
}

func TestPostReactToAGoneOrMissingPost(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	p := e.writePost("alice", "general", "going")
	e.reply(p, "bob")
	if err := e.posts.Delete(t.Context(), e.actor(e.alice), p.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{reactPath(p), "/posts/9999/reactions", "/posts/0/reactions", "/posts/x/reactions"} {
		rec := e.post("bob", path, url.Values{"emoji": {partyEmoji}}, false)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestPostReactNeedsASessionAndPost(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	p := e.writePost("bob", "general", "x")
	rec := e.send(postForm(reactPath(p), url.Values{"emoji": {partyEmoji}}))
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("status = %d, Location = %q; want a redirect to the login page", rec.Code, rec.Header().Get("Location"))
	}
	if got := e.reactionsOf("alice", p); len(got) != 0 {
		t.Errorf("reactions = %+v", got)
	}
	req := httptest.NewRequest(http.MethodGet, reactPath(p), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: e.session("alice")})
	if rec := e.send(req); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
	big := strings.Repeat("x", maxFormBytes+1)
	if rec := e.post("alice", reactPath(p), url.Values{"emoji": {big}}, false); rec.Code != http.StatusBadRequest {
		t.Errorf("a form that is too large: status = %d, want 400", rec.Code)
	}
}

func TestReactionBarOnThePages(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	p := e.writePost("bob", "general", "with reactions")
	other := e.writePost("bob", "general", "without")
	e.react("alice", p, partyEmoji)
	c := e.commentOn(p, "bob", "a comment")
	e.react("bob", c, fireEmoji)
	deleted := e.writePost("alice", "general", "to be a placeholder")
	e.react("bob", deleted, partyEmoji)
	e.reply(deleted, "bob")
	if err := e.posts.Delete(t.Context(), e.actor(e.alice), deleted.ID); err != nil {
		t.Fatal(err)
	}

	t.Run("feed, member", func(t *testing.T) {
		body := e.getAs("alice", "/rooms/"+idText(e.general.ID), false).Body.String()
		requireContains(t, article(t, body, p), []string{
			`id="reactions-` + idText(p.ID) + `"`, `<form class="reaction-chips"`, `reaction-chip mine`, `<details class="reaction-add">`,
			`<summary title="Add a reaction"`, `class="reaction-grid"`, `class="reaction-custom"`, `name="emoji"`, `maxlength="32"`,
		}, []string{`name="from"`, "open>"})
		// The add button is there for a post with no reaction, but no chip.
		requireContains(t, article(t, body, other), []string{`id="reactions-` + idText(other.ID) + `"`, `<details class="reaction-add">`},
			[]string{"reaction-chips", "reaction-chip"})
		// A deleted post has no bar.
		requireContains(t, article(t, body, deleted), []string{"This post was deleted."}, []string{"reactions-", "reaction-chip", partyEmoji})
	})
	t.Run("the picker has the 24 emoji", func(t *testing.T) {
		body := article(t, e.getAs("alice", "/rooms/"+idText(e.general.ID), false).Body.String(), other)
		if n := strings.Count(body, `class="reaction-option"`); n != 24 {
			t.Errorf("%d picker buttons, want 24", n)
		}
		for _, emoji := range pickerEmojiForTests {
			if !strings.Contains(body, `name="emoji" value="`+emoji+`" class="reaction-option"`) {
				t.Errorf("no picker button for %q", emoji)
			}
		}
	})
	t.Run("feed, a non-member sees the chips and no way to react", func(t *testing.T) {
		body := e.getAs("carol", "/rooms/"+idText(e.general.ID), false).Body.String()
		card := article(t, body, p)
		requireContains(t, card, []string{`<ul class="reaction-chips"`, `title="Alice"`, partyEmoji, `<span class="reaction-count">1</span>`},
			[]string{`<details class="reaction-add"`, "reaction-add", "reaction-grid", `name="emoji"`, "reaction-chip mine"})
		requireContains(t, article(t, body, other), nil, []string{`<details class="reaction-add"`, "reaction-add", "reaction-chips"})
	})
	t.Run("post page: the bars of the post and the comment, with from", func(t *testing.T) {
		body := e.getAs("bob", postPathOf(p, ""), false).Body.String()
		requireContains(t, article(t, body, p), []string{`id="reactions-` + idText(p.ID) + `"`, `reaction-chip`, `name="from" value="post"`}, []string{"reaction-chip mine"})
		requireContains(t, article(t, body, c), []string{`id="reactions-` + idText(c.ID) + `"`, "reaction-chip mine", fireEmoji, `name="from" value="post"`}, nil)
	})
	t.Run("post page, a non-member", func(t *testing.T) {
		body := e.getAs("carol", postPathOf(p, ""), false).Body.String()
		requireContains(t, article(t, body, c), []string{`<ul class="reaction-chips"`, fireEmoji}, []string{"<details", "reaction-add"})
	})
	t.Run("a deleted post's thread has no bar", func(t *testing.T) {
		body := e.getAs("bob", postPathOf(deleted, ""), false).Body.String()
		requireContains(t, article(t, body, deleted), []string{"This post was deleted."}, []string{"reactions-", "reaction-chip"})
	})
}

func TestReactionTitleListsTheFirstTwentyNames(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	p := e.writePost("bob", "general", "crowd")
	// Twenty-two people react; the title names twenty and counts the rest.
	for i := range 22 {
		name := "member" + idText(uint64(i))
		u := e.addUser(name, "Member "+idText(uint64(i)), app.RoleMember)
		if err := e.rooms.Join(t.Context(), e.actor(u), e.general.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.posts.ToggleReaction(t.Context(), e.actor(u), p.ID, partyEmoji); err != nil {
			t.Fatal(err)
		}
	}
	body := e.getAs("bob", postPathOf(p, ""), false).Body.String()
	requireContains(t, article(t, body, p), []string{
		`<span class="reaction-count">22</span>`, "Member 0, Member 1,", "Member 19 and 2 more",
	}, []string{"Member 20", "Member 21"})
}
