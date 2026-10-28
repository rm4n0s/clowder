package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// messagesSearchPath is the path of the Messages page with a search.
func messagesSearchPath(q string, before uint64) string {
	v := url.Values{"q": {q}}
	if before != 0 {
		v.Set("before", strconv.FormatUint(before, 10))
	}
	return "/messages?" + v.Encode()
}

func TestMessagesSearchPage(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	direct := e.direct("alice", "bob")
	crew := e.group("Launch crew", "alice", "bob", "carol")
	m1 := e.say("bob", direct, `the <script>alert(1)</script> lighthouse plan`)
	m2 := e.say("alice", crew, "lighthouse crew call at noon")
	e.say("alice", crew, "unrelated words")
	carolOnly := e.direct("carol", "admin")
	e.say("carol", carolOnly, "lighthouse supper")

	t.Run("a search shows the results in place of the list", func(t *testing.T) {
		rec := e.send(get(messagesSearchPath("lighthouse", 0), e.cookies["alice"]))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		requireBody(t, rec, []string{
			`<h1>Messages</h1>`,
			`<form id="message-search" class="search-form message-search" role="search" method="get" action="/messages">`,
			`<input id="message-search-q" name="q" type="search" value="lighthouse"`,
			`<section id="message-search-results"`, `class="search-open" href="/messages/` + idText(crew) + `?msg=` + idText(m2.ID) + `#msg-` + idText(m2.ID) + `"`,
			`class="search-open" href="/messages/` + idText(direct) + `?msg=` + idText(m1.ID) + `#msg-` + idText(m1.ID) + `"`,
			`<a class="search-room" href="/messages/` + idText(crew) + `">Launch crew</a>`,
			`<a class="search-room" href="/messages/` + idText(direct) + `">Bob</a>`,
			`<mark>lighthouse</mark>`, `<span class="post-author">Bob</span>`,
			`<a class="search-clear" href="/messages">Clear</a>`,
			`@get(&#39;/me/stream&#39;`,
		}, `id="conversation-list"`, `id="new-conversation-form"`, "supper", "messages=1", "<script>alert", "No messages match.")
		// Newest first.
		if i, j := strings.Index(body, `id="message-result-`+idText(m2.ID)+`"`), strings.Index(body, `id="message-result-`+idText(m1.ID)+`"`); i < 0 || j < 0 || i > j {
			t.Errorf("the results are not newest first: %d, %d", i, j)
		}
		requireBody(t, rec, []string{`&lt;script&gt;alert(1)&lt;/script&gt;`})
	})

	t.Run("who finds what", func(t *testing.T) {
		tests := []struct {
			user       string
			want, deny []string
		}{
			{"bob", []string{"lighthouse plan", "lighthouse crew call"}, []string{"supper"}},
			{"carol", []string{"lighthouse crew call", "lighthouse supper"}, []string{"lighthouse plan"}},
			{"admin", []string{"lighthouse supper"}, []string{"lighthouse plan", "crew call"}},
		}
		for _, tt := range tests {
			rec := e.send(get(messagesSearchPath("lighthouse", 0), e.cookies[tt.user]))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d", tt.user, rec.Code)
			}
			body := rec.Body.String()
			for _, w := range tt.want {
				if !strings.Contains(strings.ReplaceAll(strings.ReplaceAll(body, "<mark>", ""), "</mark>", ""), w) {
					t.Errorf("%s: the page lacks %q", tt.user, w)
				}
			}
			for _, w := range tt.deny {
				if strings.Contains(strings.ReplaceAll(strings.ReplaceAll(body, "<mark>", ""), "</mark>", ""), w) {
					t.Errorf("%s: the page shows %q", tt.user, w)
				}
			}
		}
	})

	t.Run("nothing found", func(t *testing.T) {
		rec := e.send(get(messagesSearchPath("zzzz", 0), e.cookies["alice"]))
		requireBody(t, rec, []string{`<p class="empty">No messages match.</p>`, `class="search-clear" href="/messages"`, `value="zzzz"`},
			`class="search-results`)
	})

	t.Run("a member who left finds nothing of the conversation", func(t *testing.T) {
		e.leave("carol", crew)
		rec := e.send(get(messagesSearchPath("lighthouse", 0), e.cookies["carol"]))
		requireBody(t, rec, []string{"<mark>lighthouse</mark> supper"}, "crew call")
	})

	t.Run("queries that are refused", func(t *testing.T) {
		tests := []struct{ q, message string }{
			{"room:general", "Type a word to search the messages."},
			{"#plan", "Type a word to search the messages."},
			{"lighthouse #plan", "Filters such as #tag, room: and from: do not work in messages."},
			{"plan after:soon", "Use dates like 2026-10-27."},
		}
		for _, tt := range tests {
			rec := e.send(get(messagesSearchPath(tt.q, 0), e.cookies["alice"]))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("%q: status = %d, want 422", tt.q, rec.Code)
			}
			requireBody(t, rec, []string{`id="message-search-error" role="alert">` + tt.message}, `class="search-results`, "No messages match.")
		}
	})

	t.Run("a blank query is the list", func(t *testing.T) {
		for _, q := range []string{"", "%20%20"} {
			rec := e.send(get("/messages?q="+q, e.cookies["alice"]))
			requireBody(t, rec, []string{`id="conversation-list"`, `id="message-search"`, `messages=1`}, "message-search-results")
		}
	})

	t.Run("the list has the search box", func(t *testing.T) {
		rec := e.send(get("/messages", e.cookies["alice"]))
		requireBody(t, rec, []string{`<input id="message-search-q" name="q" type="search" value=""`, `id="conversation-list"`})
	})

	t.Run("not logged in", func(t *testing.T) {
		rec := e.send(get(messagesSearchPath("lighthouse", 0)))
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("answer = %d to %q, want a redirect to the login", rec.Code, rec.Header().Get("Location"))
		}
	})

	t.Run("a disabled user", func(t *testing.T) {
		e.disable("bob")
		rec := e.send(get(messagesSearchPath("lighthouse", 0), e.cookies["bob"]))
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("answer = %d to %q, want a redirect to the login", rec.Code, rec.Header().Get("Location"))
		}
	})
}

func TestMessagesSearchPaging(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	var ids []uint64
	for range 31 {
		ids = append(ids, e.say("bob", conv, "pager line").ID)
	}
	rec := e.send(get(messagesSearchPath("pager", 0), e.cookies["alice"]))
	body := rec.Body.String()
	if n := strings.Count(body, `<li class="search-result message-result"`); n != 30 {
		t.Fatalf("first page has %d results, want 30", n)
	}
	next := ids[1]
	requireBody(t, rec, []string{`<a class="older" href="/messages?before=` + idText(next) + `&amp;q=pager">Older results</a>`})
	if strings.Contains(body, `id="message-result-`+idText(ids[0])+`"`) {
		t.Error("the oldest message is on the first page")
	}

	rec = e.send(get(messagesSearchPath("pager", next), e.cookies["alice"]))
	if n := strings.Count(rec.Body.String(), `<li class="search-result message-result"`); n != 1 {
		t.Fatalf("second page has %d results, want 1", n)
	}
	requireBody(t, rec, []string{`id="message-result-` + idText(ids[0]) + `"`}, "Older results")
}

// TestConversationOpensAtAMessage checks the link of a search result: the
// conversation page shows the message and marks it, the newest page when the
// message is on it, else the page that ends with it.
func TestConversationOpensAtAMessage(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	old := e.say("bob", conv, "a very old thing")
	var last app.Message
	for range 60 {
		last = e.say("bob", conv, "filler")
	}
	recent := last

	t.Run("a message among the newest", func(t *testing.T) {
		rec := e.send(get(convPath(conv)+"?msg="+idText(recent.ID), e.cookies["alice"]))
		requireBody(t, rec, []string{
			`id="msg-` + idText(recent.ID) + `"`, `scrollIntoView`, `msg-` + idText(recent.ID) + `&#39;)`,
		}, "Back to the newest messages")
		if e.unreadConversations("alice") != 0 {
			t.Error("the newest page was not marked read")
		}
	})

	t.Run("an old message", func(t *testing.T) {
		e.say("bob", conv, "one more") // unread again
		rec := e.send(get(convPath(conv)+"?msg="+idText(old.ID), e.cookies["alice"]))
		requireBody(t, rec, []string{
			`id="msg-` + idText(old.ID) + `"`, `scrollIntoView`, "Back to the newest messages",
		}, `id="msg-`+idText(recent.ID)+`"`)
		if e.unreadConversations("alice") != 1 {
			t.Error("an older page marked the conversation read")
		}
	})

	t.Run("no message is the newest page, as before", func(t *testing.T) {
		rec := e.send(get(convPath(conv), e.cookies["alice"]))
		requireBody(t, rec, []string{`data-init="el.scrollTop = el.scrollHeight"`}, "scrollIntoView", "Back to the newest messages")
	})

	t.Run("a message of another conversation is ignored", func(t *testing.T) {
		other := e.direct("alice", "carol")
		foreign := e.say("carol", other, "elsewhere")
		rec := e.send(get(convPath(conv)+"?msg="+idText(foreign.ID), e.cookies["alice"]))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		requireBody(t, rec, nil, "scrollIntoView", "elsewhere")
	})
}
