package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

func TestSendAMessage(t *testing.T) {
	t.Parallel()
	t.Run("a Datastar request gets the message at the end and a fresh composer", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		first := e.say("bob", conv, "Question?")
		rec := e.post("alice", convPath(conv), url.Values{"body": {"  Answer <b>one</b>  "}, "reply_to": {idText(first.ID)}}, true)
		requireSSE(t, rec)
		got := e.history("alice", conv)
		if len(got) != 2 || got[1].Body != "Answer <b>one</b>" || got[1].ReplyTo == nil || got[1].ReplyTo.ID != first.ID {
			t.Fatalf("messages = %+v", got)
		}
		id := idText(got[1].ID)
		requireBody(t, rec, []string{
			"selector #message-list", "mode append", `<li id="msg-` + id + `" class="message own"`,
			`Answer &lt;b&gt;one&lt;/b&gt;`, `data-quoted="` + idText(first.ID) + `"`,
			// The copies after the first are removed, as the feed does it.
			"selector #msg-" + id + " ~ #msg-" + id, "mode remove",
			// The composer is replaced, empty.
			"selector #message-composer-form", "mode replace", `<textarea id="message-body" name="body" rows="2"`,
		}, `Answer <b>`, `name="reply_to"`, `aria-invalid`)
		// The send marks the conversation read for the sender, so alice has none.
		if got := e.unreadConversations("alice"); got != 0 {
			t.Errorf("alice's unread = %d, want 0", got)
		}
		if got := e.unreadConversations("bob"); got != 1 {
			t.Errorf("bob's unread = %d, want 1", got)
		}
	})

	t.Run("a plain post is sent back to the conversation", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		rec := e.post("alice", convPath(conv), url.Values{"body": {"hello"}}, false)
		requireRedirectTo(t, rec, false, convPath(conv))
		if got := e.history("bob", conv); len(got) != 1 || got[0].Body != "hello" {
			t.Errorf("messages = %+v", got)
		}
	})

	t.Run("a group message reaches the members", func(t *testing.T) {
		e := newRoomsEnv(t)
		crew := e.group("crew", "alice", "bob", "carol")
		e.post("bob", convPath(crew), url.Values{"body": {"to all"}}, true)
		for _, who := range []string{"alice", "carol"} {
			if got := e.history(who, crew); len(got) != 1 || got[0].Author.Username != "bob" {
				t.Errorf("%s sees %+v", who, got)
			}
		}
	})
}

func TestSendProblemsKeepWhatWasTyped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		form func(e *roomsEnv, conv uint64) url.Values
		// want is shown, next to its field.
		want []string
	}{
		{"nothing to say", func(*roomsEnv, uint64) url.Values { return url.Values{"body": {"   "}} },
			[]string{"Write something.", `id="message-body-error"`, `aria-invalid="true"`}},
		{"too long", func(*roomsEnv, uint64) url.Values { return url.Values{"body": {strings.Repeat("é", 4001)}} },
			[]string{"Use at most 4,000 characters.", `id="message-body-error"`}},
		{"a reply to a message that is gone", func(e *roomsEnv, conv uint64) url.Values {
			gone := e.say("bob", conv, "gone soon")
			if err := e.msgs.Delete(e.t.Context(), e.actor(e.bob), gone.ID); err != nil {
				e.t.Fatal(err)
			}
			return url.Values{"body": {"kept words"}, "reply_to": {idText(gone.ID)}}
		}, []string{"Choose a message of this conversation to reply to.", "kept words"}},
		{"a reply to a message of another conversation", func(e *roomsEnv, conv uint64) url.Values {
			other := e.say("alice", e.direct("alice", "carol"), "elsewhere")
			return url.Values{"body": {"kept words"}, "reply_to": {idText(other.ID)}}
		}, []string{"Choose a message of this conversation to reply to.", "kept words"}},
		{"a reply that is not a number", func(*roomsEnv, uint64) url.Values {
			return url.Values{"body": {"kept words"}, "reply_to": {"x"}}
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				conv := e.direct("alice", "bob")
				form := tt.form(e, conv)
				before := len(e.history("alice", conv))
				rec := e.post("alice", convPath(conv), form, datastar)
				if tt.want == nil {
					// A reply that is no ID is no reply: the message is sent.
					if len(e.history("alice", conv)) != before+1 {
						t.Errorf("the message was not sent:\n%s", rec.Body)
					}
					return
				}
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, append([]string{`id="message-composer-form"`}, tt.want...), "selector #message-list")
				} else {
					if rec.Code != http.StatusUnprocessableEntity {
						t.Fatalf("status = %d, want 422; body:\n%s", rec.Code, rec.Body)
					}
					requireBody(t, rec, append([]string{`id="message-composer-form"`, "<h1>Bob</h1>", `<ol id="message-list"`}, tt.want...))
				}
				if got := len(e.history("alice", conv)); got != before {
					t.Errorf("%d messages, want %d: a failed send wrote one", got, before)
				}
			})
		})
	}

	t.Run("the quote of the reply stays", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		target := e.say("bob", conv, "quoted words")
		rec := e.post("alice", convPath(conv), url.Values{"body": {"   "}, "reply_to": {idText(target.ID)}}, true)
		requireBody(t, rec, []string{"Write something.", `name="reply_to" value="` + idText(target.ID) + `"`, "quoted words"})
	})
}

func TestSendWithFiles(t *testing.T) {
	t.Parallel()
	img := pngOf(t, 40, 30)
	t.Run("files and a message", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			conv := e.direct("alice", "bob")
			rec := e.postFiles("alice", convPath(conv), url.Values{"body": {"look"}}, []upload{
				{filename: "pic.png", data: img}, {filename: "a.txt", data: []byte("text")},
			}, datastar)
			got := e.history("bob", conv)
			if len(got) != 1 || len(got[0].Attachments) != 2 || got[0].Attachments[0].FileName != "pic.png" || !got[0].Attachments[0].IsImage {
				t.Fatalf("messages = %+v", got)
			}
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{`src="/messages/files/` + idText(got[0].Attachments[0].ID) + `/pic.png"`, `>a.txt</a>`})
			} else {
				requireRedirectTo(t, rec, false, convPath(conv))
			}
			if on := e.onDisk(); len(on) != 2 {
				t.Errorf("%d files on disk, want 2", len(on))
			}
		})
	})

	t.Run("files alone make a message, and the empty part of a browser is ignored", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		body, ct := multipartBody(t, url.Values{"body": {""}}, []upload{{filename: "", data: nil}, {filename: "only.txt", data: []byte("x")}})
		rec := e.sendMultipart("alice", convPath(conv), body, ct, true)
		requireSSE(t, rec)
		if got := e.history("bob", conv); len(got) != 1 || got[0].Body != "" || len(got[0].Attachments) != 1 {
			t.Errorf("messages = %+v", got)
		}
	})

	t.Run("a file that is too large", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnvWith(t, smallFiles)
			conv := e.direct("alice", "bob")
			rec := e.postFiles("alice", convPath(conv), url.Values{"body": {"my careful text"}}, []upload{
				{filename: "ok.txt", data: []byte("fine")}, {filename: "big.bin", data: make([]byte, 5000)},
			}, datastar)
			want := []string{"Each file can be at most 1 KB.", `id="message-files-error"`, "my careful text"}
			if datastar {
				requireSSE(t, rec)
			} else if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", rec.Code)
			}
			requireBody(t, rec, want)
			if got := e.history("alice", conv); len(got) != 0 {
				t.Errorf("a message was sent: %+v", got)
			}
			if on := e.onDisk(); len(on) != 0 {
				t.Errorf("files left on disk: %v", on)
			}
			if tmp := e.tmpFiles(); len(tmp) != 0 {
				t.Errorf("temporary files left: %v", tmp)
			}
		})
	})

	t.Run("too many files", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnvWith(t, smallFiles)
			conv := e.direct("alice", "bob")
			rec := e.postFiles("alice", convPath(conv), url.Values{"body": {"kept words"}}, []upload{
				{filename: "1.txt", data: []byte("1")}, {filename: "2.txt", data: []byte("2")}, {filename: "3.txt", data: []byte("3")},
			}, datastar)
			if datastar {
				requireSSE(t, rec)
			} else if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			requireBody(t, rec, []string{"Attach at most 2 files.", "kept words"})
			if on := e.onDisk(); len(on) != 0 {
				t.Errorf("files left on disk: %v", on)
			}
		})
	})

	t.Run("a refused send leaves no files", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		rec := e.postFiles("carol", convPath(conv), url.Values{"body": {"x"}}, []upload{{filename: "a.txt", data: []byte("a")}}, false)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if on := e.onDisk(); len(on) != 0 {
			t.Errorf("files left on disk: %v", on)
		}
	})

	t.Run("a form that cannot be read", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		rec := e.sendMultipart("alice", convPath(conv), strings.NewReader("not multipart at all"), "multipart/form-data; boundary=xyz", true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}

func TestEditAMessage(t *testing.T) {
	t.Parallel()
	t.Run("the editor replaces the content, and saving shows it again", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		msg := e.say("alice", conv, "first <draft>")
		id := idText(msg.ID)

		rec := e.send(get(msgPath(msg.ID, "edit"), e.cookies["alice"], asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="msg-content-` + id + `" class="message-content message-editing"`, `first &lt;draft&gt;</textarea>`, "Save", "Cancel"})

		rec = e.post("alice", msgPath(msg.ID, "edit"), url.Values{"body": {"  second <b>draft</b>  "}}, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="msg-content-` + id + `" class="message-content"`, `second &lt;b&gt;draft&lt;/b&gt;`, `class="message-edited"`, ">edited</span>"},
			"message-editing", "<textarea")
		if got := e.history("bob", conv); got[0].Body != "second <b>draft</b>" || got[0].EditedAt == nil {
			t.Errorf("message = %+v", got[0])
		}

		// Cancel shows the content as it is.
		rec = e.send(get(msgPath(msg.ID, "view"), e.cookies["alice"], asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="msg-content-` + id + `" class="message-content"`, "second &lt;b&gt;"}, "<textarea")
	})

	t.Run("a plain post goes back to the message", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		msg := e.say("alice", conv, "before")
		rec := e.post("alice", msgPath(msg.ID, "edit"), url.Values{"body": {"after"}}, false)
		requireRedirectTo(t, rec, false, convPath(conv)+"#msg-"+idText(msg.ID))
		requireRedirectTo(t, e.send(get(msgPath(msg.ID, "view"), e.cookies["alice"])), false, convPath(conv)+"#msg-"+idText(msg.ID))
		if got := e.history("bob", conv); got[0].Body != "after" {
			t.Errorf("body = %q", got[0].Body)
		}
	})

	t.Run("a mistake shows its problem in the editor and keeps the text", func(t *testing.T) {
		tests := []struct {
			name string
			body string
			want string
		}{
			{"empty", "  ", "Write something."},
			{"too long", strings.Repeat("x", 4001), "Use at most 4,000 characters."},
		}
		for _, tt := range tests {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				conv := e.direct("alice", "bob")
				msg := e.say("alice", conv, "original")
				rec := e.post("alice", msgPath(msg.ID, "edit"), url.Values{"body": {tt.body}}, datastar)
				want := []string{tt.want, `aria-invalid="true"`, `id="msg-edit-body-` + idText(msg.ID) + `-error"`, "message-editing"}
				if datastar {
					requireSSE(t, rec)
				} else {
					if rec.Code != http.StatusUnprocessableEntity {
						t.Fatalf("status = %d, want 422", rec.Code)
					}
					want = append(want, "<h1>Bob</h1>", `<ol id="message-list"`)
				}
				requireBody(t, rec, want)
				if got := e.history("alice", conv); got[0].Body != "original" || got[0].EditedAt != nil {
					t.Errorf("message changed: %+v", got[0])
				}
			})
		}
	})

	t.Run("only the author, and not a deleted message", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		msg := e.say("alice", conv, "mine")
		gone := e.say("alice", conv, "gone")
		if err := e.msgs.Delete(e.t.Context(), e.actor(e.alice), gone.ID); err != nil {
			t.Fatal(err)
		}
		for _, datastar := range []bool{false, true} {
			opts := []reqOpt{e.cookies["bob"]}
			if datastar {
				opts = append(opts, asDatastar())
			}
			// The form, and the save: bob is a member but not the author.
			for _, rec := range []*httptest.ResponseRecorder{
				e.send(get(msgPath(msg.ID, "edit"), opts...)),
				e.send(postForm(msgPath(msg.ID, "edit"), url.Values{"body": {"hijacked"}}, opts...)),
			} {
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{"You are not allowed to do that", `id="msg-content-` + idText(msg.ID) + `"`, "mine"}, "hijacked", "message-editing")
				} else {
					if rec.Code != http.StatusForbidden {
						t.Fatalf("status = %d, want 403; body:\n%s", rec.Code, rec.Body)
					}
					requireBody(t, rec, []string{"You are not allowed to do that"}, "hijacked", "message-editing")
				}
			}
			// The author: a deleted message is gone.
			authorOpts := []reqOpt{e.cookies["alice"]}
			if datastar {
				authorOpts = append(authorOpts, asDatastar())
			}
			rec := e.send(get(msgPath(gone.ID, "edit"), authorOpts...))
			if datastar {
				requireRedirectTo(t, rec, true, "/messages")
			} else if rec.Code != http.StatusNotFound {
				t.Errorf("deleted message: status = %d, want 404", rec.Code)
			}
		}
		if got := e.history("alice", conv); got[0].Body != "mine" || got[0].EditedAt != nil {
			t.Errorf("message changed: %+v", got[0])
		}
	})
}

func TestDeleteAMessage(t *testing.T) {
	t.Parallel()
	t.Run("the placeholder stays, in both modes", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			conv := e.direct("alice", "bob")
			msg := e.sayIn("alice", conv, app.MessageInput{Body: "regret", Uploads: []app.Upload{e.uploadMessage("alice", "f.txt")}})
			if _, err := e.msgs.React(e.t.Context(), e.actor(e.bob), msg.ID, "\U0001F44D"); err != nil {
				t.Fatal(err)
			}
			rec := e.post("alice", msgPath(msg.ID, "delete"), nil, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{`id="msg-content-` + idText(msg.ID) + `"`, "message deleted"}, "regret", "f.txt", "reaction-bar", "message-edit-link")
			} else {
				requireRedirectTo(t, rec, false, convPath(conv)+"#msg-"+idText(msg.ID))
			}
			got := e.history("bob", conv)
			if len(got) != 1 || !got[0].Deleted || got[0].Body != "" || len(got[0].Attachments) != 0 || len(got[0].Reactions) != 0 {
				t.Errorf("message = %+v", got)
			}
			if on := e.onDisk(); len(on) != 0 {
				t.Errorf("files left on disk: %v", on)
			}
			// The page shows the placeholder in its place.
			page := e.send(get(convPath(conv), e.cookies["bob"]))
			requireBody(t, page, []string{`<li id="msg-` + idText(msg.ID) + `" class="message message-deleted"`, "message deleted"}, "regret")
			// Deleting it again is the 404 page.
			again := e.post("alice", msgPath(msg.ID, "delete"), nil, false)
			if again.Code != http.StatusNotFound {
				t.Errorf("deleting again: status = %d, want 404", again.Code)
			}
		})
	})

	t.Run("only the author", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			conv := e.direct("alice", "bob")
			msg := e.say("alice", conv, "mine")
			rec := e.post("bob", msgPath(msg.ID, "delete"), nil, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{"You are not allowed to do that", "mine"})
			} else {
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403", rec.Code)
				}
				requireBody(t, rec, []string{"You are not allowed to do that"})
			}
			if got := e.history("alice", conv); got[0].Deleted {
				t.Error("the message was deleted")
			}
		})
	})
}

func TestReactToAMessage(t *testing.T) {
	t.Parallel()
	t.Run("a reaction puts the chip and the picker, and again takes it off", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			conv := e.direct("alice", "bob")
			msg := e.say("alice", conv, "hi")
			id := idText(msg.ID)
			rec := e.post("bob", msgPath(msg.ID, "react"), url.Values{"emoji": {"\U0001F44D"}}, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{
					`<div id="msg-reactions-` + id + `" class="reactions">`, `class="reaction-chip mine"`, `aria-pressed="true"`,
					`<div id="msg-reaction-picker-` + id + `" class="reaction-picker-box">`,
				}, "form-error")
			} else {
				requireRedirectTo(t, rec, false, convPath(conv)+"#msg-"+id)
			}
			if got := e.history("alice", conv)[0].Reactions; len(got) != 1 || got[0].Count != 1 {
				t.Fatalf("reactions = %+v", got)
			}
			// Typed, the same emoji again removes it.
			e.post("bob", msgPath(msg.ID, "react"), url.Values{"emoji": {" \U0001F44D "}}, datastar)
			if got := e.history("alice", conv)[0].Reactions; len(got) != 0 {
				t.Errorf("reactions = %+v, want none", got)
			}
		})
	})

	t.Run("text that is not an emoji is shown in the picker", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		msg := e.say("alice", conv, "hi")
		id := idText(msg.ID)
		rec := e.post("bob", msgPath(msg.ID, "react"), url.Values{"emoji": {"abc"}}, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{`<div id="msg-reaction-picker-` + id + `"`, `<details class="reaction-add" open>`, `value="abc"`, `id="msg-reaction-text-` + id + `-error"`}, `id="msg-reactions-`)
		rec = e.post("bob", msgPath(msg.ID, "react"), url.Values{"emoji": {"abc"}}, false)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("plain: status = %d, want 422", rec.Code)
		}
		requireBody(t, rec, []string{`<ol id="message-list"`})
		if got := e.history("alice", conv)[0].Reactions; len(got) != 0 {
			t.Errorf("reactions = %+v", got)
		}
	})

	t.Run("a deleted message takes none", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		msg := e.say("alice", conv, "hi")
		if err := e.msgs.Delete(e.t.Context(), e.actor(e.alice), msg.ID); err != nil {
			t.Fatal(err)
		}
		rec := e.post("bob", msgPath(msg.ID, "react"), url.Values{"emoji": {"\U0001F44D"}}, false)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestGroupActions(t *testing.T) {
	t.Parallel()
	t.Run("add people by username", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			crew := e.group("crew", "alice", "bob", "carol")
			e.say("alice", crew, "old words")
			admin := e.admin
			rec := e.post("bob", convPath(crew)+"/members", url.Values{"users": {"@Admin, carol"}}, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{`id="conversation-header"`, "With Alice, Carol, Admin", `id="conversation-members"`, `data-username="admin"`})
			} else {
				requireRedirectTo(t, rec, false, convPath(crew))
			}
			// carol was a member already; the admin sees the whole history.
			got, err := e.msgs.History(e.t.Context(), e.actor(admin), crew, 0, 0)
			if err != nil || len(got.Messages) != 1 || got.Messages[0].Body != "old words" {
				t.Errorf("history of the new member = %+v, %v", got, err)
			}
			h, err := e.msgs.Conversation(e.t.Context(), e.actor(e.bob), crew)
			if err != nil || len(h.Members) != 4 {
				t.Errorf("members = %+v, %v", h.Members, err)
			}
		})
	})

	t.Run("mistakes of the add form", func(t *testing.T) {
		tests := []struct {
			name  string
			users string
			want  string
			setup func(e *roomsEnv)
		}{
			{"nobody", " ", "Name at least one person.", nil},
			{"unknown", "nobody", "No active user named nobody.", nil},
			{"disabled", "carol", "No active user named carol.", func(e *roomsEnv) { e.disable("carol") }},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				modes(t, func(t *testing.T, datastar bool) {
					e := newRoomsEnv(t)
					crew := e.group("crew", "alice", "bob", "admin")
					if tt.setup != nil {
						tt.setup(e)
					}
					rec := e.post("alice", convPath(crew)+"/members", url.Values{"users": {tt.users}}, datastar)
					want := []string{tt.want, `id="conversation-add-form"`, `aria-invalid="true"`, `value="` + tt.users + `"`}
					if datastar {
						requireSSE(t, rec)
						requireBody(t, rec, want)
					} else {
						if rec.Code != http.StatusUnprocessableEntity {
							t.Fatalf("status = %d, want 422", rec.Code)
						}
						requireBody(t, rec, append(want, "<h1>crew</h1>", `<ol id="message-list"`))
					}
				})
			})
		}
	})

	t.Run("people are not added to a conversation of two", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		rec := e.post("alice", convPath(conv)+"/members", url.Values{"users": {"carol"}}, false)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		requireBody(t, rec, []string{"People cannot be added to a conversation of two."})
		h, _ := e.msgs.Conversation(e.t.Context(), e.actor(e.alice), conv)
		if len(h.Members) != 2 {
			t.Errorf("members = %+v", h.Members)
		}
	})

	t.Run("rename", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			crew := e.group("crew", "alice", "bob", "carol")
			rec := e.post("bob", convPath(crew)+"/rename", url.Values{"name": {"  Release crew "}}, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{"<h1>Release crew</h1>", `name="name" type="text" value="Release crew"`})
			} else {
				requireRedirectTo(t, rec, false, convPath(crew))
			}
			if h, _ := e.msgs.Conversation(e.t.Context(), e.actor(e.carol), crew); h.Name != "Release crew" {
				t.Errorf("name = %q", h.Name)
			}
			// A name that is too long keeps the text in the field.
			long := strings.Repeat("n", 81)
			rec = e.post("bob", convPath(crew)+"/rename", url.Values{"name": {long}}, datastar)
			want := []string{"Use at most 80 characters.", `value="` + long + `"`, `id="conversation-rename-form"`}
			if datastar {
				requireSSE(t, rec)
			} else if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			requireBody(t, rec, want)
			if h, _ := e.msgs.Conversation(e.t.Context(), e.actor(e.carol), crew); h.Name != "Release crew" {
				t.Errorf("name = %q after a refused rename", h.Name)
			}
		})
	})

	t.Run("a direct conversation is not renamed", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		rec := e.post("alice", convPath(conv)+"/rename", url.Values{"name": {"x"}}, false)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
	})

	t.Run("leave goes back to the list", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			crew := e.group("crew", "alice", "bob", "carol")
			rec := e.post("carol", convPath(crew)+"/leave", nil, datastar)
			requireRedirectTo(t, rec, datastar, "/messages")
			if _, err := e.msgs.Conversation(e.t.Context(), e.actor(e.carol), crew); err == nil {
				t.Error("carol still reads the group")
			}
			// She no longer reads the page.
			if got := e.send(get(convPath(crew), e.cookies["carol"])); got.Code != http.StatusNotFound {
				t.Errorf("page after leaving: status = %d, want 404", got.Code)
			}
			// The others do.
			if got := e.send(get(convPath(crew), e.cookies["bob"])); got.Code != http.StatusOK {
				t.Errorf("bob's page: status = %d", got.Code)
			}
		})
	})

	t.Run("nobody leaves a direct conversation", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			conv := e.direct("alice", "bob")
			rec := e.post("alice", convPath(conv)+"/leave", nil, datastar)
			if datastar {
				requireSSE(t, rec)
			} else if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			requireBody(t, rec, []string{"You cannot leave a conversation of two."})
			if _, err := e.msgs.Conversation(e.t.Context(), e.actor(e.alice), conv); err != nil {
				t.Errorf("alice lost the conversation: %+v", err)
			}
		})
	})
}

func TestMessageFileDownloads(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	img := pngOf(t, 4, 3)
	pic, err := e.msgs.Upload(e.t.Context(), e.actor(e.alice), "pic.png", strings.NewReader(string(img)))
	if err != nil {
		t.Fatal(err)
	}
	doc := e.uploadMessage("alice", "weird \"name\".txt")
	html := e.uploadMessage("alice", "page.html")
	msg := e.sayIn("alice", conv, app.MessageInput{Body: "files", Uploads: []app.Upload{pic, doc, html}})
	pathOf := func(i int) string {
		return "/messages/files/" + idText(msg.Attachments[i].ID) + "/" + url.PathEscape(msg.Attachments[i].FileName)
	}

	t.Run("an image is inline with its own type", func(t *testing.T) {
		rec := e.send(get(pathOf(0), e.cookies["bob"]))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		h := rec.Header()
		if h.Get("Content-Type") != "image/png" || h.Get("Content-Disposition") != `inline; filename="pic.png"` ||
			h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != downloadCSP ||
			h.Get("Cache-Control") != downloadCacheControl {
			t.Errorf("headers = %v", h)
		}
		if rec.Body.Len() != len(img) {
			t.Errorf("body is %d bytes, want %d", rec.Body.Len(), len(img))
		}
	})
	t.Run("everything else is a download", func(t *testing.T) {
		for i, name := range []string{"", "weird", "page.html"} {
			if i == 0 {
				continue
			}
			rec := e.send(get(pathOf(i), e.cookies["alice"]))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d", name, rec.Code)
			}
			h := rec.Header()
			if h.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment; filename=") ||
				h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != downloadCSP {
				t.Errorf("%s: headers = %v", name, h)
			}
		}
		if got := e.send(get(pathOf(1), e.cookies["bob"])).Header().Get("Content-Disposition"); !strings.Contains(got, `filename="weird _name_.txt"; filename*=UTF-8''weird%20%22name%22.txt`) {
			t.Errorf("Content-Disposition = %q", got)
		}
	})
	t.Run("a range is served", func(t *testing.T) {
		req := get(pathOf(1), e.cookies["bob"])
		req.Header.Set("Range", "bytes=0-3")
		rec := e.send(req)
		if rec.Code != http.StatusPartialContent || rec.Body.String() != "cont" {
			t.Errorf("status = %d, body = %q, want 206 and cont", rec.Code, rec.Body)
		}
	})
	t.Run("a deleted message has no files", func(t *testing.T) {
		if err := e.msgs.Delete(e.t.Context(), e.actor(e.alice), msg.ID); err != nil {
			t.Fatal(err)
		}
		if rec := e.send(get(pathOf(0), e.cookies["bob"])); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestMessageFilesAreNotPostFiles(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	msg := e.sayIn("alice", conv, app.MessageInput{Uploads: []app.Upload{e.uploadMessage("alice", "f.txt")}})
	// An attachment of a message is not downloaded through the posts' path.
	rec := e.send(get("/files/"+idText(msg.Attachments[0].ID)+"/f.txt", e.cookies["alice"]))
	if rec.Code == http.StatusOK {
		t.Errorf("the posts' path served a file of a message")
	}
}
