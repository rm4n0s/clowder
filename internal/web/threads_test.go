package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// commentOn writes a text comment of the user under the post or comment through
// the service and fails the test on an error.
func (e *roomsEnv) commentOn(parent app.Post, user, body string) app.Post {
	e.t.Helper()
	c, err := e.posts.Reply(e.t.Context(), e.actor(e.users(user)), parent.ID,
		app.PostInput{Format: app.FormatText, Body: body})
	if err != nil {
		e.t.Fatalf("Reply %q: %+v", body, err)
	}
	return c
}

// demoThread is a thread in "general" like the one of the demo:
//
//	root (alice)
//	  first (bob)
//	    second (alice)
//	      third (admin)
//	  gone (alice)      deleted, kept for its reply
//	    after (bob)
type demoThread struct {
	root, first, second, third, gone, after app.Post
}

func (e *roomsEnv) demoThread() demoThread {
	e.t.Helper()
	var d demoThread
	d.root = e.writePost("alice", "general", "The root post", "threaded")
	d.first = e.commentOn(d.root, "bob", "first comment")
	d.second = e.commentOn(d.first, "alice", "second comment")
	d.third = e.commentOn(d.second, "admin", "third comment")
	d.gone = e.commentOn(d.root, "alice", "this will be deleted")
	d.after = e.commentOn(d.gone, "bob", "reply to the deleted one")
	if err := e.posts.Delete(e.t.Context(), e.actor(e.alice), d.gone.ID); err != nil {
		e.t.Fatalf("Delete: %+v", err)
	}
	return d
}

// article returns the HTML of the article of the post in the page.
func article(t *testing.T, body string, p app.Post) string {
	t.Helper()
	start := strings.Index(body, `<article id="post-`+idText(p.ID)+`"`)
	if start < 0 {
		t.Fatalf("no article for post %d in:\n%s", p.ID, body)
	}
	card := body[start:]
	return card[:strings.Index(card, "</article>")]
}

// requireOrder fails unless the strings are in the body in this order.
func requireOrder(t *testing.T, body string, parts ...string) {
	t.Helper()
	last := -1
	for _, part := range parts {
		i := strings.Index(body, part)
		if i < 0 {
			t.Errorf("body does not contain %q", part)
			return
		}
		if i < last {
			t.Errorf("%q is out of order", part)
		}
		last = i
	}
}

func TestPostPageShowsTheThread(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	d := e.demoThread()
	rec := e.getAs("bob", postPathOf(d.root, ""), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	// The root, then the comments depth first.
	requireOrder(t, body,
		`id="post-`+idText(d.root.ID)+`"`, `id="comments"`,
		`id="post-`+idText(d.first.ID)+`"`, `id="post-`+idText(d.second.ID)+`"`, `id="post-`+idText(d.third.ID)+`"`,
		`id="post-`+idText(d.gone.ID)+`"`, `id="post-`+idText(d.after.ID)+`"`)
	requireBody(t, rec, []string{
		`id="thread"`, "The root post", "first comment", "second comment", "third comment", "reply to the deleted one", "4 comments",
	}, "No comments yet", "Join this room to reply")
	for _, tt := range []struct {
		post  app.Post
		class string
	}{
		{d.first, `class="post comment depth-1"`}, {d.second, `class="post comment depth-2"`}, {d.third, `class="post comment depth-3"`},
		{d.gone, `class="post comment depth-1 post-deleted"`}, {d.after, `class="post comment depth-2"`},
	} {
		card := article(t, body, tt.post)
		if !strings.Contains(card, tt.class) {
			t.Errorf("article of %d does not have %q:\n%s", tt.post.ID, tt.class, card)
		}
	}
	// No comment has a "replying to" line below the cap.
	if strings.Contains(body, "comment-replying") {
		t.Error("a comment above the capped depth says whom it replies to")
	}
	// The root is not a comment; it keeps its count link.
	root := article(t, body, d.root)
	requireContains(t, root, []string{`class="post"`, `href="/posts/` + idText(d.root.ID) + `#comments"`}, []string{"comment depth"})
	// The deleted comment shows the placeholder only; its reply stays below.
	gone := article(t, body, d.gone)
	requireContains(t, gone, []string{"This comment was deleted.", "post-deleted"},
		[]string{"Alice", "@alice", "this will be deleted", "post-edit", "/delete", "post-reply", "<time"})
	// Bob's own comment has Reply, Edit and Delete; alice's has Reply only.
	bobs := article(t, body, d.first)
	requireContains(t, bobs, []string{"Bob", "@bob", `class="post-reply" href="/posts/` + idText(d.first.ID) + `/reply"`,
		`class="post-edit" href="/posts/` + idText(d.first.ID) + `/edit?from=post"`, `action="/posts/` + idText(d.first.ID) + `/delete"`,
		`id="reply-slot-` + idText(d.first.ID) + `"`, `href="/posts/` + idText(d.root.ID) + `#post-` + idText(d.first.ID) + `"`}, []string{"post-comments"})
	alices := article(t, body, d.second)
	requireContains(t, alices, []string{"post-reply"}, []string{"post-edit", "/delete"})
}

func TestPostPageCapsTheDepth(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	root := e.writePost("alice", "general", "deep root")
	chain := make([]app.Post, 0, 9)
	parent := root
	for i := 1; i <= 9; i++ {
		user := "bob"
		if i%2 == 0 {
			user = "alice"
		}
		parent = e.commentOn(parent, user, "level "+strconv.Itoa(i))
		chain = append(chain, parent)
	}
	// The parent of the ninth is deleted but kept for its reply.
	if err := e.posts.Delete(t.Context(), e.actor(e.alice), chain[7].ID); err != nil {
		t.Fatal(err)
	}
	body := e.getAs("bob", postPathOf(root, ""), false).Body.String()
	for i, c := range chain {
		requireContains(t, article(t, body, c), []string{"depth-" + strconv.Itoa(min(i+1, 6))}, nil)
	}
	if strings.Contains(body, "depth-7") || strings.Contains(body, "depth-8") {
		t.Error("a comment is indented past the cap")
	}
	// Levels 1 to 6 do not say whom they reply to; 7 and 9 do (8 is deleted).
	for i, c := range chain {
		card := article(t, body, c)
		switch i + 1 {
		case 7:
			// Its parent, level 6, is by alice.
			requireContains(t, card, []string{`<p class="comment-replying">replying to @alice</p>`}, nil)
		case 8:
			requireContains(t, card, []string{"This comment was deleted."}, []string{"comment-replying", "@alice", "@bob"})
		case 9:
			requireContains(t, card, []string{`<p class="comment-replying">replying to a deleted comment</p>`}, nil)
		default:
			requireContains(t, card, nil, []string{"comment-replying"})
		}
	}
}

func TestCommentPageShowsTheThreadOfItsPost(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	d := e.demoThread()
	rec := e.getAs("alice", postPathOf(d.second, ""), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	requireBody(t, rec, []string{
		"<title id=\"page-title\">(1) Post by Alice in general · Clowder</title>", "The root post", "first comment", "second comment", "third comment",
		"reply to the deleted one", `id="comments"`,
	})
	requireOrder(t, body, `id="post-`+idText(d.root.ID)+`"`, `id="post-`+idText(d.second.ID)+`"`)
	// Only the asked comment is in focus, and scrolled into view.
	if n := strings.Count(body, "post-focus"); n != 1 {
		t.Errorf("%d items in focus, want 1", n)
	}
	focus := article(t, body, d.second)
	requireContains(t, focus, []string{"post-focus", "data-init=", "scrollIntoView"}, nil)
	for _, p := range []app.Post{d.root, d.first, d.third} {
		requireContains(t, article(t, body, p), nil, []string{"post-focus", "data-init"})
	}
	// The post itself has no focus.
	requireContains(t, e.getAs("alice", postPathOf(d.root, ""), false).Body.String(), nil, []string{"post-focus"})
}

func TestPostReplyForm(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	d := e.demoThread()
	slot := `id="reply-slot-` + idText(d.second.ID) + `"`
	t.Run("the editor opens in the slot under the comment", func(t *testing.T) {
		rec := e.getAs("bob", postPathOf(d.second, "reply"), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			slot, `class="reply-slot"`, `id="post-editor-reply-` + idText(d.second.ID) + `"`, `action="/posts/` + idText(d.second.ID) + `/replies"`,
			`name="format" value="markdown" checked`, `>Reply</button>`, `>Cancel</a>`, ".reply-slot", `id="post-preview-reply-` + idText(d.second.ID) + `"`,
		}, "data-init", "<article")
	})
	t.Run("without JavaScript the thread opens with the editor", func(t *testing.T) {
		rec := e.getAs("bob", postPathOf(d.second, "reply"), false)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		requireBody(t, rec, []string{`id="sidebar-rooms"`, `id="post-editor-reply-` + idText(d.second.ID) + `"`, "third comment", "The root post"})
		if n := strings.Count(body, `<form id="post-editor-reply-`); n != 1 {
			t.Errorf("%d reply editors, want 1", n)
		}
		requireContains(t, article(t, body, d.second), []string{`id="post-editor-reply-` + idText(d.second.ID) + `"`}, nil)
	})
	t.Run("the root has a reply slot too", func(t *testing.T) {
		rec := e.getAs("bob", postPathOf(d.root, "reply"), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="reply-slot-` + idText(d.root.ID) + `"`, `action="/posts/` + idText(d.root.ID) + `/replies"`})
	})
	t.Run("a deleted comment cannot be replied to", func(t *testing.T) {
		for _, datastar := range []bool{true, false} {
			rec := e.getAs("bob", postPathOf(d.gone, "reply"), datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{redirectScript(postPathOf(d.gone, ""))})
			} else if rec.Code != http.StatusNotFound {
				t.Errorf("plain status = %d, want 404", rec.Code)
			}
		}
	})
}

func TestPostReply(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		d := e.demoThread()
		rec := e.post("bob", postPathOf(d.second, "replies"), editorForm("markdown", "A **fresh** reply", "mine"), datastar)
		thread, err := e.posts.Thread(t.Context(), e.actor(e.bob), d.root.ID)
		if err != nil {
			t.Fatal(err)
		}
		var fresh app.Comment
		var at int
		for i, c := range thread.Comments {
			if c.Body == "A **fresh** reply" {
				fresh, at = c, i
			}
		}
		if fresh.ID == 0 || fresh.ParentID != d.second.ID || fresh.Depth != 3 || fresh.Author.Username != "bob" || strings.Join(fresh.Tags, ",") != "mine" {
			t.Fatalf("stored reply = %+v", fresh)
		}
		// It sits right after its parent's subtree: after third (also depth 3), before gone.
		if thread.Comments[at-1].ID != d.third.ID {
			t.Errorf("the reply is after %d, want after the third comment", thread.Comments[at-1].ID)
		}
		if !datastar {
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/posts/"+idText(d.root.ID)+"#post-"+idText(fresh.ID) {
				t.Fatalf("answer = %d %q, want 303 to the comment in its thread", rec.Code, rec.Header().Get("Location"))
			}
			return
		}
		requireSSE(t, rec)
		body := rec.Body.String()
		requireBody(t, rec, []string{
			`id="thread"`, "<strong>fresh</strong>", `id="post-` + idText(fresh.ID) + `"`, "depth-3", "5 comments",
			// The root and the other comments are there too.
			"The root post", "first comment", "third comment", `href="/search?q=%23mine"`,
		}, "<textarea", "post-editor-reply")
		requireOrder(t, body, `id="post-`+idText(d.third.ID)+`"`, `id="post-`+idText(fresh.ID)+`"`, `id="post-`+idText(d.gone.ID)+`"`)
	})
}

func TestPostReplyToThePost(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	d := e.demoThread()
	rec := e.post("alice", postPathOf(d.root, "replies"), editorForm("text", "top level", ""), true)
	requireSSE(t, rec)
	requireBody(t, rec, []string{"top level", "depth-1", "5 comments"})
	thread, err := e.posts.Thread(t.Context(), e.actor(e.alice), d.root.ID)
	if err != nil || len(thread.Comments) != 6 {
		t.Fatalf("thread = %d comments, %v", len(thread.Comments), err)
	}
}

func TestPostReplyMistakes(t *testing.T) {
	t.Parallel()
	tags11 := "t1 t2 t3 t4 t5 t6 t7 t8 t9 t10 t11"
	tests := []struct {
		name string
		form url.Values
		want []string
	}{
		{"empty body", editorForm("text", " ", "keep-me"), []string{"Write something.", `id="post-body-reply-3-error"`, `value="keep-me"`, `value="text" checked`, `aria-invalid="true"`}},
		{"too many tags", editorForm("markdown", "Some **body**", tags11), []string{"At most 10 tags.", `id="post-tags-reply-3-error"`, ">Some **body**</textarea>"}},
		{"no format", url.Values{"body": {"Some body"}}, []string{"Choose text or markdown.", `id="post-format-reply-3-error"`, ">Some body</textarea>"}},
	}
	for _, tt := range tests {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			d := e.demoThread()
			editor := `id="post-editor-reply-` + idText(d.second.ID) + `"`
			rec := e.post("bob", postPathOf(d.second, "replies"), tt.form, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, append([]string{editor}, tt.want...), `id="thread"`)
			} else {
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status = %d, want 422", rec.Code)
				}
				body := rec.Body.String()
				// The whole page: the sidebar, the thread and the editor under the comment.
				requireBody(t, rec, append([]string{`id="sidebar-rooms"`, `id="thread"`, "third comment"}, tt.want...))
				requireContains(t, article(t, body, d.second), []string{editor}, nil)
			}
			thread, err := e.posts.Thread(t.Context(), e.actor(e.bob), d.root.ID)
			if err != nil || len(thread.Comments) != 5 {
				t.Errorf("thread after a refused reply = %d comments, %v", len(thread.Comments), err)
			}
		})
	}
}

func TestPostReplyByANonMember(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	d := e.demoThread()
	t.Run("the page has no Reply buttons and says to join", func(t *testing.T) {
		rec := e.getAs("carol", postPathOf(d.root, ""), false)
		requireBody(t, rec, []string{"The root post", "first comment", `id="join-to-reply"`, "Join this room to reply"},
			"post-reply", "reply-slot", "<textarea")
		// A member of the room has the buttons and no note.
		rec = e.getAs("alice", postPathOf(d.root, ""), false)
		requireBody(t, rec, []string{"post-reply", "reply-slot"}, "Join this room to reply")
	})
	t.Run("the reply is refused", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			rec := e.post("carol", postPathOf(d.second, "replies"), editorForm("text", "let me in", ""), datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{"You are not allowed to do that", `id="post-` + idText(d.second.ID) + `"`}, "let me in", "<textarea")
			} else {
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403", rec.Code)
				}
				requireBody(t, rec, []string{"You are not allowed to do that", "Join this room to reply"}, "<textarea")
			}
			thread, err := e.posts.Thread(t.Context(), e.actor(e.alice), d.root.ID)
			if err != nil || len(thread.Comments) != 5 {
				t.Errorf("thread after a refused reply = %d comments, %v", len(thread.Comments), err)
			}
		})
	})
	t.Run("the editor does not open", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			rec := e.getAs("carol", postPathOf(d.second, "reply"), datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{"You are not allowed to do that"}, "<textarea")
			} else if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
		})
	})
}

func TestDeleteComments(t *testing.T) {
	t.Parallel()
	post := func(e *roomsEnv, user string, p app.Post, datastar bool) *httptest.ResponseRecorder {
		return e.post(user, postPathOf(p, "delete"), url.Values{"from": {"post"}}, datastar)
	}
	t.Run("a comment with replies leaves a placeholder in the thread", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			d := e.demoThread()
			rec := post(e, "bob", d.first, datastar)
			if datastar {
				requireSSE(t, rec)
				body := rec.Body.String()
				requireBody(t, rec, []string{`id="thread"`, "This comment was deleted.", "second comment", "third comment", "The root post"}, "first comment", "data: mode remove")
				requireContains(t, article(t, body, d.first), []string{"post-deleted"}, []string{"Bob", "@bob"})
				// The deleted comment is counted no more.
				requireBody(t, rec, []string{"3 comments"}, "4 comments")
			} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/posts/"+idText(d.root.ID) {
				t.Fatalf("answer = %d %q, want 303 to the thread", rec.Code, rec.Header().Get("Location"))
			}
			got, err := e.posts.Get(t.Context(), e.actor(e.alice), d.first.ID)
			if err != nil || !got.Deleted {
				t.Errorf("comment after the delete = %+v, %v", got, err)
			}
		})
	})
	t.Run("the last reply under a placeholder takes it away", func(t *testing.T) {
		e := newRoomsEnv(t)
		d := e.demoThread()
		rec := post(e, "bob", d.after, true)
		requireSSE(t, rec)
		body := rec.Body.String()
		requireBody(t, rec, []string{`id="thread"`, "first comment", "third comment"}, "reply to the deleted one", "This comment was deleted.", "data: mode remove")
		if strings.Contains(body, `id="post-`+idText(d.gone.ID)+`"`) || strings.Contains(body, `id="post-`+idText(d.after.ID)+`"`) {
			t.Error("the placeholder or its reply is still in the thread")
		}
		if _, err := e.posts.Get(t.Context(), e.actor(e.alice), d.gone.ID); err == nil {
			t.Error("the placeholder is still stored")
		}
	})
	t.Run("a comment without replies disappears", func(t *testing.T) {
		e := newRoomsEnv(t)
		d := e.demoThread()
		rec := post(e, "admin", d.third, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{"second comment", "3 comments"}, "third comment")
		// The post page itself is gone only with its last placeholder.
		root := e.getAs("alice", postPathOf(d.root, ""), false)
		requireBody(t, root, []string{"The root post"}, "third comment")
	})
	t.Run("the last reply under a deleted post takes the post away", func(t *testing.T) {
		e := newRoomsEnv(t)
		root := e.writePost("alice", "general", "going soon")
		reply := e.commentOn(root, "bob", "the only reply")
		if err := e.posts.Delete(t.Context(), e.actor(e.alice), root.ID); err != nil {
			t.Fatal(err)
		}
		rec := post(e, "bob", reply, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{redirectScript("/rooms/" + idText(e.general.ID))}, `id="thread"`)
	})
	t.Run("who may delete a comment", func(t *testing.T) {
		tests := []struct {
			user string
			of   func(demoThread) app.Post
			ok   bool
		}{
			{"admin", func(d demoThread) app.Post { return d.third }, true},  // the author
			{"admin", func(d demoThread) app.Post { return d.first }, true},  // an admin deletes any
			{"bob", func(d demoThread) app.Post { return d.third }, false},   // another member
			{"alice", func(d demoThread) app.Post { return d.third }, false}, // another member
			{"carol", func(d demoThread) app.Post { return d.third }, false}, // a reader who has not joined
		}
		for _, tt := range tests {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				d := e.demoThread()
				target := tt.of(d)
				rec := post(e, tt.user, target, datastar)
				got, err := e.posts.Get(t.Context(), e.actor(e.alice), target.ID)
				removed := err != nil || got.Deleted
				if removed != tt.ok {
					t.Errorf("%s: comment removed = %v, want %v", tt.user, removed, tt.ok)
				}
				if tt.ok {
					return
				}
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{"You are not allowed to do that", `id="post-` + idText(target.ID) + `"`, "third comment"}, `id="thread"`)
				} else if rec.Code != http.StatusForbidden {
					t.Errorf("%s: status = %d, want 403", tt.user, rec.Code)
				}
			})
		}
	})
}

func TestEditComments(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	d := e.demoThread()
	card := `id="post-` + idText(d.second.ID) + `"`
	t.Run("the editor replaces the comment and keeps its indentation", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(d.second, "edit")+"?from=post", true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			card, `class="post post-editing comment depth-2"`, `id="post-editor-edit-` + idText(d.second.ID) + `"`, ">second comment</textarea>",
			`name="from" value="post"`, `/posts/` + idText(d.second.ID) + `/view?from=post`,
		})
		// Even without "from" a comment knows it is on the post page.
		rec = e.getAs("alice", postPathOf(d.second, "edit"), true)
		requireBody(t, rec, []string{`name="from" value="post"`, `depth-2`})
	})
	t.Run("without JavaScript the thread opens with the editor on the comment", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(d.second, "edit"), false)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		requireBody(t, rec, []string{`id="thread"`, "The root post", "third comment"})
		requireContains(t, article(t, body, d.second), []string{"post-editing", ">second comment</textarea>"}, nil)
		requireContains(t, article(t, body, d.root), nil, []string{"post-editing", "<textarea"})
	})
	t.Run("Save patches the comment back as a comment", func(t *testing.T) {
		rec := e.post("alice", postPathOf(d.second, "edit"), url.Values{"format": {"markdown"}, "body": {"Changed *comment*"}, "tags": {""}, "from": {"post"}}, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{card, `class="post comment depth-2"`, "<em>comment</em>", `class="post-edited"`, `class="post-reply"`,
			`href="/posts/` + idText(d.root.ID) + `#post-` + idText(d.second.ID) + `"`}, "<textarea", "post-comments")
	})
	t.Run("a plain save goes to the comment in its thread", func(t *testing.T) {
		rec := e.post("alice", postPathOf(d.second, "edit"), editorForm("text", "Plain change", ""), false)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/posts/"+idText(d.root.ID)+"#post-"+idText(d.second.ID) {
			t.Fatalf("answer = %d %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("a mistake keeps the editor indented", func(t *testing.T) {
		rec := e.post("alice", postPathOf(d.second, "edit"), editorForm("text", "", "kept"), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{"Write something.", `class="post post-editing comment depth-2"`, `value="kept"`})
		rec = e.post("alice", postPathOf(d.second, "edit"), editorForm("text", "", "kept"), false)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("plain status = %d, want 422", rec.Code)
		}
		requireBody(t, rec, []string{`id="thread"`, "Write something.", "third comment"})
	})
	t.Run("Cancel restores the comment", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(d.second, "view")+"?from=post", true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{card, `class="post comment depth-2"`, "Plain change"}, "post-editing", "<textarea")
	})
	t.Run("a capped comment keeps its replying-to line", func(t *testing.T) {
		parent := d.root
		var last app.Post
		for i := 1; i <= 8; i++ {
			user := "bob"
			if i%2 == 0 {
				user = "alice"
			}
			last = e.commentOn(parent, user, "deep")
			parent = last
		}
		// The eighth comment is alice's; the seventh is bob's.
		rec := e.post("alice", postPathOf(last, "edit"), editorForm("text", "deep, edited", ""), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{"depth-6", "replying to @bob", "deep, edited"})
	})
	t.Run("a deleted comment cannot be edited", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(d.gone, "edit"), false)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestFeedLinksToTheThread(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	d := e.demoThread()
	rec := e.getAs("bob", "/rooms/"+idText(e.general.ID), false)
	// The feed shows posts only, with the count of the comments, which links to the thread.
	requireBody(t, rec, []string{`<a class="post-comments" href="/posts/` + idText(d.root.ID) + `#comments">4 comments</a>`}, "first comment", "depth-1", "post-reply")
}

func TestMentionsAreLinksOnThePages(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	link := `<a href="/search?q=from%3Abob" class="mention">@bob</a>`
	root := e.writePost("alice", "general", "hello @bob and @nobody")
	md := e.writeMarkdown("alice", "general", "hello **@bob**, `@bob`")
	c := e.commentOn(root, "admin", "ping @bob")

	feed := e.getAs("bob", "/rooms/"+idText(e.general.ID), false)
	requireBody(t, feed, []string{link, "<strong>" + link + "</strong>", "<code>@bob</code>", "@nobody"}, "from%3Anobody")

	page := e.getAs("bob", postPathOf(root, ""), false)
	requireBody(t, page, []string{link}, "from%3Anobody")
	if card := article(t, page.Body.String(), c); !strings.Contains(card, link) {
		t.Errorf("the comment does not link the mention:\n%s", card)
	}
	if card := article(t, feed.Body.String(), md); !strings.Contains(card, "<strong>"+link+"</strong>") {
		t.Errorf("the markdown post does not link the mention:\n%s", card)
	}

	// A search result shows the text, not a link.
	search := e.getAs("bob", "/search?q=hello", false)
	requireBody(t, search, []string{"@bob"}, "from%3Abob")
}
