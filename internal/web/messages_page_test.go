package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// messageItemOf returns the HTML of the item of the message in a page.
func messageItemOf(t *testing.T, body string, id uint64) string {
	t.Helper()
	start := strings.Index(body, `<li id="msg-`+idText(id)+`"`)
	if start < 0 {
		t.Fatalf("the page has no item for message %d:\n%s", id, body)
	}
	rest := body[start+1:]
	end := strings.Index(rest, `<li id="msg-`)
	if end < 0 {
		end = strings.Index(rest, "</ol>")
	}
	return body[start : start+1+end]
}

// scene is a direct conversation of alice and bob with a message of each state:
// a reply, a script and a link, files, a deleted message that is quoted, and
// reactions.
type scene struct {
	conv                   uint64
	m1, m2, m3, m4, m5     app.Message
	image, document        app.AttachmentView
	imageName, documentTxt string
}

func (e *roomsEnv) newScene() scene {
	e.t.Helper()
	s := scene{imageName: "photo.png", documentTxt: "notes.txt"}
	s.conv = e.direct("alice", "bob")
	s.m1 = e.say("alice", s.conv, "Hi Bob")
	s.m2 = e.sayIn("bob", s.conv, app.MessageInput{Body: "Yes <script>alert(1)</script> see https://example.com/x\nsecond line", ReplyToID: s.m1.ID})
	picture, err := e.msgs.Upload(e.t.Context(), e.actor(e.alice), s.imageName, strings.NewReader(string(pngOf(e.t, 4, 3))))
	if err != nil {
		e.t.Fatalf("Upload: %+v", err)
	}
	s.m3 = e.sayIn("alice", s.conv, app.MessageInput{Body: "Files", Uploads: []app.Upload{picture, e.uploadMessage("alice", s.documentTxt)}})
	for _, a := range s.m3.Attachments {
		if a.IsImage {
			s.image = a
		} else {
			s.document = a
		}
	}
	s.m4 = e.say("bob", s.conv, "to delete")
	s.m5 = e.sayIn("alice", s.conv, app.MessageInput{Body: "re deleted", ReplyToID: s.m4.ID})
	if err := e.msgs.Delete(e.t.Context(), e.actor(e.bob), s.m4.ID); err != nil {
		e.t.Fatalf("Delete: %+v", err)
	}
	for _, who := range []string{"alice", "bob"} {
		if _, err := e.msgs.React(e.t.Context(), e.actor(e.users(who)), s.m2.ID, "\U0001F44D"); err != nil {
			e.t.Fatalf("React: %+v", err)
		}
	}
	return s
}

func TestConversationPageShowsTheMessages(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	s := e.newScene()
	rec := e.send(get(convPath(s.conv), e.cookies["alice"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	requireBody(t, rec, []string{
		"<title id=\"page-title\">Bob · Clowder</title>", "<h1>Bob</h1>", `<p class="hint conversation-members-line">With Bob</p>`,
		`href="/messages">All conversations</a>`,
		`<div id="message-pane" class="message-pane"`, `<ol id="message-list" class="message-list">`,
		`<div id="messages-older" class="messages-older"></div>`, `<p id="messages-empty" class="empty messages-empty">No messages yet.</p>`,
		`<script src="/static/messages.js" defer></script>`,
		`@get(&#39;/messages/` + idText(s.conv) + `/stream?seen=` + idText(s.m5.ID) + `&#39;`,
	}, "?before=", `id="conversation-members"`)

	t.Run("oldest at the top, newest at the bottom", func(t *testing.T) {
		last := -1
		for _, m := range []app.Message{s.m1, s.m2, s.m3, s.m4, s.m5} {
			at := strings.Index(body, `<li id="msg-`+idText(m.ID)+`"`)
			if at < 0 || at < last {
				t.Fatalf("message %d is at %d after %d", m.ID, at, last)
			}
			last = at
		}
	})

	t.Run("a message of the viewer", func(t *testing.T) {
		item := messageItemOf(t, body, s.m1.ID)
		requireContains(t, item, []string{
			`class="message own"`, `data-author="alice"`, `<span class="message-author">Alice</span>`,
			`<time datetime="2026-10-26T09:30:00Z"`, `<div class="message-body"><p>Hi Bob</p>`,
			`id="msg-content-` + idText(s.m1.ID) + `"`,
			`class="message-reply-link" href="/messages/` + idText(s.conv) + `?reply=` + idText(s.m1.ID) + `#message-composer" data-on:click__prevent="@get(&#39;/messages/m/` + idText(s.m1.ID) + `/reply&#39;)">Reply</a>`,
			`class="message-edit-link" href="/messages/m/` + idText(s.m1.ID) + `/edit"`,
			`<form class="action message-delete" method="post" action="/messages/m/` + idText(s.m1.ID) + `/delete"`,
			`confirm(&#39;Delete this message? This cannot be undone.&#39;) &amp;&amp; @post(&#39;/messages/m/` + idText(s.m1.ID) + `/delete&#39;`,
		}, []string{"edited", "message-quote"})
	})

	t.Run("a message of someone else has no Edit and no Delete", func(t *testing.T) {
		item := messageItemOf(t, body, s.m2.ID)
		requireContains(t, item, []string{`class="message"`, `<span class="message-author">Bob</span>`, `>Reply</a>`},
			[]string{"message-edit-link", `class="action message-delete"`, `class="message own"`, "msg-seen-"})
	})

	t.Run("the text is escaped, line breaks and links kept", func(t *testing.T) {
		item := messageItemOf(t, body, s.m2.ID)
		requireContains(t, item, []string{
			`&lt;script&gt;alert(1)&lt;/script&gt;`,
			`<a href="https://example.com/x" rel="nofollow noopener noreferrer" target="_blank">https://example.com/x</a>`,
			"<br>", "second line",
		}, []string{"<script>alert", "</script>"})
	})

	t.Run("the quote shows the author and the start of the message", func(t *testing.T) {
		item := messageItemOf(t, body, s.m2.ID)
		requireContains(t, item, []string{`<blockquote class="message-quote" data-quoted="` + idText(s.m1.ID) + `"><span class="quote-author">Alice</span> <span class="quote-preview">Hi Bob</span></blockquote>`}, nil)
	})

	t.Run("a deleted message is a placeholder, and its quote says so", func(t *testing.T) {
		item := messageItemOf(t, body, s.m4.ID)
		requireContains(t, item, []string{`class="message message-deleted"`, `<p class="message-deleted-note">message deleted</p>`, `<span class="message-author">Bob</span>`},
			[]string{"to delete", "message-reply-link", "message-edit-link", "reaction-bar", "msg-reactions-"})
		quoted := messageItemOf(t, body, s.m5.ID)
		requireContains(t, quoted, []string{`class="message-quote message-quote-deleted" data-quoted="` + idText(s.m4.ID) + `"><span class="quote-author">Bob</span> <span class="quote-preview">message deleted</span>`}, []string{"to delete"})
	})

	t.Run("images inline, other files as links, from the messages' own path", func(t *testing.T) {
		item := messageItemOf(t, body, s.m3.ID)
		requireContains(t, item, []string{
			`<a href="/messages/files/` + idText(s.image.ID) + `/photo.png" target="_blank" rel="noopener"><img src="/messages/files/` + idText(s.image.ID) + `/photo.png" alt="photo.png" loading="lazy" width="4" height="3">`,
			`<a href="/messages/files/` + idText(s.document.ID) + `/notes.txt">notes.txt</a>`,
		}, []string{`"/files/`})
	})

	t.Run("reactions have a bar and a picker of their own", func(t *testing.T) {
		item := messageItemOf(t, body, s.m2.ID)
		requireContains(t, item, []string{
			`<div id="msg-reactions-` + idText(s.m2.ID) + `" class="reactions">`,
			`class="reaction-chip mine" title="Alice, Bob"`, `aria-label="👍 2, you reacted" aria-pressed="true"`, `<span class="reaction-count">2</span>`,
			`<div id="msg-reaction-picker-` + idText(s.m2.ID) + `" class="reaction-picker-box">`,
			`action="/messages/m/` + idText(s.m2.ID) + `/react" data-on:submit="@post(&#39;/messages/m/` + idText(s.m2.ID) + `/react&#39;, {contentType: &#39;form&#39;})"`,
			`name="emoji" value="👍" class="reaction-option"`, `id="msg-reaction-text-` + idText(s.m2.ID) + `"`,
		}, nil)
	})

	t.Run("Seen shows under the newest own message that was read", func(t *testing.T) {
		// bob wrote m4 after reading up to it: m3 is the newest of alice's
		// that he has read, and m5 is not read yet.
		requireContains(t, messageItemOf(t, body, s.m3.ID), []string{`<div id="msg-seen-` + idText(s.m3.ID) + `" class="message-seen-slot"><p class="message-seen">Seen</p></div>`}, nil)
		requireContains(t, messageItemOf(t, body, s.m1.ID), []string{`<div id="msg-seen-` + idText(s.m1.ID) + `" class="message-seen-slot"></div>`}, []string{"Seen"})
		requireContains(t, messageItemOf(t, body, s.m5.ID), []string{`<div id="msg-seen-` + idText(s.m5.ID) + `" class="message-seen-slot"></div>`}, []string{"Seen"})
		if got := strings.Count(body, `<p class="message-seen">`); got != 1 {
			t.Errorf("%d Seen marks, want 1", got)
		}
	})

	t.Run("the composer", func(t *testing.T) {
		requireContains(t, body, []string{
			`<form id="message-composer-form" class="message-composer-form" method="post" enctype="multipart/form-data" action="/messages/` + idText(s.conv) + `"`,
			`data-on:submit="@post(&#39;/messages/` + idText(s.conv) + `&#39;, {contentType: &#39;form&#39;})"`,
			`<div id="message-reply" class="message-reply"></div>`,
			`<textarea id="message-body" name="body" rows="2"`, `data-on:keydown="evt.key === &#39;Enter&#39; &amp;&amp; !evt.shiftKey`,
			`<input id="message-files" name="files" type="file" multiple>`, `<button type="submit">Send</button>`,
		}, []string{"disabled user"})
	})
}

func TestConversationPageAsTheOtherMember(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	s := e.newScene()
	body := e.send(get(convPath(s.conv), e.cookies["bob"])).Body.String()
	// alice read up to her m5, so bob's newest message that is not deleted is
	// m2, and that one shows Seen.
	requireContains(t, messageItemOf(t, body, s.m2.ID), []string{`class="message own"`, `<p class="message-seen">Seen</p>`, "message-edit-link"}, nil)
	requireContains(t, messageItemOf(t, body, s.m1.ID), []string{`class="message"`}, []string{"message-edit-link", "msg-seen-"})
	if got := strings.Count(body, `<p class="message-seen">`); got != 1 {
		t.Errorf("%d Seen marks, want 1", got)
	}
	// The deleted message of bob has no buttons for him either.
	requireContains(t, messageItemOf(t, body, s.m4.ID), []string{"message deleted"}, []string{"message-edit-link", `class="action message-delete"`, "msg-seen-"})
}

func TestOpeningAConversationMarksItRead(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	e.say("alice", conv, "one")
	last := e.say("alice", conv, "two")
	if got := e.unreadConversations("bob"); got != 1 {
		t.Fatalf("bob's unread = %d, want 1", got)
	}
	// The sidebar of the page shows the count after the marking.
	rec := e.send(get(convPath(conv), e.cookies["bob"]))
	requireBody(t, rec, []string{messageBadge(0)}, `title="1 conversation with unread messages"`)
	if got := e.unreadConversations("bob"); got != 0 {
		t.Errorf("bob's unread after opening = %d, want 0", got)
	}
	// alice sees it as Seen under her newest message.
	alice := e.send(get(convPath(conv), e.cookies["alice"])).Body.String()
	requireContains(t, messageItemOf(t, alice, last.ID), []string{`<p class="message-seen">Seen</p>`}, nil)

	t.Run("only the reader's own mark moves", func(t *testing.T) {
		e.say("bob", conv, "three")
		// alice has not opened it: it is unread for her.
		if got := e.unreadConversations("alice"); got != 1 {
			t.Errorf("alice's unread = %d, want 1", got)
		}
	})
	t.Run("a conversation with no messages has nothing to mark", func(t *testing.T) {
		empty := e.direct("alice", "carol")
		rec := e.send(get(convPath(empty), e.cookies["alice"]))
		requireBody(t, rec, []string{`<ol id="message-list" class="message-list"></ol>`, "No messages yet.", "stream?seen=0"})
	})
}

func TestConversationPageOfADisabledCounterpart(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	e.say("bob", conv, "last words")
	e.disable("bob")
	rec := e.send(get(convPath(conv), e.cookies["alice"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	requireBody(t, rec, []string{
		"last words", "With Bob (disabled)",
		`<div id="message-composer" class="message-composer"><p id="message-composer-note" class="hint join-note">You can't send messages to a disabled user.</p></div>`,
	}, "message-composer-form", "message-reply-link")

	t.Run("sending is refused", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			rec := e.post("alice", convPath(conv), url.Values{"body": {"hello?"}}, datastar)
			if datastar {
				requireSSE(t, rec)
			} else if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			requireBody(t, rec, []string{"This person is disabled, so you cannot send them messages."})
			if got := e.history("alice", conv); len(got) != 1 {
				t.Errorf("%d messages, want the one that was there", len(got))
			}
		})
	})
}

func TestGroupConversationPage(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	crew := e.group("Launch crew", "alice", "bob", "carol")
	first := e.say("alice", crew, "Welcome")
	unnamed := e.group("", "alice", "bob", "carol")
	e.disable("carol")

	rec := e.send(get(convPath(crew), e.cookies["bob"]))
	requireBody(t, rec, []string{
		"<title id=\"page-title\">(1) Launch crew · Clowder</title>", "<h1>Launch crew</h1>", "With Alice, Carol (disabled)",
		`<section id="conversation-members" class="members-panel conversation-members">`,
		`data-username="alice"`, `data-username="bob"`, `<span class="hint member-added">disabled</span>`,
		`<form id="conversation-add-form" class="add-member" method="post" action="/messages/` + idText(crew) + `/members"`,
		`<form id="conversation-rename-form" class="add-member" method="post" action="/messages/` + idText(crew) + `/rename"`,
		`name="name" type="text" value="Launch crew"`,
		`<form id="conversation-leave-form" class="action" method="post" action="/messages/` + idText(crew) + `/leave"`,
		`Leave this group?`, `<div id="msg-content-` + idText(first.ID) + `"`,
		// A group has no direct counterpart: no Seen, and the composer is open.
		`id="message-composer-form"`,
	}, "message-seen", "disabled user")
	requireBody(t, e.send(get(convPath(unnamed), e.cookies["bob"])), []string{"<h1>Alice, Carol</h1>", `name="name" type="text" value=""`})
}

func TestConversationPageSetsTheReply(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	s := e.newScene()
	other := e.direct("alice", "carol")
	foreign := e.say("alice", other, "elsewhere")

	t.Run("a reply is set from the link", func(t *testing.T) {
		rec := e.send(get(convPath(s.conv)+"?reply="+idText(s.m2.ID), e.cookies["alice"]))
		requireBody(t, rec, []string{
			`<div id="message-reply" class="message-reply"><input type="hidden" name="reply_to" value="` + idText(s.m2.ID) + `">`,
			`<blockquote class="message-quote composer-quote" data-quoted="` + idText(s.m2.ID) + `"><span class="quote-label">Replying to</span> <span class="quote-author">Bob</span>`,
			`class="quote-cancel" href="/messages/` + idText(s.conv) + `"`, `data-on:click__prevent="el.closest(&#39;#message-reply&#39;).replaceChildren()"`,
		})
	})
	for _, tt := range []struct {
		name  string
		reply uint64
	}{
		{"a deleted message cannot be replied to", s.m4.ID},
		{"a message of another conversation cannot", foreign.ID},
		{"nor one that does not exist", 9999},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get(convPath(s.conv)+"?reply="+idText(tt.reply), e.cookies["alice"]))
			requireBody(t, rec, []string{`<div id="message-reply" class="message-reply"></div>`}, `name="reply_to"`)
		})
	}
	t.Run("the slot answers a Datastar request", func(t *testing.T) {
		rec := e.send(get(msgPath(s.m2.ID, "reply"), e.cookies["alice"], asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="message-reply"`, `name="reply_to" value="` + idText(s.m2.ID) + `"`, "Replying to"})
		// A deleted message has nothing to quote: the slot is emptied.
		rec = e.send(get(msgPath(s.m4.ID, "reply"), e.cookies["alice"], asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="message-reply"`}, "reply_to")
		// A plain request is sent to the page with the composer set.
		rec = e.send(get(msgPath(s.m2.ID, "reply"), e.cookies["alice"]))
		requireRedirectTo(t, rec, false, convPath(s.conv)+"?reply="+idText(s.m2.ID)+"#message-composer")
	})
}

func TestConversationPaginates(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	var all []app.Message
	for i := range 120 {
		all = append(all, e.say("alice", conv, "message number "+idText(uint64(i+1))))
	}
	path := convPath(conv)
	has := func(body string, i int) bool { return strings.Contains(body, `id="msg-`+idText(all[i-1].ID)+`"`) }

	t.Run("the newest page has 50 and the sentinel", func(t *testing.T) {
		rec := e.send(get(path, e.cookies["bob"]))
		body := rec.Body.String()
		if got := strings.Count(body, `<li id="msg-`); got != 50 {
			t.Errorf("%d messages, want 50", got)
		}
		if !has(body, 120) || !has(body, 71) || has(body, 70) {
			t.Errorf("the page does not show messages 71 to 120")
		}
		before := idText(all[70].ID)
		requireBody(t, rec, []string{
			`<div id="messages-older" class="messages-older" data-on-intersect__once="@get(&#39;/messages/` + idText(conv) + `/older?before=` + before + `&#39;)">`,
			`<a href="/messages/` + idText(conv) + `?before=` + before + `">Older messages</a>`,
			`/stream?seen=` + idText(all[119].ID) + `&#39;`,
		}, "Back to the newest")
	})

	t.Run("a Datastar request puts the older page on top, with the next sentinel", func(t *testing.T) {
		rec := e.send(get(path+"/older?before="+idText(all[70].ID), e.cookies["bob"], asDatastar()))
		requireSSE(t, rec)
		body := rec.Body.String()
		if got := strings.Count(body, `<li id="msg-`); got != 50 {
			t.Errorf("%d messages, want 50", got)
		}
		if !has(body, 70) || !has(body, 21) || has(body, 20) || has(body, 71) {
			t.Errorf("the answer does not hold messages 21 to 70")
		}
		requireBody(t, rec, []string{
			"selector #message-list", "mode prepend", "selector #messages-older", "mode replace",
			`data-on-intersect__once="@get(&#39;/messages/` + idText(conv) + `/older?before=` + idText(all[20].ID) + `&#39;)"`,
		})
		// The last page has no sentinel left.
		rec = e.send(get(path+"/older?before="+idText(all[20].ID), e.cookies["bob"], asDatastar()))
		body = rec.Body.String()
		if got := strings.Count(body, `<li id="msg-`); got != 20 {
			t.Errorf("%d messages, want 20", got)
		}
		requireBody(t, rec, []string{`<div id="messages-older" class="messages-older"></div>`}, "data-on-intersect")
	})

	t.Run("without JavaScript the link opens the older page, which is not marked read", func(t *testing.T) {
		e.say("alice", conv, "a new one")
		if got := e.unreadConversations("bob"); got != 1 {
			t.Fatalf("bob's unread = %d, want 1", got)
		}
		rec := e.send(get(path+"?before="+idText(all[70].ID), e.cookies["bob"]))
		body := rec.Body.String()
		if !has(body, 70) || !has(body, 21) || has(body, 71) {
			t.Errorf("the page does not show messages 21 to 70")
		}
		requireBody(t, rec, []string{
			`<p class="hint feed-newest"><a href="/messages/` + idText(conv) + `">Back to the newest messages</a></p>`,
			`/stream?seen=` + idText(all[69].ID) + `&amp;before=` + idText(all[70].ID),
			`?before=` + idText(all[20].ID) + `">Older messages</a>`,
		})
		if got := e.unreadConversations("bob"); got != 1 {
			t.Errorf("bob's unread after the older page = %d, want 1: it was not read", got)
		}
		// The newest page marks it read.
		e.send(get(path, e.cookies["bob"]))
		if got := e.unreadConversations("bob"); got != 0 {
			t.Errorf("bob's unread after the newest page = %d, want 0", got)
		}
	})

	t.Run("a plain request for the older messages goes to the page", func(t *testing.T) {
		rec := e.send(get(path+"/older?before=77", e.cookies["bob"]))
		requireRedirectTo(t, rec, false, path+"?before=77")
		rec = e.send(get(path+"/older", e.cookies["bob"]))
		requireRedirectTo(t, rec, false, path)
	})
}

func TestConversationEditorOpensWithoutJavaScript(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	mine := e.say("alice", conv, "a <b>draft</b>")
	theirs := e.say("bob", conv, "not yours")
	// The message may be older than the newest page: the page ends with it.
	for range 60 {
		e.say("bob", conv, "filler")
	}
	rec := e.send(get(msgPath(mine.ID, "edit"), e.cookies["alice"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	requireContains(t, messageItemOf(t, body, mine.ID), []string{
		`<div id="msg-content-` + idText(mine.ID) + `" class="message-content message-editing">`,
		`<form class="message-edit-form" method="post" action="/messages/m/` + idText(mine.ID) + `/edit" data-on:submit="@post(&#39;/messages/m/` + idText(mine.ID) + `/edit&#39;, {contentType: &#39;form&#39;})">`,
		`<textarea id="msg-edit-body-` + idText(mine.ID) + `" name="body" rows="3">a &lt;b&gt;draft&lt;/b&gt;</textarea>`,
		`<button type="submit">Save</button>`,
		`href="/messages/` + idText(conv) + `#msg-` + idText(mine.ID) + `" data-on:click__prevent="@get(&#39;/messages/m/` + idText(mine.ID) + `/view&#39;)">Cancel</a>`,
	}, []string{"message-edit-link"})
	if strings.Contains(body, `<li id="msg-`+idText(theirs.ID)+`"`) && !strings.Contains(body, "not yours") {
		t.Error("the page lost a message")
	}

	t.Run("someone else gets the 403 page, and no editor", func(t *testing.T) {
		rec := e.send(get(msgPath(theirs.ID, "edit"), e.cookies["alice"]))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		requireBody(t, rec, []string{"You are not allowed to do that", "not yours"}, "message-editing", `name="body" rows="3"`)
	})
}
