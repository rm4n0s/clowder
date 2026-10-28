package app_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/errors"
)

// mentionEnv is the prefsEnv (lobby: admin, bob, carol; den: private, alice
// and bob; crew: a group of alice, bob and carol; pair: alice and bob; dina is
// disabled) with alice and dave also in the lobby, a group "wide" of alice,
// bob, carol, dave and admin, and a presence that the test controls behind
// the posts and the messages. The lobby has five members: admin, alice, bob,
// carol and dave; root is an admin who is in no room, dina is disabled. The
// notifications and events of the fixture are forgotten, so a test sees only
// its own.
type mentionEnv struct {
	*prefsEnv
	pres *live.MemoryPresence
	wide uint64
	// mark is the largest notification ID that settle saw.
	mark uint64
}

func newMentionEnv(t *testing.T) *mentionEnv {
	t.Helper()
	e := &mentionEnv{prefsEnv: newPrefsEnv(t), pres: live.NewMemoryPresence()}
	cfg := app.PostsConfig{MaxAttachmentSize: testMaxUpload, MaxAttachments: testMaxAttachments, Presence: e.pres}
	e.posts = app.NewPosts(e.st, e.clk, markdown.New(), e.files, cfg, e.rec)
	e.msgs = app.NewMessages(e.st, e.clk, e.files, cfg, e.rec)
	for _, name := range []string{"alice", "dave"} {
		if err := e.rooms.Join(t.Context(), e.as(name), e.lobby.ID); err != nil {
			t.Fatalf("Join %s: %+v", name, err)
		}
	}
	var err error
	if e.wide, err = e.msgs.StartGroup(t.Context(), e.as("alice"), e.uids("bob", "carol", "dave", "admin"), "Wide"); err != nil {
		t.Fatalf("StartGroup: %+v", err)
	}
	e.settle()
	return e
}

// uids are the IDs of the named users.
func (e *mentionEnv) uids(names ...string) []uint64 {
	e.t.Helper()
	ids := make([]uint64, len(names))
	for i, n := range names {
		ids[i] = e.uid(n)
	}
	return ids
}

// settle forgets the notifications made so far and the published events.
func (e *mentionEnv) settle() {
	e.t.Helper()
	for name := range e.users {
		for _, n := range e.list(name) {
			e.mark = max(e.mark, n.ID)
		}
	}
	e.rec.Reset()
}

// list returns the notifications of the user, newest first.
func (e *mentionEnv) list(user string) []app.Notification {
	e.t.Helper()
	page, err := e.notes.List(e.t.Context(), e.as(user), 0, 100)
	if err != nil {
		e.t.Fatalf("List %s: %+v", user, err)
	}
	return page.Notifications
}

// news returns the notifications of the user made since the last settle,
// newest first.
func (e *mentionEnv) news(user string) []app.Notification {
	e.t.Helper()
	var out []app.Notification
	for _, n := range e.list(user) {
		if n.ID > e.mark {
			out = append(out, n)
		}
	}
	return out
}

// kinds returns the kinds of the news of the user.
func (e *mentionEnv) kinds(user string) []app.NotificationKind {
	var out []app.NotificationKind
	for _, n := range e.news(user) {
		out = append(out, n.Kind)
	}
	return out
}

// requireKinds fails the test unless every user has exactly the one
// notification that want names (and every user who is not in want has none).
func (e *mentionEnv) requireKinds(want map[string]app.NotificationKind) {
	e.t.Helper()
	for name := range e.users {
		var wantKinds []app.NotificationKind
		if k, ok := want[name]; ok {
			wantKinds = []app.NotificationKind{k}
		}
		if got := e.kinds(name); !slices.Equal(got, wantKinds) {
			e.t.Errorf("notifications of %s = %v, want %v", name, got, wantKinds)
		}
	}
	for name := range want {
		if _, ok := e.users[name]; !ok {
			e.t.Fatalf("no user %q in the fixture", name)
		}
	}
}

// post has the user write a post in the room.
func (e *mentionEnv) post(user, room string, format app.Format, body string) app.Post {
	e.t.Helper()
	p, err := e.posts.Create(e.t.Context(), e.as(user), e.room(room).ID, app.PostInput{Format: format, Body: body})
	if err != nil {
		e.t.Fatalf("Create %q: %+v", body, err)
	}
	return p
}

// comment has the user write a comment under the post or comment parent.
func (e *mentionEnv) comment(user string, parent uint64, format app.Format, body string) app.Post {
	e.t.Helper()
	c, err := e.posts.Reply(e.t.Context(), e.as(user), parent, app.PostInput{Format: format, Body: body})
	if err != nil {
		e.t.Fatalf("Reply %q: %+v", body, err)
	}
	return c
}

// edit has the user edit the post to this body (keeping its format).
func (e *mentionEnv) edit(user string, p app.Post, body string, tags ...string) app.Post {
	e.t.Helper()
	got, err := e.posts.Edit(e.t.Context(), e.as(user), p.ID, app.PostInput{Format: p.Format, Body: body, Tags: tags})
	if err != nil {
		e.t.Fatalf("Edit %q: %+v", body, err)
	}
	return got
}

// send has the user send a message to the conversation.
func (e *mentionEnv) send(user string, conv uint64, body string) app.Message {
	e.t.Helper()
	m, err := e.msgs.Send(e.t.Context(), e.as(user), conv, app.MessageInput{Body: body})
	if err != nil {
		e.t.Fatalf("Send %q: %+v", body, err)
	}
	return m
}

// disable disables the user.
func (e *mentionEnv) disable(user string) {
	e.t.Helper()
	if err := e.svc.SetDisabled(e.t.Context(), e.as("admin"), e.uid(user), true); err != nil {
		e.t.Fatalf("SetDisabled %s: %+v", user, err)
	}
}

// connect puts the users online.
func (e *mentionEnv) connect(names ...string) {
	for _, n := range names {
		e.pres.Connect(e.uid(n))
	}
}

// keywords sets the mention keywords of the user.
func (e *mentionEnv) keywords(user string, words ...string) {
	e.t.Helper()
	e.settings(user, app.SettingsUpdate{Keywords: &words})
}

// ignoreIn makes the user ignore the group mentions of the room.
func (e *mentionEnv) ignoreIn(user string, ref app.PlaceRef) {
	e.t.Helper()
	e.set(user, ref, app.PlaceUpdate{IgnoreGroupMentions: ptr(true)})
}

// an envStep is a change to the fixture that a case does before it acts.
type envStep func(e *mentionEnv)

// steps makes one envStep of several.
func steps(all ...envStep) envStep {
	return func(e *mentionEnv) {
		for _, s := range all {
			s(e)
		}
	}
}

func withKeywords(user string, words ...string) envStep {
	return func(e *mentionEnv) { e.keywords(user, words...) }
}

func ignoringInLobby(users ...string) envStep {
	return func(e *mentionEnv) {
		for _, u := range users {
			e.ignoreIn(u, app.RoomRef(e.lobby.ID))
		}
	}
}

func groupMentionsOff(users ...string) envStep {
	return func(e *mentionEnv) {
		for _, u := range users {
			e.settings(u, app.SettingsUpdate{GroupMentions: ptr(false)})
		}
	}
}

func online(users ...string) envStep {
	return func(e *mentionEnv) { e.connect(users...) }
}

// every makes the want of a case: each of the users has one notification of
// the kind.
func every(kind app.NotificationKind, users ...string) map[string]app.NotificationKind {
	out := map[string]app.NotificationKind{}
	for _, u := range users {
		out[u] = kind
	}
	return out
}

// plus returns want with one more user.
func plus(want map[string]app.NotificationKind, user string, kind app.NotificationKind) map[string]app.NotificationKind {
	out := map[string]app.NotificationKind{user: kind}
	for k, v := range want {
		out[k] = v
	}
	return out
}

// lobbyMembers are the members of the lobby of the fixture but the author
// admin.
var lobbyMembers = []string{"alice", "bob", "carol", "dave"}

func TestGroupAndKeywordNotifications(t *testing.T) {
	t.Parallel()
	group, keyword, mention := app.NotificationGroupMention, app.NotificationKeyword, app.NotificationMention
	none := map[string]app.NotificationKind{}
	md, text := app.FormatMarkdown, app.FormatText
	tests := []struct {
		name   string
		setup  envStep
		author string // admin when empty
		room   string // lobby when empty
		format app.Format
		body   string
		want   map[string]app.NotificationKind
	}{
		// @room and @all.
		{name: "@room notifies every member but the author", body: "hello @room", want: every(group, lobbyMembers...)},
		{name: "@all is the same", body: "hello @all", want: every(group, lobbyMembers...)},
		{name: "the case of the name does not matter", body: "HELLO @Room", want: every(group, lobbyMembers...)},
		{name: "@room and @all together", body: "@room @all", want: every(group, lobbyMembers...)},
		{name: "in markdown", format: md, body: "**hello** @room", want: every(group, lobbyMembers...)},
		{name: "followed by punctuation", body: "(hey @room!)", want: every(group, lobbyMembers...)},
		{name: "the author is not notified", author: "bob", body: "@room", want: every(group, "alice", "carol", "dave", "admin")},
		{name: "a member who ignores the room is left out", setup: ignoringInLobby("carol"), body: "@room", want: every(group, "alice", "bob", "dave")},
		{name: "everybody ignores it", setup: ignoringInLobby(lobbyMembers...), body: "@room", want: none},
		{name: "a member who switched group mentions off is left out", setup: groupMentionsOff("dave"), body: "@all", want: every(group, "alice", "bob", "carol")},
		{name: "both reasons together", setup: steps(ignoringInLobby("carol"), groupMentionsOff("dave")), body: "@room", want: every(group, "alice", "bob")},
		{name: "ignoring another room changes nothing", setup: func(e *mentionEnv) { e.ignoreIn("bob", app.RoomRef(e.den.ID)) }, body: "@room", want: every(group, lobbyMembers...)},
		{name: "a muted room still notifies the bell", setup: func(e *mentionEnv) { e.set("carol", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Muted: ptr(true)}) }, body: "@room", want: every(group, lobbyMembers...)},
		{name: "a disabled member is left out", setup: func(e *mentionEnv) { e.disable("carol") }, body: "@room", want: every(group, "alice", "bob", "dave")},
		{name: "a private room: only its members", author: "alice", room: "den", body: "@room", want: every(group, "bob")},
		{name: "a private room: @all and a non-member's name", author: "bob", room: "den", body: "@all @carol", want: every(group, "alice")},

		// @here.
		{name: "@here with nobody online", body: "@here", want: none},
		{name: "@here reaches the members who are online", setup: online("bob", "carol"), body: "@here", want: every(group, "bob", "carol")},
		{name: "@here does not reach the author", setup: online("admin", "bob"), body: "@here", want: every(group, "bob")},
		{name: "@here does not reach one who is not a member", setup: online("root", "bob"), body: "@here", want: every(group, "bob")},
		{name: "@here does not reach one who ignores it", setup: steps(online("bob", "carol"), ignoringInLobby("carol")), body: "@here", want: every(group, "bob")},
		{name: "@here does not reach one who switched it off", setup: steps(online("bob", "carol"), groupMentionsOff("bob")), body: "@here", want: every(group, "carol")},
		{name: "@here with several connections is still one notification", setup: online("bob", "bob"), body: "@here", want: every(group, "bob")},
		{name: "@room wins over @here", setup: online("bob"), body: "@here @room", want: every(group, lobbyMembers...)},
		{name: "@here in markdown", format: md, setup: online("dave"), body: "_ping_ @here", want: every(group, "dave")},

		// Where they do not work.
		{name: "inline code in markdown", format: md, body: "write `@room` to ping", want: none},
		{name: "a fenced block in markdown", format: md, body: "```\n@room @all @here\n```", want: none},
		{name: "after a backslash in markdown", format: md, body: `\@room`, want: none},
		{name: "in code @here is not asked either", format: md, setup: online("bob"), body: "`@here`", want: none},
		{name: "part of a word: an email address", body: "write to a@room.com", want: none},
		{name: "part of a word: a@all", body: "mail a@all", want: none},
		{name: "a longer name is a username, not @room", body: "@roomies @allies @hereby", want: none},
		{name: "in a text post the backticks are only text", format: text, body: "write `@room`", want: every(group, lobbyMembers...)},
		{name: "outside the code of a markdown post it works", format: md, body: "`code` and @room", want: every(group, lobbyMembers...)},

		// @username wins over the group mention, which wins over a keyword.
		{name: "a name and @room: one mention for the named user", body: "@bob @room", want: plus(every(group, "alice", "carol", "dave"), "bob", mention)},
		{name: "the name twice and @room", body: "@bob @bob @room @all", want: plus(every(group, "alice", "carol", "dave"), "bob", mention)},
		{name: "a name and @here", setup: online("bob", "carol"), body: "@bob @here", want: plus(every(group, "carol"), "bob", mention)},
		{name: "a name wins over the ignored room too", setup: ignoringInLobby("bob"), body: "@bob @room", want: plus(every(group, "alice", "carol", "dave"), "bob", mention)},
		{name: "@room and a keyword: the group mention", setup: withKeywords("bob", "feeder"), body: "@room the feeder", want: every(group, lobbyMembers...)},
		{name: "a name and a keyword: the mention", setup: withKeywords("bob", "feeder"), body: "@bob the feeder", want: every(mention, "bob")},
		{name: "a name, @room and a keyword: the mention", setup: withKeywords("bob", "feeder"), body: "@bob @room the feeder", want: plus(every(group, "alice", "carol", "dave"), "bob", mention)},
		{name: "ignoring the room leaves the keyword", setup: steps(withKeywords("bob", "feeder"), ignoringInLobby("bob")), body: "@room the feeder", want: plus(every(group, "alice", "carol", "dave"), "bob", keyword)},

		// Keywords.
		{name: "a keyword", setup: withKeywords("carol", "deploy"), body: "we deploy now", want: every(keyword, "carol")},
		{name: "case does not matter", setup: withKeywords("carol", "Deploy"), body: "DEPLOY now", want: every(keyword, "carol")},
		{name: "at the start and the end of the body", setup: withKeywords("carol", "deploy"), body: "deploy", want: every(keyword, "carol")},
		{name: "before punctuation and with a hyphen", setup: withKeywords("carol", "deploy"), body: "the deploy-script, deploy!", want: every(keyword, "carol")},
		{name: "a phrase", setup: withKeywords("carol", "feeder schedule"), body: "The Feeder   schedule changes", want: every(keyword, "carol")},
		{name: "a phrase across a line break", setup: withKeywords("carol", "feeder schedule"), body: "the feeder\nschedule", want: every(keyword, "carol")},
		{name: "a phrase is not its words alone", setup: withKeywords("carol", "feeder schedule"), body: "the feeder is late, the schedule too", want: none},
		{name: "inside a word does not match", setup: withKeywords("carol", "deploy"), body: "redeployed and deployment", want: none},
		{name: "inside code in markdown", format: md, setup: withKeywords("carol", "deploy"), body: "run `deploy` now", want: none},
		{name: "inside a fenced block in markdown", format: md, setup: withKeywords("carol", "deploy"), body: "```\ndeploy\n```", want: none},
		{name: "outside the code of a markdown post", format: md, setup: withKeywords("carol", "deploy"), body: "`x` then deploy", want: every(keyword, "carol")},
		{name: "in a text post the backticks are only text", format: text, setup: withKeywords("carol", "deploy"), body: "run `deploy` now", want: every(keyword, "carol")},
		{name: "in a markdown post with emphasis", format: md, setup: withKeywords("carol", "deploy"), body: "we **deploy** now", want: every(keyword, "carol")},
		{name: "the author's own keyword does not notify them", author: "carol", setup: withKeywords("carol", "deploy"), body: "I deploy", want: none},
		{name: "several keywords of one user: one notification", setup: withKeywords("carol", "deploy", "feeder", "now"), body: "deploy the feeder now", want: every(keyword, "carol")},
		{name: "several users", setup: steps(withKeywords("carol", "deploy"), withKeywords("dave", "DEPLOY", "other")), body: "deploy", want: every(keyword, "carol", "dave")},
		{name: "a user without that keyword is left alone", setup: steps(withKeywords("carol", "deploy"), withKeywords("dave", "feeder")), body: "deploy", want: every(keyword, "carol")},
		{name: "ignoring the room leaves the keyword", setup: steps(withKeywords("carol", "deploy"), ignoringInLobby("carol")), body: "deploy", want: every(keyword, "carol")},
		{name: "group mentions switched off leave the keyword", setup: steps(withKeywords("carol", "deploy"), groupMentionsOff("carol")), body: "deploy", want: every(keyword, "carol")},
		{name: "not a member of the room: no notification", setup: withKeywords("root", "deploy"), body: "deploy", want: none},
		{name: "a member of another room only", author: "alice", room: "den", setup: withKeywords("carol", "deploy"), body: "deploy", want: none},
		{name: "a private room's members", author: "alice", room: "den", setup: steps(withKeywords("bob", "deploy"), withKeywords("dave", "deploy")), body: "deploy", want: every(keyword, "bob")},
		{name: "a disabled member", setup: steps(withKeywords("carol", "deploy"), func(e *mentionEnv) { e.disable("carol") }), body: "deploy", want: none},
		{name: "an unrelated word is not a keyword", setup: withKeywords("carol", "deploy"), body: "hello there", want: none},
		{name: "a keyword with a space inside code only", format: md, setup: withKeywords("carol", "feeder schedule"), body: "`feeder schedule`", want: none},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			if tt.setup != nil {
				tt.setup(e)
				e.settle()
			}
			author, room, format := tt.author, tt.room, tt.format
			if author == "" {
				author = "admin"
			}
			if room == "" {
				room = "lobby"
			}
			if format == "" {
				format = app.FormatText
			}
			e.post(author, room, format, tt.body)
			e.requireKinds(tt.want)
		})
	}
}

func TestGroupAndKeywordNotificationsInAComment(t *testing.T) {
	t.Parallel()
	group, keyword, mention, reply := app.NotificationGroupMention, app.NotificationKeyword, app.NotificationMention, app.NotificationReply
	tests := []struct {
		name  string
		setup envStep
		// before has bob comment under the post first (so he follows it).
		bobFirst bool
		author   string
		body     string
		want     map[string]app.NotificationKind
	}{
		{name: "@room: the post's author hears the reply, the others the group mention", author: "carol", body: "hello @room",
			want: plus(every(group, "alice", "bob", "dave"), "admin", reply)},
		{name: "a follower hears the reply, not the group mention", bobFirst: true, author: "carol", body: "@room",
			want: plus(plus(every(group, "alice", "dave"), "admin", reply), "bob", reply)},
		{name: "a follower who is named hears the mention", bobFirst: true, author: "carol", body: "@bob @room",
			want: plus(plus(every(group, "alice", "dave"), "admin", reply), "bob", mention)},
		{name: "@here in a comment", setup: online("alice", "bob"), author: "carol", body: "@here",
			want: plus(every(group, "alice", "bob"), "admin", reply)},
		{name: "a keyword in a comment", setup: withKeywords("dave", "feeder"), author: "carol", body: "the feeder",
			want: plus(every(keyword, "dave"), "admin", reply)},
		{name: "the commenter does not hear their own @room", author: "admin", body: "@room",
			want: every(group, "alice", "bob", "carol", "dave")},
		{name: "ignoring the room is respected in a comment", setup: ignoringInLobby("alice"), author: "carol", body: "@room",
			want: plus(every(group, "bob", "dave"), "admin", reply)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			p := e.post("admin", "lobby", app.FormatText, "Feeder schedule")
			if tt.bobFirst {
				e.comment("bob", p.ID, app.FormatText, "first")
				// Past the minute in which a reply folds into the last one.
				e.clk.Advance(2 * time.Minute)
			}
			e.settle()
			e.comment(tt.author, p.ID, app.FormatText, tt.body)
			e.requireKinds(tt.want)
		})
	}
}

func TestEditNotifiesTheNewlyMentioned(t *testing.T) {
	t.Parallel()
	group, keyword, mention := app.NotificationGroupMention, app.NotificationKeyword, app.NotificationMention
	type step struct {
		body string
		tags []string
		want map[string]app.NotificationKind
	}
	none := map[string]app.NotificationKind{}
	tests := []struct {
		name    string
		setup   envStep
		initial string
		steps   []step
		// comment edits a comment of bob under the post instead of the post.
		comment bool
	}{
		{name: "an edit that adds @room notifies the members once", initial: "hello", steps: []step{
			{body: "hello @room", want: every(group, lobbyMembers...)},
			{body: "hello @room and more text", want: none},
			{body: "hello @room and still more", want: none},
		}},
		{name: "an unchanged edit, only the tags changed, notifies nobody new", initial: "hi @room", steps: []step{
			{body: "hi @room", tags: []string{"x"}, want: none},
			{body: "hi @room", tags: []string{"x", "y"}, want: none},
		}},
		{name: "an edit that keeps the mention and adds text", initial: "hi @room", steps: []step{
			{body: "hi @room, see the notes", want: none},
		}},
		{name: "an edit that adds the name of one already group-mentioned", initial: "hi @room", steps: []step{
			{body: "hi @room and @bob", want: none},
		}},
		{name: "an edit that names a user for the first time", initial: "hi", steps: []step{
			{body: "hi @bob", want: every(mention, "bob")},
			{body: "hi @bob and @carol", want: every(mention, "carol")},
		}},
		{name: "an edit that adds @room after a name notifies only the others", initial: "hi @bob", steps: []step{
			{body: "hi @bob @room", want: every(group, "alice", "carol", "dave")},
		}},
		{name: "an edit that adds a keyword", setup: withKeywords("carol", "feeder"), initial: "hi", steps: []step{
			{body: "hi, the feeder", want: every(keyword, "carol")},
			{body: "hi, the feeder again", want: none},
		}},
		{name: "an edit that adds @here to those online", setup: online("bob"), initial: "hi", steps: []step{
			{body: "hi @here", want: every(group, "bob")},
		}},
		{name: "an edit that removes the mention notifies nobody", initial: "hi @room", steps: []step{
			{body: "hi", want: none},
		}},
		{name: "a comment edit that adds @room", comment: true, initial: "see this", steps: []step{
			{body: "see this @room", want: every(group, "alice", "carol", "dave", "admin")},
			{body: "see this @room too", want: none},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			target := e.post("admin", "lobby", app.FormatText, tt.initial)
			author := "admin"
			if tt.comment {
				root := target
				target = e.comment("bob", root.ID, app.FormatText, tt.initial)
				author = "bob"
			}
			e.settle()
			for i, s := range tt.steps {
				target = e.edit(author, target, s.body, s.tags...)
				t.Logf("step %d: %q", i, s.body)
				e.requireKinds(s.want)
				e.settle()
			}
		})
	}
}

func TestGroupAndKeywordMentionsAreNotLinks(t *testing.T) {
	t.Parallel()
	const link = `class="mention"`
	e := newMentionEnv(t)
	e.keywords("carol", "feeder")
	tests := []struct {
		name      string
		format    app.Format
		body      string
		wantLinks int
		notWant   []string
	}{
		{"@room alone", app.FormatText, "ping @room", 0, []string{"from%3Aroom"}},
		{"@all alone in markdown", app.FormatMarkdown, "ping @all", 0, []string{"from%3Aall"}},
		{"@here alone", app.FormatText, "ping @here", 0, []string{"from%3Ahere"}},
		{"a keyword alone", app.FormatText, "the feeder is late", 0, nil},
		{"a name next to @room: only the name is a link", app.FormatText, "@bob @room", 1, []string{"from%3Aroom"}},
		{"a name next to @room in markdown", app.FormatMarkdown, "@bob @room @all", 1, []string{"from%3Aroom", "from%3Aall"}},
		{"a name next to a keyword", app.FormatText, "@bob and the feeder", 1, []string{"from%3Acarol"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := e.post("admin", "lobby", tt.format, tt.body)
			got, err := e.posts.Get(t.Context(), e.as("dave"), p.ID)
			if err != nil {
				t.Fatalf("Get: %+v", err)
			}
			for _, html := range []string{string(p.HTML), string(got.HTML)} {
				if n := strings.Count(html, link); n != tt.wantLinks {
					t.Errorf("%d mention links in %s, want %d", n, html, tt.wantLinks)
				}
				for _, s := range tt.notWant {
					if strings.Contains(html, s) {
						t.Errorf("%q is in %s", s, html)
					}
				}
			}
			// The mention is in the HTML as text, so it stays readable.
			if strings.Contains(tt.body, "@") && !strings.Contains(string(got.HTML), "@") {
				t.Errorf("the @ is gone from %s", got.HTML)
			}
		})
	}
}

func TestGroupMentionRingsByTheRoomChoice(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	lobby := app.RoomRef(e.lobby.ID)
	// alice: default (global Mentions). bob: the room on Mentions. carol: the
	// room muted. dave: the global Nothing, so his notification does not ring.
	// alice chose All for the room, which would also ring for the post: the
	// notified are not told twice.
	e.set("bob", lobby, app.PlaceUpdate{Notify: ptr(app.PlaceNotifyMentions)})
	e.set("carol", lobby, app.PlaceUpdate{Muted: ptr(true)})
	e.settings("dave", app.SettingsUpdate{Notify: ptr(app.NotifyNothing)})
	e.set("alice", lobby, app.PlaceUpdate{Notify: ptr(app.PlaceNotifyAll), Sound: ptr(app.PlaceSoundOff)})
	e.settle()

	p := e.post("admin", "lobby", app.FormatText, "ping @room")
	e.requireKinds(every(app.NotificationGroupMention, lobbyMembers...))
	wantRing := map[string]bool{"alice": true, "bob": true, "carol": false, "dave": false}
	for _, name := range lobbyMembers {
		created := e.userEvents(name, live.KindNotificationCreated)
		if len(created) != 1 {
			t.Fatalf("%s: notification events = %+v, want one", name, created)
		}
		ev := created[0]
		news := e.news(name)
		if ev.NotificationID != news[0].ID || ev.PostID != p.ID || ev.RoomID != e.lobby.ID || ev.UserID != e.uid(name) {
			t.Errorf("%s: event %+v does not match notification %+v", name, ev, news[0])
		}
		if ev.Ring != wantRing[name] {
			t.Errorf("%s: Ring = %v, want %v (%+v)", name, ev.Ring, wantRing[name], ev)
		}
		if !ev.Ring && (ev.Sound != "" || ev.Browser) {
			t.Errorf("%s: a silent event carries a sound: %+v", name, ev)
		}
		// alice is on All for the room but was notified: no second ring for the post.
		if posts := e.userEvents(name, live.KindPostCreated); len(posts) != 0 {
			t.Errorf("%s: also heard of the post: %+v", name, posts)
		}
	}
	if ev := e.userEvents("alice", live.KindNotificationCreated); ev[0].Sound != "" {
		t.Errorf("alice turned the sound off in the room: %+v", ev[0])
	}
	if ev := e.userEvents("bob", live.KindNotificationCreated); ev[0].Sound != "purr" {
		t.Errorf("bob's sound = %q, want purr", ev[0].Sound)
	}

	// A keyword rings the same way.
	e.keywords("bob", "feeder")
	e.settle()
	e.post("admin", "lobby", app.FormatText, "the feeder")
	if ev := e.userEvents("bob", live.KindNotificationCreated); len(ev) != 1 || !ev[0].Ring {
		t.Errorf("bob's keyword event = %+v, want one that rings", ev)
	}
	if got := e.kinds("bob"); !slices.Equal(got, []app.NotificationKind{app.NotificationKeyword}) {
		t.Errorf("bob's kinds = %v, want a keyword", got)
	}
}

func TestGroupMentionNotifiesTheMutedBellWithoutRinging(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	e.set("carol", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Muted: ptr(true)})
	e.settle()
	e.post("admin", "lobby", app.FormatText, "@room")
	if n, err := e.notes.UnreadCount(t.Context(), e.as("carol")); err != nil || n != 1 {
		t.Errorf("carol's unread = %d, %v; want 1", n, err)
	}
	if ev := e.userEvents("carol", live.KindNotificationCreated); len(ev) != 1 || ev[0].Ring {
		t.Errorf("carol's events = %+v, want one that does not ring", ev)
	}
}

func TestNoticeTextsOfGroupMentionsAndKeywords(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	e.keywords("carol", "feeder")
	e.settle()
	e.post("admin", "lobby", app.FormatText, "@room the feeder")
	e.post("admin", "lobby", app.FormatText, "the feeder")
	tests := []struct {
		user     string
		wantKind app.NotificationKind
		wantText string
	}{
		{"bob", app.NotificationGroupMention, "Admin mentioned you in #lobby"},
		{"carol", app.NotificationKeyword, "Admin mentioned you in #lobby"},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			created := e.userEvents(tt.user, live.KindNotificationCreated)
			if len(created) == 0 {
				t.Fatal("no event")
			}
			// The last event is the one of the last post.
			ev := created[len(created)-1]
			n, ok, err := e.prefs.Notice(t.Context(), e.as(tt.user), ev)
			if err != nil || !ok {
				t.Fatalf("Notice = %+v, %v, %v", n, ok, err)
			}
			if n.Text != tt.wantText || n.NotificationKind != tt.wantKind || n.RoomID != e.lobby.ID || n.PlaceName != "lobby" {
				t.Errorf("notice = %+v, want %q of kind %s", n, tt.wantText, tt.wantKind)
			}
			if strings.Contains(fmt.Sprintf("%+v", n), "feeder") {
				t.Errorf("the body is in the notice %+v", n)
			}
		})
	}
}

func TestGroupMentionKindsMatchTheStore(t *testing.T) {
	t.Parallel()
	if app.NotificationGroupMention != "group_mention" || app.NotificationKeyword != "keyword" {
		t.Errorf("kinds = %q, %q; want group_mention and keyword", app.NotificationGroupMention, app.NotificationKeyword)
	}
	if app.GroupMentionRoom != "room" || app.GroupMentionAll != "all" || app.GroupMentionHere != "here" {
		t.Errorf("names = %q, %q, %q", app.GroupMentionRoom, app.GroupMentionAll, app.GroupMentionHere)
	}
	if app.GroupMentionConfirmAbove != 5 {
		t.Errorf("GroupMentionConfirmAbove = %d, want 5", app.GroupMentionConfirmAbove)
	}
}

// brokenPresence is a Presence whose shared store cannot be read.
type brokenPresence struct{ *live.MemoryPresence }

const tagPresenceBroke = "PresenceBroke"

func (*brokenPresence) Online(_ context.Context, _ []uint64) (map[uint64]bool, error) {
	return nil, errors.New(tagPresenceBroke, "redis is down")
}

func TestPresenceIsAskedOnlyForHere(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	cfg := app.PostsConfig{MaxAttachmentSize: testMaxUpload, MaxAttachments: testMaxAttachments, Presence: &brokenPresence{live.NewMemoryPresence()}}
	e.posts = app.NewPosts(e.st, e.clk, markdown.New(), e.files, cfg, e.rec)
	e.msgs = app.NewMessages(e.st, e.clk, e.files, cfg, e.rec)

	// No @here: the presence is not asked, so its failure does not matter.
	e.post("admin", "lobby", app.FormatText, "@room hello")
	e.send("alice", e.wide, "@room hello")
	if _, err := e.posts.GroupMentionCount(t.Context(), e.as("admin"), e.lobby.ID, app.FormatText, "@room"); err != nil {
		t.Errorf("GroupMentionCount @room: %+v", err)
	}

	// @here: the error comes out untouched and nothing is written.
	before := len(e.list("bob"))
	_, err := e.posts.Create(t.Context(), e.as("admin"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "@here"})
	_ = requireTag(t, err, tagPresenceBroke, "Posts.Create")
	if page, ferr := e.posts.Feed(t.Context(), e.as("admin"), e.lobby.ID, 0, 50); ferr != nil || len(page.Posts) != 1 {
		t.Errorf("feed = %d posts, %v; want only the first post", len(page.Posts), ferr)
	}
	if got := len(e.list("bob")); got != before {
		t.Errorf("a failed post left %d notifications", got-before)
	}
	_, err = e.msgs.Send(t.Context(), e.as("alice"), e.wide, app.MessageInput{Body: "@here"})
	_ = requireTag(t, err, tagPresenceBroke, "Messages.Send")
	_, err = e.posts.GroupMentionCount(t.Context(), e.as("admin"), e.lobby.ID, app.FormatText, "@here")
	_ = requireTag(t, err, tagPresenceBroke, "Posts.GroupMentionCount")
	_, err = e.msgs.GroupMentionCount(t.Context(), e.as("alice"), e.wide, "@here")
	_ = requireTag(t, err, tagPresenceBroke, "Messages.GroupMentionCount")
}

// The messages of a group conversation make no notification; they ring.

func TestGroupMentionsInMessagesRing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		conv   string // "wide" or "pair"
		setup  envStep
		sender string // alice when empty
		body   string
		// probes are the members on "Mentions" for the conversation; the ones
		// in want ring, the others do not.
		want []string
	}{
		// A group conversation.
		{name: "a plain message rings nobody on Mentions", conv: "wide", body: "hello", want: nil},
		{name: "a name", conv: "wide", body: "hi @Bob", want: []string{"bob"}},
		{name: "two names", conv: "wide", body: "@bob @dave", want: []string{"bob", "dave"}},
		{name: "@room", conv: "wide", body: "@room", want: []string{"bob", "carol", "dave"}},
		{name: "@all", conv: "wide", body: "hey @all", want: []string{"bob", "carol", "dave"}},
		{name: "@here with nobody online", conv: "wide", body: "@here", want: nil},
		{name: "@here", conv: "wide", setup: online("carol"), body: "@here", want: []string{"carol"}},
		{name: "@here but not the sender", conv: "wide", setup: online("alice", "dave"), body: "@here", want: []string{"dave"}},
		{name: "@room ignored in the conversation", conv: "wide", setup: func(e *mentionEnv) { e.ignoreIn("carol", app.ConversationRef(e.wide)) }, body: "@room", want: []string{"bob", "dave"}},
		{name: "@here ignored in the conversation", conv: "wide", setup: steps(online("bob", "carol"), func(e *mentionEnv) { e.ignoreIn("carol", app.ConversationRef(e.wide)) }), body: "@here", want: []string{"bob"}},
		{name: "@room with the global switch off", conv: "wide", setup: groupMentionsOff("dave"), body: "@room", want: []string{"bob", "carol"}},
		{name: "a name still rings when @room is ignored", conv: "wide", setup: func(e *mentionEnv) { e.ignoreIn("carol", app.ConversationRef(e.wide)) }, body: "@carol", want: []string{"carol"}},
		{name: "a keyword", conv: "wide", setup: withKeywords("carol", "feeder"), body: "the Feeder is late", want: []string{"carol"}},
		{name: "a keyword is not inside a word", conv: "wide", setup: withKeywords("carol", "feeder"), body: "feeders", want: nil},
		{name: "a keyword still rings when group mentions are ignored", conv: "wide", setup: steps(withKeywords("carol", "feeder"), groupMentionsOff("carol"), func(e *mentionEnv) { e.ignoreIn("carol", app.ConversationRef(e.wide)) }), body: "@room feeder", want: []string{"bob", "carol", "dave"}},
		{name: "a keyword of the sender does not ring the sender", conv: "wide", setup: withKeywords("alice", "feeder"), body: "feeder", want: nil},
		{name: "@roomies is not @room", conv: "wide", body: "@roomies", want: nil},
		{name: "the sender is never rung", conv: "wide", sender: "bob", body: "@room", want: []string{"carol", "dave"}},
		// A direct conversation has no group mentions.
		{name: "direct: @room does nothing", conv: "pair", body: "@room", want: nil},
		{name: "direct: @all does nothing", conv: "pair", body: "@all", want: nil},
		{name: "direct: @here does nothing", conv: "pair", setup: online("bob"), body: "@here", want: nil},
		{name: "direct: a plain message does not ring a member on Mentions", conv: "pair", body: "hello", want: nil},
		{name: "direct: a name rings", conv: "pair", body: "@bob look", want: []string{"bob"}},
		{name: "direct: a keyword rings", conv: "pair", setup: withKeywords("bob", "feeder"), body: "the feeder", want: []string{"bob"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			conv, probes := e.wide, []string{"bob", "carol", "dave"}
			if tt.conv == "pair" {
				conv, probes = e.pair, []string{"bob"}
			}
			sender := tt.sender
			if sender == "" {
				sender = "alice"
			}
			for _, name := range probes {
				if name != sender {
					e.set(name, app.ConversationRef(conv), app.PlaceUpdate{Notify: ptr(app.PlaceNotifyMentions)})
				}
			}
			if tt.setup != nil {
				tt.setup(e)
			}
			e.settle()
			m := e.send(sender, conv, tt.body)
			var got []string
			for _, name := range probes {
				evs := e.userEvents(name, live.KindMessageCreated)
				if name == sender {
					if len(evs) != 0 {
						t.Errorf("the sender %s was rung: %+v", name, evs)
					}
					continue
				}
				if len(evs) > 1 {
					t.Errorf("%s heard %d events", name, len(evs))
				}
				if len(evs) == 1 {
					if !evs[0].Ring || evs[0].MessageID != m.ID || evs[0].ConversationID != conv {
						t.Errorf("%s: event %+v", name, evs[0])
					}
					got = append(got, name)
				}
			}
			if !slices.Equal(got, tt.want) && (len(got) != 0 || len(tt.want) != 0) {
				t.Errorf("rung = %v, want %v", got, tt.want)
			}
			// A message makes no notification, whatever it mentions.
			for name := range e.users {
				if n := e.news(name); len(n) != 0 {
					t.Errorf("%s has notifications %+v for a message", name, n)
				}
			}
		})
	}
}

func TestMessagesToMembersOnAllStillRingForGroupMentions(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	// The default of a group conversation is every message: a group mention
	// does not change that, nor does ignoring it.
	e.ignoreIn("carol", app.ConversationRef(e.wide))
	e.settle()
	e.send("alice", e.wide, "@room")
	for _, name := range []string{"bob", "carol", "dave", "admin"} {
		if evs := e.userEvents(name, live.KindMessageCreated); len(evs) != 1 || !evs[0].Ring {
			t.Errorf("%s: events = %+v, want one that rings", name, evs)
		}
	}
}

// The count that the editor asks before a group mention is sent.

func TestPostsGroupMentionCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		setup  envStep
		actor  string // admin when empty
		format app.Format
		body   string
		want   int
	}{
		{name: "@room counts every other member", body: "@room", want: 4},
		{name: "@all counts the same", body: "@all", want: 4},
		{name: "@room and @all together count once", body: "@room @all", want: 4},
		{name: "the actor is not counted", actor: "bob", body: "@room", want: 4},
		{name: "a member who ignores the room is not counted", setup: ignoringInLobby("carol"), body: "@room", want: 3},
		{name: "a member who switched them off is not counted", setup: groupMentionsOff("dave"), body: "@room", want: 3},
		{name: "both", setup: steps(ignoringInLobby("carol"), groupMentionsOff("dave")), body: "@room", want: 2},
		{name: "a disabled member is not counted", setup: func(e *mentionEnv) { e.disable("dave") }, body: "@room", want: 3},
		{name: "@here with nobody online", body: "@here", want: 0},
		{name: "@here counts those online", setup: online("bob", "carol"), body: "@here", want: 2},
		{name: "@here does not count the actor", setup: online("admin", "bob"), body: "@here", want: 1},
		{name: "@here does not count a user who is not a member", setup: online("root", "dina", "bob"), body: "@here", want: 1},
		{name: "@here does not count one who ignores", setup: steps(online("bob", "carol"), ignoringInLobby("carol")), body: "@here", want: 1},
		{name: "@here and @room count the members once", setup: online("bob"), body: "@here @room", want: 4},
		{name: "no group mention", body: "hello", want: 0},
		{name: "a name is not a group mention", body: "hello @bob @carol", want: 0},
		{name: "a keyword is not counted", setup: withKeywords("bob", "feeder"), body: "the feeder", want: 0},
		{name: "a keyword next to @room is not counted twice", setup: withKeywords("bob", "feeder"), body: "@room the feeder", want: 4},
		{name: "an empty body", body: "", want: 0},
		{name: "only in code", format: app.FormatMarkdown, body: "`@room` and\n```\n@all\n```", want: 0},
		{name: "in code and out of it", format: app.FormatMarkdown, body: "`@room` and @all", want: 4},
		{name: "as part of a word", body: "mail a@room.com", want: 0},
		{name: "in a text post the backticks are only text", format: app.FormatText, body: "`@room`", want: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			actor, format := tt.actor, tt.format
			if actor == "" {
				actor = "admin"
			}
			if format == "" {
				format = app.FormatText
			}
			e.settle()
			got, err := e.posts.GroupMentionCount(t.Context(), e.as(actor), e.lobby.ID, format, tt.body)
			if err != nil {
				t.Fatalf("GroupMentionCount: %+v", err)
			}
			if got != tt.want {
				t.Errorf("count = %d, want %d", got, tt.want)
			}
			// Asking writes and publishes nothing.
			if ev := e.rec.Events(); len(ev) != 0 {
				t.Errorf("asking published %+v", ev)
			}
			for name := range e.users {
				if n := e.news(name); len(n) != 0 {
					t.Errorf("asking notified %s: %+v", name, n)
				}
			}
		})
	}
}

func TestPostsGroupMentionCountPermissions(t *testing.T) {
	t.Parallel()
	const (
		route        = "Posts.GroupMentionCount"
		unauthRoute  = route + "->checkActive.Unauthenticated"
		forbidRoute  = route + ".Forbidden"
		notFoundRoom = route + "->loadReadableRoom.NotFound"
	)
	rooms := []struct {
		name string
		id   func(e *mentionEnv) uint64
		// want is the route of the error by caller; a caller who is not listed
		// succeeds.
		want map[string]string
	}{
		{"visible room (lobby: admin, alice, bob, carol, dave)", func(e *mentionEnv) uint64 { return e.lobby.ID },
			map[string]string{"root": forbidRoute, "dina": unauthRoute, "nobody": unauthRoute}},
		{"private room (den: alice, bob)", func(e *mentionEnv) uint64 { return e.den.ID },
			map[string]string{"admin": notFoundRoom, "root": notFoundRoom, "carol": notFoundRoom, "dave": notFoundRoom, "dina": unauthRoute, "nobody": unauthRoute}},
		{"no such room", func(*mentionEnv) uint64 { return 99999 },
			map[string]string{"admin": route + "->loadReadableRoom->Store.RoomByID.NotFound", "root": route + "->loadReadableRoom->Store.RoomByID.NotFound", "alice": route + "->loadReadableRoom->Store.RoomByID.NotFound",
				"bob": route + "->loadReadableRoom->Store.RoomByID.NotFound", "carol": route + "->loadReadableRoom->Store.RoomByID.NotFound", "dave": route + "->loadReadableRoom->Store.RoomByID.NotFound",
				"dina": unauthRoute, "nobody": unauthRoute}},
	}
	callers := []string{"admin", "root", "alice", "bob", "carol", "dave", "dina", "nobody"}
	for _, r := range rooms {
		for _, who := range callers {
			t.Run(r.name+"/"+who, func(t *testing.T) {
				t.Parallel()
				e := newMentionEnv(t)
				actor := app.Actor{}
				if who != "nobody" {
					actor = e.as(who)
				}
				n, err := e.posts.GroupMentionCount(t.Context(), actor, r.id(e), app.FormatText, "@room")
				wantRoute := r.want[who]
				if wantRoute == "" {
					if err != nil {
						t.Fatalf("GroupMentionCount: %+v", err)
					}
					if n == 0 {
						t.Error("a member was told that nobody is notified")
					}
					return
				}
				wantTag := app.TagNotFound
				switch wantRoute {
				case unauthRoute:
					wantTag = app.TagUnauthenticated
				case forbidRoute:
					wantTag = app.TagForbidden
				}
				_ = requireTag(t, err, wantTag, wantRoute)
				if n != 0 {
					t.Errorf("a refused call counted %d", n)
				}
			})
		}
	}
}

func TestMessagesGroupMentionCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		conv  string // "wide", "crew" or "pair"
		setup envStep
		actor string // alice when empty
		body  string
		want  int
	}{
		{name: "@room counts the other members", conv: "wide", body: "@room", want: 4},
		{name: "@all counts the same", conv: "wide", body: "@all", want: 4},
		{name: "the actor is not counted", conv: "wide", actor: "bob", body: "@room", want: 4},
		{name: "a smaller group", conv: "crew", body: "@room", want: 2},
		{name: "ignoring the conversation", conv: "wide", setup: func(e *mentionEnv) { e.ignoreIn("carol", app.ConversationRef(e.wide)) }, body: "@room", want: 3},
		{name: "ignoring another conversation changes nothing", conv: "wide", setup: func(e *mentionEnv) { e.ignoreIn("carol", app.ConversationRef(e.crew)) }, body: "@room", want: 4},
		{name: "ignoring a room changes nothing", conv: "wide", setup: ignoringInLobby("carol"), body: "@room", want: 4},
		{name: "the global switch off", conv: "wide", setup: groupMentionsOff("dave", "bob"), body: "@room", want: 2},
		{name: "a member who left is not counted", conv: "wide", setup: func(e *mentionEnv) {
			if err := e.msgs.Leave(e.t.Context(), e.as("dave"), e.wide); err != nil {
				e.t.Fatalf("Leave: %+v", err)
			}
		}, body: "@room", want: 3},
		{name: "a disabled member is not counted", conv: "wide", setup: func(e *mentionEnv) { e.disable("dave") }, body: "@room", want: 3},
		{name: "@here with nobody online", conv: "wide", body: "@here", want: 0},
		{name: "@here counts those online", conv: "wide", setup: online("bob", "dave"), body: "@here", want: 2},
		{name: "@here does not count the actor or a stranger", conv: "wide", setup: online("alice", "root", "bob"), body: "@here", want: 1},
		{name: "@here and @room", conv: "wide", setup: online("bob"), body: "@here @room", want: 4},
		{name: "no group mention", conv: "wide", body: "hello @bob", want: 0},
		{name: "a keyword is not counted", conv: "wide", setup: withKeywords("bob", "feeder"), body: "feeder", want: 0},
		{name: "a direct conversation has none: @room", conv: "pair", body: "@room", want: 0},
		{name: "a direct conversation has none: @here", conv: "pair", setup: online("bob"), body: "@here", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			conv := map[string]uint64{"wide": e.wide, "crew": e.crew, "pair": e.pair}[tt.conv]
			actor := tt.actor
			if actor == "" {
				actor = "alice"
			}
			e.settle()
			got, err := e.msgs.GroupMentionCount(t.Context(), e.as(actor), conv, tt.body)
			if err != nil {
				t.Fatalf("GroupMentionCount: %+v", err)
			}
			if got != tt.want {
				t.Errorf("count = %d, want %d", got, tt.want)
			}
			if ev := e.rec.Events(); len(ev) != 0 {
				t.Errorf("asking published %+v", ev)
			}
		})
	}
}

func TestMessagesGroupMentionCountPermissions(t *testing.T) {
	t.Parallel()
	const (
		route       = "Messages.GroupMentionCount"
		unauthRoute = route + "->checkActive.Unauthenticated"
		notFound    = route + "->loadConversation.NotFound"
	)
	places := []struct {
		name string
		id   func(e *mentionEnv) uint64
		want map[string]string
	}{
		{"group conversation (crew: alice, bob, carol)", func(e *mentionEnv) uint64 { return e.crew },
			map[string]string{"admin": notFound, "root": notFound, "dave": notFound, "dina": unauthRoute, "nobody": unauthRoute}},
		{"direct conversation (alice, bob)", func(e *mentionEnv) uint64 { return e.pair },
			map[string]string{"admin": notFound, "root": notFound, "carol": notFound, "dave": notFound, "dina": unauthRoute, "nobody": unauthRoute}},
		{"no such conversation", func(*mentionEnv) uint64 { return 99999 },
			map[string]string{"admin": route + "->loadConversation->Store.ConversationByID.NotFound", "alice": route + "->loadConversation->Store.ConversationByID.NotFound",
				"bob": route + "->loadConversation->Store.ConversationByID.NotFound", "carol": route + "->loadConversation->Store.ConversationByID.NotFound",
				"dave": route + "->loadConversation->Store.ConversationByID.NotFound", "root": route + "->loadConversation->Store.ConversationByID.NotFound",
				"dina": unauthRoute, "nobody": unauthRoute}},
	}
	for _, pl := range places {
		for _, who := range []string{"admin", "root", "alice", "bob", "carol", "dave", "dina", "nobody"} {
			t.Run(pl.name+"/"+who, func(t *testing.T) {
				t.Parallel()
				e := newMentionEnv(t)
				actor := app.Actor{}
				if who != "nobody" {
					actor = e.as(who)
				}
				n, err := e.msgs.GroupMentionCount(t.Context(), actor, pl.id(e), "@room")
				wantRoute := pl.want[who]
				if wantRoute == "" {
					if err != nil {
						t.Fatalf("GroupMentionCount: %+v", err)
					}
					return
				}
				wantTag := app.TagNotFound
				if wantRoute == unauthRoute {
					wantTag = app.TagUnauthenticated
				}
				_ = requireTag(t, err, wantTag, wantRoute)
				if n != 0 {
					t.Errorf("a refused call counted %d", n)
				}
			})
		}
	}
}
