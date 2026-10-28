package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// following reports whether the user follows the post.
func (e *roomsEnv) following(user string, postID uint64) bool {
	e.t.Helper()
	p, err := e.posts.Get(e.t.Context(), e.actor(e.users(user)), postID)
	if err != nil {
		e.t.Fatalf("Get: %+v", err)
	}
	return p.Following
}

func TestFollowAndUnfollowRoutes(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		post := e.writePost("alice", "general", "A post to follow")
		var opts []reqOpt
		if datastar {
			opts = append(opts, asDatastar())
		}
		path := "/posts/" + idText(post.ID)
		if !e.following("alice", post.ID) || e.following("bob", post.ID) {
			t.Fatal("the author follows their post and bob does not")
		}

		// Bob follows: the post comes back with Unfollow (Datastar), or he is
		// sent to the post.
		rec := e.send(postForm(path+"/follow", url.Values{}, append([]reqOpt{e.cookies["bob"]}, opts...)...))
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="post-content-` + idText(post.ID) + `"`, `action="` + path + `/unfollow"`, ">Unfollow<"}, ">Follow<")
		} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path {
			t.Fatalf("status = %d, Location = %q:\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
		}
		if !e.following("bob", post.ID) {
			t.Fatal("bob does not follow after the Follow button")
		}

		// Again is no error; then Unfollow brings Follow back.
		if rec = e.send(postForm(path+"/follow", url.Values{}, append([]reqOpt{e.cookies["bob"]}, opts...)...)); rec.Code >= 400 {
			t.Fatalf("following twice: status %d", rec.Code)
		}
		rec = e.send(postForm(path+"/unfollow", url.Values{}, append([]reqOpt{e.cookies["bob"]}, opts...)...))
		if datastar {
			requireBody(t, rec, []string{`action="` + path + `/follow"`, ">Follow<"}, ">Unfollow<")
		}
		if e.following("bob", post.ID) {
			t.Fatal("bob still follows after Unfollow")
		}
	})
}

func TestFollowRoutesRefuseWhoCannotReadThePost(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	secret := e.writePost("alice", "secret", "Only a few of us")
	for _, action := range []string{"follow", "unfollow"} {
		for _, datastar := range []bool{false, true} {
			opts := []reqOpt{e.cookies["carol"]}
			if datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/posts/"+idText(secret.ID)+"/"+action, url.Values{}, opts...))
			if !datastar && rec.Code != http.StatusNotFound {
				t.Errorf("%s as carol: status = %d, want 404", action, rec.Code)
			}
			if datastar && !strings.Contains(rec.Body.String(), "/posts/"+idText(secret.ID)) {
				t.Errorf("%s as carol (datastar): not sent to the post:\n%s", action, rec.Body)
			}
		}
	}
	if rec := e.send(postForm("/posts/99999/follow", url.Values{}, e.cookies["carol"])); rec.Code != http.StatusNotFound {
		t.Errorf("a missing post: status = %d, want 404", rec.Code)
	}
	if rec := e.send(postForm("/posts/"+idText(e.writePost("alice", "general", "x").ID)+"/follow", url.Values{})); rec.Code != http.StatusFound {
		t.Errorf("no login: status = %d, want 302 to the login page", rec.Code)
	}
}

func TestPostCardOffersFollowOrUnfollow(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	post := e.writePost("alice", "general", "Hello")
	comment, err := e.posts.Reply(t.Context(), e.actor(e.bob), post.ID, app.PostInput{Format: app.FormatText, Body: "A comment"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/posts/" + idText(post.ID)
	// The post page: alice follows (Unfollow), carol does not (Follow); the
	// comment has neither.
	alice := e.send(get(path, e.cookies["alice"])).Body.String()
	carol := e.send(get(path, e.cookies["carol"])).Body.String()
	if n := strings.Count(alice, `class="action post-follow"`); n != 1 || !strings.Contains(alice, ">Unfollow<") || strings.Contains(alice, ">Follow<") {
		t.Errorf("alice's page: %d follow forms, want one with Unfollow", n)
	}
	if n := strings.Count(carol, `class="action post-follow"`); n != 1 || !strings.Contains(carol, ">Follow<") {
		t.Errorf("carol's page: %d follow forms, want one with Follow", n)
	}
	_ = comment
	// The feed card has the button too.
	if rec := e.send(get("/rooms/"+idText(e.general.ID), e.cookies["alice"])); !strings.Contains(rec.Body.String(), `class="action post-follow"`) {
		t.Error("the feed card of the post has no follow button")
	}
}

func TestSettingsKeywordsForm(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	page := e.send(get("/me/settings", e.cookies["bob"]))
	requireBody(t, page, []string{
		`<legend>Mention keywords</legend>`, `name="groupMentions" value="on" checked`, `@room, @all and @here`,
		`id="notify-keywords"`, `name="keywords" value=""`,
	})

	base := url.Values{"notify": {"mentions"}, "sound": {"on"}, "soundName": {"purr"}, "replies": {"following"}}
	with := func(kv ...string) url.Values {
		f := url.Values{}
		for k, v := range base {
			f[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			f.Set(kv[i], kv[i+1])
		}
		return f
	}
	stored := func() app.Settings {
		s, err := e.accounts.Settings(t.Context(), e.actor(e.bob))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	rec := e.send(postForm("/me/settings/notifications", with("keywords", " deploy , Feeder   schedule,,", "groupMentions", "on"), e.cookies["bob"]))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303:\n%s", rec.Code, rec.Body)
	}
	if got := stored(); strings.Join(got.Keywords, "|") != "deploy|Feeder schedule" || !got.GroupMentions {
		t.Errorf("stored = %+v, want the two keywords and group mentions on", got)
	}
	page = e.send(get("/me/settings", e.cookies["bob"]))
	requireBody(t, page, []string{`name="keywords" value="deploy, Feeder schedule"`, `name="groupMentions" value="on" checked`})

	// Without the box the group mentions are off; an empty field clears the keywords.
	e.send(postForm("/me/settings/notifications", with("keywords", ""), e.cookies["bob"]))
	if got := stored(); len(got.Keywords) != 0 || got.GroupMentions {
		t.Errorf("stored = %+v, want no keywords and group mentions off", got)
	}

	// Mistakes: the message is shown and what was typed is kept; nothing is stored.
	tests := []struct {
		name, keywords, want string
	}{
		{"too many", "a1,b2,c3,d4,e5,f6,g7,h8,i9,j10,k11", "Use at most 10 keywords."},
		{"too long", strings.Repeat("x", 33), "Use at most 32 characters in a keyword."},
		{"twice", "Deploy, deploy", "is there twice."},
	}
	for _, tt := range tests {
		for _, datastar := range []bool{false, true} {
			opts := []reqOpt{e.cookies["bob"]}
			if datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/me/settings/notifications", with("keywords", tt.keywords, "groupMentions", "on"), opts...))
			if !datastar && rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s: status = %d, want 422", tt.name, rec.Code)
			}
			requireBody(t, rec, []string{tt.want, `id="notify-keywords-error"`, `value="` + tt.keywords + `"`})
		}
	}
	if got := stored(); len(got.Keywords) != 0 || got.GroupMentions {
		t.Errorf("a refused form changed the settings: %+v", got)
	}
}

func TestNotificationsPageShowsFoldedRepliesAndGroupMentions(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	post := e.writePost("bob", "general", "Feeder schedule")
	for _, name := range []string{"carol", "dave", "erin"} {
		if _, err := e.posts.Reply(t.Context(), e.actor(e.user(name)), post.ID, app.PostInput{Format: app.FormatText, Body: name + " here"}); err != nil {
			t.Fatal(err)
		}
	}
	e.writePost("alice", "general", "Heads up @room")
	rec := e.send(get("/notifications", e.cookies["bob"]))
	requireBody(t, rec, []string{
		`<span class="notification-actor">Erin</span><span class="notification-others"> and 2 others</span> replied in <a class="notification-room" href="/rooms/` + idText(e.general.ID) + `">general</a>`,
		`<span class="notification-actor">Alice</span> <span class="notification-username">@alice</span> mentioned you in`,
		`data-kind="group_mention"`, `data-kind="reply"`,
	})
	// The three replies are one notification.
	if got := strings.Count(rec.Body.String(), `data-kind="reply"`); got != 1 {
		t.Errorf("%d reply notifications, want the three replies folded into 1", got)
	}
}

func TestMeStreamRefreshesAFoldedNotification(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	post := e.writePost("bob", "general", "Feeder schedule")
	if _, err := e.posts.Reply(t.Context(), e.actor(e.carol), post.ID, app.PostInput{Format: app.FormatText, Body: "one"}); err != nil {
		t.Fatal(err)
	}
	// Bob's notifications page, with the one notification.
	first := e.notifications("bob")[0]
	unread := e.unread("bob")
	c := e.serveStreams().open("bob", "/me/stream?notifications="+idText(first.ID), asDatastar())
	c.patches(2) // the counts
	c.drainQuiet()
	if _, err := e.posts.Reply(t.Context(), e.actor(e.user("dave")), post.ID, app.PostInput{Format: app.FormatText, Body: "two"}); err != nil {
		t.Fatal(err)
	}
	got := c.patches(1)[0]
	requirePatch(t, got, "", "outer", []string{
		`id="notification-` + idText(first.ID) + `"`, `Dave`, ` and 1 other`, `replied in`,
	}, nil)
	// The bell does not change and nothing rings.
	c.requireQuiet()
	if n := e.unread("bob"); n != unread {
		t.Errorf("unread = %d, want %d", n, unread)
	}
}
