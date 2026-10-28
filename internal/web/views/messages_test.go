package views_test

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// requireHTML fails unless the HTML has every string of want and none of
// notWant.
func requireHTML(t *testing.T, html string, want, notWant []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(html, w) {
			t.Errorf("html does not contain %q:\n%s", w, html)
		}
	}
	for _, w := range notWant {
		if strings.Contains(html, w) {
			t.Errorf("html contains %q:\n%s", w, html)
		}
	}
}

var (
	msgTime = time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	alice   = app.UserRef{ID: 2, Username: "alice", DisplayName: "Alice"}
	bob     = app.UserRef{ID: 3, Username: "bob", DisplayName: "Bob"}
)

// message makes a message of conversation 4 by alice, which bob reads.
func message(f func(m *app.Message)) app.Message {
	m := app.Message{
		ID: 12, ConversationID: 4, Author: alice, Body: "hello", CreatedAt: msgTime,
		Attachments: []app.AttachmentView{}, Reactions: []app.ReactionCount{}, CanReact: true,
	}
	if f != nil {
		f(&m)
	}
	return m
}

func TestMessageCount(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		want    []string
		notWant []string
	}{
		{"none is an empty element", 0, []string{`<span id="message-count" class="count-badge"></span>`}, []string{"title="}},
		{"one", 1, []string{`<span id="message-count" class="count-badge" title="1 conversation with unread messages">1</span>`}, nil},
		{"several", 7, []string{`title="7 conversations with unread messages">7</span>`}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireHTML(t, renderHTML(t, views.MessageCount(tt.n)), tt.want, tt.notWant)
		})
	}
}

func TestHeaderHasTheMessagesLink(t *testing.T) {
	user := app.User{DisplayName: "Bob", Username: "bob", Role: app.RoleMember}
	tests := []struct {
		name   string
		unread int
		want   string
	}{
		{"with a count", 2, `<span class="visually-hidden">Messages</span><span id="message-count" class="count-badge" title="2 conversations with unread messages">2</span></a>`},
		{"without", 0, `<span class="visually-hidden">Messages</span><span id="message-count" class="count-badge"></span></a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderHTML(t, views.Header(views.SidebarData{User: user, UnreadMessages: tt.unread}))
			requireHTML(t, html, []string{`<a class="header-link header-messages" href="/messages">`, tt.want}, nil)
		})
	}
}

func TestMessageContentStates(t *testing.T) {
	edited := msgTime.Add(time.Hour)
	tests := []struct {
		name    string
		d       views.MessageData
		want    []string
		notWant []string
	}{
		{
			name: "a message of someone else", d: views.MessageData{Message: message(nil), HTML: template.HTML("<p>hello</p>"), CanReply: true},
			want: []string{
				`<div id="msg-content-12" class="message-content">`, `<span class="message-author">Alice</span>`,
				`<time datetime="2026-10-27T09:30:00Z" title="2026-10-27T09:30:00Z">2026-10-27 09:30</time>`,
				`<div class="message-body"><p>hello</p>`, `id="msg-reactions-12"`, `id="msg-reaction-picker-12"`,
				`<a class="message-reply-link" href="/messages/4?reply=12#message-composer" data-on:click__prevent="@get(&#39;/messages/m/12/reply&#39;)">Reply</a>`,
			},
			notWant: []string{"edited", "message-edit-link", "message-delete", "message-quote", "form-error"},
		},
		{
			name: "an own message that can be edited and deleted", d: views.MessageData{
				Message: message(func(m *app.Message) { m.CanEdit, m.CanDelete = true, true }), HTML: "<p>hello</p>", Own: true, CanReply: true,
			},
			want: []string{
				`class="message-edit-link" href="/messages/m/12/edit" data-on:click__prevent="@get(&#39;/messages/m/12/edit&#39;)">Edit</a>`,
				`<form class="action message-delete" method="post" action="/messages/m/12/delete" data-on:submit="confirm(&#39;Delete this message? This cannot be undone.&#39;) &amp;&amp; @post(&#39;/messages/m/12/delete&#39;, {contentType: &#39;form&#39;})">`,
				`<button type="submit" class="danger">Delete</button>`,
			},
		},
		{
			name: "edited", d: views.MessageData{Message: message(func(m *app.Message) { m.EditedAt = &edited }), HTML: "<p>hello</p>"},
			want:    []string{`<span class="message-edited" title="2026-10-27T10:30:00Z">edited</span>`},
			notWant: []string{"message-reply-link"},
		},
		{
			name: "with a quote", d: views.MessageData{Message: message(func(m *app.Message) {
				m.ReplyTo = &app.MessageQuote{ID: 9, Author: bob, Preview: "an <earlier> one"}
			}), HTML: "<p>hello</p>"},
			want:    []string{`<blockquote class="message-quote" data-quoted="9"><span class="quote-author">Bob</span> <span class="quote-preview">an &lt;earlier&gt; one</span></blockquote>`},
			notWant: []string{"message-quote-deleted"},
		},
		{
			name: "with the quote of a deleted message", d: views.MessageData{Message: message(func(m *app.Message) {
				m.ReplyTo = &app.MessageQuote{ID: 9, Author: bob, Preview: app.MessageDeletedPreview, Deleted: true}
			}), HTML: "<p>hello</p>"},
			want: []string{`<blockquote class="message-quote message-quote-deleted" data-quoted="9"><span class="quote-author">Bob</span> <span class="quote-preview">message deleted</span></blockquote>`},
		},
		{
			name: "deleted", d: views.MessageData{Message: message(func(m *app.Message) {
				m.Deleted, m.Body, m.CanReact = true, "", false
				m.ReplyTo = &app.MessageQuote{ID: 9, Author: bob, Preview: "x"}
			}), CanReply: true, Own: true},
			want:    []string{`<p class="message-deleted-note">message deleted</p>`, `<span class="message-author">Alice</span>`, `<time datetime=`},
			notWant: []string{"message-body", "message-quote", "reaction-bar", "message-footer", "message-reply-link", "edited"},
		},
		{
			name: "files: an image inline and a file as a link", d: views.MessageData{Message: message(func(m *app.Message) {
				m.Attachments = []app.AttachmentView{
					{ID: 5, FileName: "a b.png", ContentType: "image/png", Size: 100, IsImage: true, Width: 40, Height: 30},
					{ID: 6, FileName: "notes.txt", ContentType: "text/plain", Size: 2048},
				}
			})},
			want: []string{
				`<ul class="attachment-images"><li><a href="/messages/files/5/a%20b.png" target="_blank" rel="noopener"><img src="/messages/files/5/a%20b.png" alt="a b.png" loading="lazy" width="40" height="30"></a></li></ul>`,
				`<ul class="attachment-files" aria-label="Files"><li><a href="/messages/files/6/notes.txt">notes.txt</a> <span class="hint">2 KB</span></li></ul>`,
			},
			notWant: []string{`"/files/`, "message-body"},
		},
		{
			name: "with a notice", d: views.MessageData{Message: message(nil), HTML: "<p>hello</p>", Notice: "You are not allowed to do that"},
			want: []string{`<p class="form-error" role="alert">You are not allowed to do that</p>`},
		},
		{
			name: "no text, only files, shows no body", d: views.MessageData{Message: message(func(m *app.Message) {
				m.Body = ""
				m.Attachments = []app.AttachmentView{{ID: 6, FileName: "n.txt", ContentType: "text/plain", Size: 1}}
			})},
			want:    []string{`href="/messages/files/6/n.txt"`},
			notWant: []string{"message-body"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireHTML(t, renderHTML(t, views.MessageContent(tt.d)), tt.want, tt.notWant)
		})
	}
}

func TestMessageItem(t *testing.T) {
	tests := []struct {
		name    string
		d       views.MessageData
		want    []string
		notWant []string
	}{
		{
			name: "an own message in a direct conversation has the place for Seen", d: views.MessageData{Message: message(nil), HTML: "<p>x</p>", Own: true, Direct: true},
			want:    []string{`<li id="msg-12" class="message own" data-message="12" data-author="alice">`, `<div id="msg-seen-12" class="message-seen-slot"></div>`},
			notWant: []string{"Seen<"},
		},
		{
			name: "and shows it when it is the one", d: views.MessageData{Message: message(nil), HTML: "<p>x</p>", Own: true, Direct: true, Seen: true},
			want: []string{`<div id="msg-seen-12" class="message-seen-slot"><p class="message-seen">Seen</p></div>`},
		},
		{
			name: "a message of someone else has no place for it", d: views.MessageData{Message: message(nil), HTML: "<p>x</p>", Direct: true},
			want: []string{`<li id="msg-12" class="message" `}, notWant: []string{"msg-seen-"},
		},
		{
			name: "nor does a group", d: views.MessageData{Message: message(nil), HTML: "<p>x</p>", Own: true},
			want: []string{`class="message own"`}, notWant: []string{"msg-seen-"},
		},
		{
			name: "nor a deleted message", d: views.MessageData{Message: message(func(m *app.Message) { m.Deleted = true }), Own: true, Direct: true},
			want: []string{`class="message own message-deleted"`}, notWant: []string{"msg-seen-"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireHTML(t, renderHTML(t, views.MessageItem(tt.d)), tt.want, tt.notWant)
		})
	}
}

func TestMessageReactions(t *testing.T) {
	chips := []app.ReactionCount{
		{Emoji: "\U0001F44D", Count: 2, Mine: true, Names: []string{"Alice", "Bob"}},
		{Emoji: "\u2764\uFE0F", Count: 1, Names: []string{"Bob"}},
	}
	tests := []struct {
		name    string
		d       views.MessageReactionsData
		want    []string
		notWant []string
	}{
		{
			name: "chips a person may click", d: views.MessageReactionsData{Message: message(func(m *app.Message) { m.Reactions = chips })},
			want: []string{
				`<div id="msg-reactions-12" class="reactions"><form class="reaction-chips" method="post" action="/messages/m/12/react" data-on:submit="@post(&#39;/messages/m/12/react&#39;, {contentType: &#39;form&#39;})">`,
				`<button type="submit" name="emoji" value="👍" class="reaction-chip mine" title="Alice, Bob" aria-label="👍 2, you reacted" aria-pressed="true">`,
				`class="reaction-chip" title="Bob" aria-label="❤️ 1" aria-pressed="false"`,
			},
		},
		{
			name: "no chips for a message that cannot be reacted to", d: views.MessageReactionsData{Message: message(func(m *app.Message) { m.Reactions, m.CanReact = chips, false })},
			want:    []string{`<ul class="reaction-chips" aria-label="Reactions">`, `<li class="reaction-chip mine" title="Alice, Bob">`},
			notWant: []string{"<button", "<form"},
		},
		{
			name: "nothing yet", d: views.MessageReactionsData{Message: message(nil)},
			want: []string{`<div id="msg-reactions-12" class="reactions"></div>`},
		},
		{
			name: "a notice", d: views.MessageReactionsData{Message: message(nil), Notice: "You are not allowed to do that"},
			want: []string{`<p class="form-error" role="alert">You are not allowed to do that</p>`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireHTML(t, renderHTML(t, views.MessageReactions(tt.d)), tt.want, tt.notWant)
		})
	}
}

func TestMessageReactionPicker(t *testing.T) {
	t.Run("the picker", func(t *testing.T) {
		html := renderHTML(t, views.MessageReactionPicker(views.MessageReactionsData{Message: message(nil)}))
		requireHTML(t, html, []string{
			`<div id="msg-reaction-picker-12" class="reaction-picker-box"><details class="reaction-add">`,
			`<form class="reaction-grid" method="post" action="/messages/m/12/react"`, `name="emoji" value="👍" class="reaction-option" aria-label="React with 👍"`,
			`<form class="reaction-custom" method="post" action="/messages/m/12/react"`, `id="msg-reaction-text-12"`, `maxlength="32"`,
		}, []string{"open", "aria-invalid", "field-error"})
	})
	t.Run("a problem opens it and keeps the text", func(t *testing.T) {
		html := renderHTML(t, views.MessageReactionPicker(views.MessageReactionsData{Message: message(nil), Problem: "Use one emoji.", Typed: "abc"}))
		requireHTML(t, html, []string{
			`<details class="reaction-add" open>`, `value="abc"`, `aria-invalid="true"`, `aria-describedby="msg-reaction-text-12-error"`,
			`<p class="field-error" id="msg-reaction-text-12-error" role="alert">Use one emoji.</p>`,
		}, nil)
	})
	t.Run("empty when the person cannot react", func(t *testing.T) {
		html := renderHTML(t, views.MessageReactionPicker(views.MessageReactionsData{Message: message(func(m *app.Message) { m.CanReact = false })}))
		if html != `<div id="msg-reaction-picker-12" class="reaction-picker-box"></div>` {
			t.Errorf("html = %s", html)
		}
	})
}

func TestMessageEdit(t *testing.T) {
	d := views.MessageEditData{Message: views.MessageData{Message: message(nil)}, Body: "a <draft>"}
	requireHTML(t, renderHTML(t, views.MessageEdit(d)), []string{
		`<div id="msg-content-12" class="message-content message-editing"><form class="message-edit-form" method="post" action="/messages/m/12/edit"`,
		`<textarea id="msg-edit-body-12" name="body" rows="3">a &lt;draft&gt;</textarea>`, `<button type="submit">Save</button>`,
		`href="/messages/4#msg-12" data-on:click__prevent="@get(&#39;/messages/m/12/view&#39;)">Cancel</a>`,
	}, []string{"aria-invalid", "field-error"})
	d.Errors = views.FormErrors{Form: "Something went wrong.", Fields: map[string]string{"body": "Write something."}}
	requireHTML(t, renderHTML(t, views.MessageEdit(d)), []string{
		`<p class="form-error" role="alert">Something went wrong.</p>`, `aria-invalid="true"`, `aria-describedby="msg-edit-body-12-error"`,
		`<p class="field-error" id="msg-edit-body-12-error" role="alert">Write something.</p>`,
	}, nil)
}

func TestMessageComposer(t *testing.T) {
	t.Run("the form", func(t *testing.T) {
		html := renderHTML(t, views.MessageComposer(views.ComposerData{ConversationID: 4, CanSend: true}))
		requireHTML(t, html, []string{
			`<div id="message-composer" class="message-composer"><form id="message-composer-form" class="message-composer-form" method="post" enctype="multipart/form-data" action="/messages/4" data-on:submit="@post(&#39;/messages/4&#39;, {contentType: &#39;form&#39;})">`,
			`<div id="message-reply" class="message-reply"></div>`,
			`<textarea id="message-body" name="body" rows="2" placeholder="Write a message. Enter sends, Shift+Enter makes a new line." data-on:keydown="evt.key === &#39;Enter&#39; &amp;&amp; !evt.shiftKey &amp;&amp; !evt.isComposing &amp;&amp; (evt.preventDefault(), el.form.requestSubmit())"></textarea>`,
			`<input id="message-files" class="attach-input" name="files" type="file" multiple data-on:change="`, `<label class="attach-button" for="message-files"`, `<button type="submit">Send</button>`,
		}, []string{"disabled user", "reply_to", "aria-invalid"})
	})
	t.Run("a disabled counterpart", func(t *testing.T) {
		html := renderHTML(t, views.MessageComposer(views.ComposerData{ConversationID: 4}))
		requireHTML(t, html, []string{`<p id="message-composer-note" class="hint join-note">You can't send messages to a disabled user.</p>`}, []string{"<form", "<textarea"})
	})
	t.Run("problems keep the text and show the reply", func(t *testing.T) {
		q := app.MessageQuote{ID: 9, Author: bob, Preview: "an <earlier> one"}
		html := renderHTML(t, views.MessageComposerForm(views.ComposerData{
			ConversationID: 4, CanSend: true, Body: "kept <words>", Reply: &q,
			Errors: views.FormErrors{Form: "Try again.", Fields: map[string]string{"body": "Write something.", "attachments": "Too big.", "replyTo": "Choose a message."}},
		}))
		requireHTML(t, html, []string{
			`>kept &lt;words&gt;</textarea>`, `<p class="form-error" role="alert">Try again.</p>`,
			`<p class="field-error" id="message-body-error" role="alert">Write something.</p>`,
			`<p class="field-error" id="message-files-error" role="alert">Too big.</p>`,
			`<p class="field-error" role="alert">Choose a message.</p>`,
			`<input type="hidden" name="reply_to" value="9">`, `<span class="quote-label">Replying to</span> <span class="quote-author">Bob</span> <span class="quote-preview">an &lt;earlier&gt; one</span>`,
			`class="quote-cancel" href="/messages/4" title="Cancel the reply" aria-label="Cancel the reply" data-on:click__prevent="el.closest(&#39;#message-reply&#39;).replaceChildren()">×</a>`,
		}, nil)
	})
}

func TestConversationList(t *testing.T) {
	last := &app.LastMessage{ID: 5, Author: bob, Preview: "see you <soon>", CreatedAt: msgTime}
	items := []app.ConversationSummary{
		{ID: 4, Kind: app.ConversationDirect, DisplayName: "Bob", Members: []app.UserRef{alice, bob}, LastMessage: last, LastActivity: msgTime, Unread: true},
		{ID: 6, Kind: app.ConversationGroup, Name: "crew", DisplayName: "crew", Members: []app.UserRef{bob, alice, {ID: 4, Username: "carol", DisplayName: "Carol"}},
			LastMessage: &app.LastMessage{Author: alice, Preview: "mine"}, LastActivity: msgTime},
		{ID: 7, Kind: app.ConversationDirect, DisplayName: "Carol", Members: []app.UserRef{alice}, LastActivity: msgTime},
	}
	html := renderHTML(t, views.ConversationList(items, alice.ID))
	requireHTML(t, html, []string{
		`<div id="conversation-list" class="conversation-list"><ul class="conversation-items">`,
		`<li id="conversation-4" class="conversation unread" data-conversation="4" data-members="alice bob"><a class="conversation-link" href="/messages/4">`,
		`<span class="conversation-title">Bob</span>`, `<span class="conversation-unread">Unread</span>`,
		`<span class="conversation-preview">Bob: see you &lt;soon&gt;</span>`,
		`<time class="conversation-time" datetime="2026-10-27T09:30:00Z" title="2026-10-27T09:30:00Z">2026-10-27 09:30</time>`,
		`<li id="conversation-6" class="conversation" data-conversation="6" data-members="alice bob carol">`,
		`<span class="conversation-kind">Group of 3</span>`, `<span class="conversation-preview">You: mine</span>`,
		`<span class="conversation-preview">No messages yet</span>`,
	}, []string{"empty"})
	if got := strings.Count(html, `conversation-unread`); got != 1 {
		t.Errorf("%d unread marks, want 1", got)
	}
	requireHTML(t, renderHTML(t, views.ConversationList(nil, alice.ID)), []string{`<div id="conversation-list" class="conversation-list"><div class="empty-state">`, `<p class="empty" id="conversations-empty">No conversations yet.</p></div></div>`}, []string{"<ul"})
}

func TestNewConversationForm(t *testing.T) {
	requireHTML(t, renderHTML(t, views.NewConversationForm(views.NewConversationData{})), []string{
		`<form id="new-conversation-form" class="room-form new-conversation" method="post" action="/messages" data-on:submit="@post(&#39;/messages&#39;, {contentType: &#39;form&#39;})">`,
		`<input id="new-conversation-users" name="users" type="text" value=""`, `<input id="new-conversation-name" name="name" type="text" value=""`,
		`maxlength="80"`, `Usernames separated by spaces or commas.`, `<button type="submit">Start conversation</button>`,
	}, []string{"aria-invalid", "field-error"})
	html := renderHTML(t, views.NewConversationForm(views.NewConversationData{
		Users: "bob <x>", Name: "n", Errors: views.FormErrors{Form: "Try again.", Fields: map[string]string{"users": "No active user named x.", "name": "Too long."}},
	}))
	requireHTML(t, html, []string{
		`value="bob &lt;x&gt;"`, `value="n"`, `<p class="form-error" role="alert">Try again.</p>`,
		`<p class="field-error" id="new-conversation-users-error" role="alert">No active user named x.</p>`,
		`<p class="field-error" id="new-conversation-name-error" role="alert">Too long.</p>`,
		`aria-describedby="new-conversation-users-error"`, `aria-describedby="new-conversation-name-error"`,
	}, []string{"Usernames separated by spaces or commas."})
}

func TestConversationMembers(t *testing.T) {
	h := app.ConversationHeader{
		ID: 4, Kind: app.ConversationGroup, Name: "crew", DisplayName: "crew", IsGroup: true, CanSend: true,
		Members: []app.ConversationMemberView{{User: alice}, {User: bob, Disabled: true}},
	}
	html := renderHTML(t, views.ConversationMembers(views.MembersPanelData{Header: h, ViewerID: alice.ID}))
	requireHTML(t, html, []string{
		`<section id="conversation-members" class="members-panel conversation-members">`,
		`<li id="conversation-member-2" data-username="alice"><span class="avatar avatar-sm avatar-c2" aria-hidden="true">A</span><span class="member-name">Alice</span>`, `<span class="member-username">@bob</span>`,
		`<span class="hint member-added">disabled</span>`,
		`<form id="conversation-add-form" class="add-member" method="post" action="/messages/4/members"`, `name="users"`,
		`<form id="conversation-rename-form" class="add-member" method="post" action="/messages/4/rename"`, `name="name" type="text" value="crew"`,
		`<form id="conversation-leave-form" class="action" method="post" action="/messages/4/leave"`, `Leave this group?`,
	}, []string{"aria-invalid", "form-error"})
	// A failed rename keeps the typed name; a failed add shows its problem.
	html = renderHTML(t, views.ConversationMembers(views.MembersPanelData{
		Header: h, ViewerID: alice.ID, AddUsers: "x", RenameTo: "typed", RenameOpen: true, Message: "Not now.",
		AddErrors:    views.FormErrors{Fields: map[string]string{"users": "No active user named x."}},
		RenameErrors: views.FormErrors{Fields: map[string]string{"name": "Too long."}},
	}))
	requireHTML(t, html, []string{
		`value="typed"`, `value="x"`, `<p class="form-error" role="alert">Not now.</p>`,
		`<p class="field-error" id="conversation-add-users-error" role="alert">No active user named x.</p>`,
		`<p class="field-error" id="conversation-rename-name-error" role="alert">Too long.</p>`,
	}, []string{`value="crew"`})
}

func TestConversationPageHeaderAndPane(t *testing.T) {
	h := app.ConversationHeader{
		ID: 4, Kind: app.ConversationDirect, DisplayName: "Bob", CanSend: true,
		Members: []app.ConversationMemberView{{User: alice}, {User: bob, Disabled: true}},
	}
	d := views.ConversationData{
		Sidebar: views.SidebarData{User: app.User{DisplayName: "Alice", Role: app.RoleMember}}, ViewerID: alice.ID, Header: h,
		Messages: []views.MessageData{{Message: message(nil), HTML: "<p>hello</p>"}, {Message: message(func(m *app.Message) { m.ID = 13 }), HTML: "<p>two</p>"}},
		HasOlder: true, NextBefore: 12, Composer: views.ComposerData{ConversationID: 4, CanSend: true},
	}
	html := renderHTML(t, views.ConversationPage(d))
	requireHTML(t, html, []string{
		"<title id=\"page-title\">Bob · Clowder</title>", `<h1>Bob</h1>`, `With Bob (disabled)`,
		`@get(&#39;/messages/4/stream?seen=13&#39;`,
		`<div id="message-pane" class="message-pane" data-init="el.scrollTop = el.scrollHeight">`,
		`<div id="messages-older" class="messages-older" data-on-intersect__once="@get(&#39;/messages/4/older?before=12&#39;)"><a href="/messages/4?before=12">Older messages</a></div>`,
		`<script src="/static/messages.js" defer></script>`,
	}, []string{`id="conversation-members"`, "Back to the newest"})
	if strings.Index(html, `id="msg-12"`) > strings.Index(html, `id="msg-13"`) {
		t.Error("the messages are not oldest first")
	}

	// The editor takes the place of the message being edited.
	edit := views.MessageEditData{Message: d.Messages[1], Body: "two"}
	d.Edit = &edit
	d.Before, d.HasOlder = 20, false
	html = renderHTML(t, views.ConversationPage(d))
	requireHTML(t, html, []string{
		`<li id="msg-13" class="message" data-message="13" data-author="alice"><div id="msg-content-13" class="message-content message-editing">`,
		`<p class="hint feed-newest"><a href="/messages/4">Back to the newest messages</a></p>`, `stream?seen=13&amp;before=20`,
		`<div id="messages-older" class="messages-older"></div>`,
	}, nil)
	if strings.Count(html, `id="msg-content-13"`) != 1 {
		t.Error("the message is shown twice")
	}
}

func TestSeenMessageID(t *testing.T) {
	direct := func(aliceRead, bobRead uint64) app.ConversationHeader {
		return app.ConversationHeader{Kind: app.ConversationDirect, Members: []app.ConversationMemberView{
			{User: alice, LastReadMessageID: aliceRead}, {User: bob, LastReadMessageID: bobRead},
		}}
	}
	msg := func(id uint64, by app.UserRef, deleted bool) app.Message {
		return app.Message{ID: id, Author: by, Deleted: deleted}
	}
	messages := []app.Message{msg(1, alice, false), msg(2, bob, false), msg(3, alice, false), msg(4, bob, false), msg(5, alice, true), msg(6, alice, false)}
	tests := []struct {
		name   string
		viewer uint64
		h      app.ConversationHeader
		want   uint64
	}{
		{"the other has read nothing", alice.ID, direct(6, 0), 0},
		{"read up to the first", alice.ID, direct(6, 1), 1},
		{"read past the second: still the first own", alice.ID, direct(6, 2), 1},
		{"read up to the third", alice.ID, direct(6, 3), 3},
		{"a deleted message does not take the mark", alice.ID, direct(6, 5), 3},
		{"read everything", alice.ID, direct(6, 6), 6},
		{"seen from the other side", bob.ID, direct(4, 0), 4},
		{"seen from the other side, a message earlier", bob.ID, direct(3, 0), 2},
		{"the viewer's own reading is not it", bob.ID, direct(0, 6), 0},
		{"a group has none", alice.ID, app.ConversationHeader{Kind: app.ConversationGroup, Members: direct(6, 6).Members}, 0},
		{"no messages", alice.ID, direct(6, 6), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := messages
			if tt.name == "no messages" {
				ms = nil
			}
			if got := views.SeenMessageID(tt.viewer, tt.h, ms); got != tt.want {
				t.Errorf("SeenMessageID = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMessageSeen(t *testing.T) {
	requireHTML(t, renderHTML(t, views.MessageSeen(12, true)), []string{`<div id="msg-seen-12" class="message-seen-slot"><p class="message-seen">Seen</p></div>`}, nil)
	requireHTML(t, renderHTML(t, views.MessageSeen(12, false)), []string{`<div id="msg-seen-12" class="message-seen-slot"></div>`}, []string{"Seen"})
}

func TestMessagePaths(t *testing.T) {
	tests := []struct{ name, got, want string }{
		{"conversation", views.ConversationPath(4), "/messages/4"},
		{"older", views.ConversationOlderPath(4, 9), "/messages/4/older?before=9"},
		{"before", views.ConversationBeforePath(4, 9), "/messages/4?before=9"},
		{"reply", views.ConversationReplyPath(4, 9), "/messages/4?reply=9#message-composer"},
		{"stream", views.ConversationStreamPath(4, 9, 0), "/messages/4/stream?seen=9"},
		{"stream of an older page", views.ConversationStreamPath(4, 9, 12), "/messages/4/stream?seen=9&before=12"},
		{"action", views.MessageActionPath(7, "react"), "/messages/m/7/react"},
		{"file", views.MessageFilePath(app.AttachmentView{ID: 5, FileName: "a b/c.png"}), "/messages/files/5/a%20b%2Fc.png"},
		{"element", views.MessageElementID(7), "msg-7"},
		{"content", views.MessageContentID(7), "msg-content-7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestMembersPanelHasMessageButtons(t *testing.T) {
	d := views.RoomData{
		Room:    app.Room{ID: 3, IsMember: true},
		Viewer:  app.User{ID: 2, Role: app.RoleMember},
		Members: []app.Member{{UserID: 2, Username: "alice", DisplayName: "Alice"}, {UserID: 3, Username: "bob", DisplayName: "Bob"}},
	}
	html := renderHTML(t, views.MembersPanel(d))
	requireHTML(t, html, []string{
		`<form class="action message-form" method="post" action="/messages" data-on:submit="@post(&#39;/messages&#39;, {contentType: &#39;form&#39;})"><input type="hidden" name="users" value="bob"> <input type="hidden" name="from" value="room-3"> <button type="submit" class="secondary" title="Send Bob a message">Message</button></form>`,
	}, []string{`value="alice"`})
	if got := strings.Count(html, ">Message</button>"); got != 1 {
		t.Errorf("%d Message buttons, want 1 (not for the viewer)", got)
	}
}

// TestConversationListRowsHaveTheOthersAvatars: a row of the list shows the
// avatar of the other person of a direct conversation (large) and up to three
// of a group (small), never the viewer's own, in the avatar span the CSS lays
// out beside the title (21.3).
func TestConversationListRowsHaveTheOthersAvatars(t *testing.T) {
	carol := app.UserRef{ID: 4, Username: "carol", DisplayName: "Carol"}
	dave := app.UserRef{ID: 5, Username: "dave", DisplayName: "Dave"}
	erin := app.UserRef{ID: 6, Username: "erin", DisplayName: "Erin"}
	items := []app.ConversationSummary{
		{ID: 4, Kind: app.ConversationDirect, DisplayName: "Bob", Members: []app.UserRef{alice, bob}, LastActivity: msgTime},
		{ID: 6, Kind: app.ConversationGroup, DisplayName: "crew", Members: []app.UserRef{alice, bob, carol, dave, erin}, LastActivity: msgTime},
	}
	html := renderHTML(t, views.ConversationList(items, alice.ID))
	direct := html[strings.Index(html, `id="conversation-4"`):strings.Index(html, `id="conversation-6"`)]
	group := html[strings.Index(html, `id="conversation-6"`):]
	tests := []struct {
		name    string
		html    string
		want    []string
		notWant []string
		avatars int
	}{
		{"a direct conversation", direct, []string{`<span class="conversation-avatars" aria-hidden="true">`, `avatar avatar-xl`, ">B<"}, []string{">A<", "avatar-md"}, 1},
		{"a group", group, []string{`avatar avatar-md`, ">B<", ">C<", ">D<"}, []string{">A<", ">E<", "avatar-xl"}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireHTML(t, tt.html, tt.want, tt.notWant)
			if got := strings.Count(tt.html, `class="avatar avatar-`); got != tt.avatars {
				t.Errorf("%d avatars, want %d", got, tt.avatars)
			}
		})
	}
}

// TestConversationHeaderAndActions: the header names the people with their
// avatars and presence dots and has no button of its own (the server patches it
// when the group changes); the "more" menu with the notification preferences is
// a separate component, drawn only for a viewer who has preferences here.
func TestConversationHeaderAndActions(t *testing.T) {
	h := app.ConversationHeader{
		ID: 4, Kind: app.ConversationDirect, DisplayName: "Bob", CanSend: true,
		Members: []app.ConversationMemberView{{User: alice}, {User: bob}},
	}
	online := views.ConversationData{Header: h, ViewerID: alice.ID, Members: views.MembersPanelData{Online: map[uint64]bool{bob.ID: true}}}
	offline := views.ConversationData{Header: h, ViewerID: alice.ID}
	tests := []struct {
		name    string
		html    string
		want    []string
		notWant []string
	}{
		{"the other person is online", renderHTML(t, views.ConversationHeader(online)), []string{
			`<header id="conversation-header" class="conversation-header">`, `<a href="/messages">All conversations</a>`,
			`<span class="conversation-person" title="Bob">`, `class="online-dot"`, `<h1>Bob</h1>`, `<p class="hint conversation-members-line">With Bob</p>`,
		}, []string{`title="Alice"`, "place-prefs-open", "<details"}},
		{"the other person is not", renderHTML(t, views.ConversationHeader(offline)), []string{`<span class="conversation-person" title="Bob">`}, []string{"online-dot"}},
		{"the menu", renderHTML(t, views.ConversationActions(views.PlacePrefsData{Action: "/messages/4/notifications"})), []string{
			`<div class="conversation-actions">`, `<details class="post-menu conversation-menu" data-preserve-attr="open" data-on:click__outside="el.removeAttribute('open')">`,
			`aria-label="More actions"`, `id="place-prefs-open"`, `el.closest(&#39;details&#39;)?.removeAttribute(&#39;open&#39;)`,
		}, nil},
		{"no preferences here", renderHTML(t, views.ConversationActions(views.PlacePrefsData{})), []string{`<div class="conversation-actions"></div>`}, []string{"<details", "place-prefs-open"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireHTML(t, tt.html, tt.want, tt.notWant)
		})
	}
}
