package web

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// scope is a roomsEnv with the objects of two rooms and two conversations,
// for the tests of what a person may reach (docs/SECURITY.md, "Access").
type scope struct {
	*roomsEnv
	// secret room: a post with a file and a task, a comment.
	sPost, sComment app.Post
	sTask           app.TaskCard
	sColumn         uint64
	sFile           app.AttachmentView
	// general room: a post with a file and a task.
	gPost app.Post
	gTask app.TaskCard
	gCol  uint64
	gFile app.AttachmentView
	// conversations: alice and bob (direct), alice and carol (direct).
	conv, conv2 uint64
	msg, msg2   app.Message
	msgFile     app.AttachmentView
}

func newScope(t *testing.T) *scope {
	t.Helper()
	e := newRoomsEnv(t)
	sc := &scope{roomsEnv: e}
	attach := func(user, room, body, title string) app.Post {
		up, err := e.posts.Upload(t.Context(), e.actor(e.users(user)), "notes.txt", strings.NewReader("contents of "+body))
		if err != nil {
			t.Fatalf("Upload: %+v", err)
		}
		p, err := e.posts.Create(t.Context(), e.actor(e.users(user)), e.room(room).ID, app.PostInput{
			Format: app.FormatText, Body: body, Uploads: []app.Upload{up}, Tasks: []app.TaskInput{{Title: title}},
		})
		if err != nil {
			t.Fatalf("Create: %+v", err)
		}
		return p
	}
	sc.sPost = attach("alice", "secret", "secret plans", "secret task")
	sc.gPost = attach("alice", "general", "general plans", "general task")
	sc.sFile, sc.gFile = sc.sPost.Attachments[0], sc.gPost.Attachments[0]
	sc.sComment = e.commentOn(sc.sPost, "bob", "secret comment")
	sc.sTask, sc.gTask = e.cardTitled("secret", "secret task"), e.cardTitled("general", "general task")
	sc.sColumn = e.boardOf("alice", "secret").Columns[0].ID
	sc.gCol = e.boardOf("alice", "general").Columns[1].ID
	sc.conv, sc.conv2 = e.direct("alice", "bob"), e.direct("alice", "carol")
	up := e.uploadMessage("alice", "memo.txt")
	sc.msg = e.sayIn("alice", sc.conv, app.MessageInput{Body: "private words", Uploads: []app.Upload{up}})
	sc.msgFile = sc.msg.Attachments[0]
	sc.msg2 = e.say("alice", sc.conv2, "words for carol")
	return sc
}

// request is one request of the table of routes.
type request struct {
	method, path string
	form         url.Values
	// adminMay is true when an admin who is not a member may do it (SPEC D29:
	// admins rename and delete any room and remove any member).
	adminMay bool
}

// send runs the request as the user (plain, not by Datastar).
func (sc *scope) send(user string, rq request) *http.Request {
	if rq.method == http.MethodGet {
		return get(rq.path, sc.cookies[user])
	}
	return postForm(rq.path, rq.form, sc.cookies[user])
}

// TestPrivateDataIsNotFoundToNonMembers is the check of SPEC §7.2, §7.11 and D29
// over every route that takes the ID of something in a private room or in a
// private conversation: whoever is not a member, an admin included, gets the
// 404 page, and nothing changes.
func TestPrivateDataIsNotFoundToNonMembers(t *testing.T) {
	t.Parallel()
	sc := newScope(t)
	s, c, p := idText(sc.secret.ID), idText(sc.sColumn), postPathOf(sc.sPost, "")
	cm := postPathOf(sc.sComment, "")
	tk, v, m := idText(sc.sTask.ID), idText(sc.conv), idText(sc.msg.ID)
	form := func(kv ...string) url.Values {
		u := url.Values{}
		for i := 0; i < len(kv); i += 2 {
			u.Set(kv[i], kv[i+1])
		}
		return u
	}
	requests := []request{
		{http.MethodGet, "/rooms/" + s, nil, false},
		{http.MethodGet, "/rooms/" + s + "/feed", nil, false},
		{http.MethodGet, "/rooms/" + s + "/stream", nil, false},
		{http.MethodGet, "/rooms/" + s + "/board", nil, false},
		{http.MethodGet, "/rooms/" + s + "/board/columns", nil, false},
		{http.MethodGet, "/rooms/" + s + "/board/stream", nil, false},
		{http.MethodGet, "/rooms/" + s + "/board/tasks/" + tk + "/edit", nil, false},
		{http.MethodPost, "/rooms/" + s + "/join", nil, false},
		{http.MethodPost, "/rooms/" + s + "/leave", nil, false},
		{http.MethodPost, "/rooms/" + s + "/rename", form("name", "stolen"), true},
		{http.MethodPost, "/rooms/" + s + "/delete", nil, true},
		{http.MethodPost, "/rooms/" + s + "/members", form("user", idText(sc.carol.ID)), false},
		{http.MethodPost, "/rooms/" + s + "/members/" + idText(sc.bob.ID) + "/remove", nil, true},
		{http.MethodPost, "/rooms/" + s + "/posts", editorForm("text", "intruder", ""), false},
		{http.MethodPost, "/rooms/" + s + "/board/tasks/" + tk, form("title", "stolen"), false},
		{http.MethodPost, "/rooms/" + s + "/board/tasks/" + tk + "/move", form("column", c), false},
		{http.MethodPost, "/rooms/" + s + "/board/columns", form("name", "stolen"), false},
		{http.MethodPost, "/rooms/" + s + "/board/columns/" + c + "/rename", form("name", "stolen"), false},
		{http.MethodPost, "/rooms/" + s + "/board/columns/" + c + "/move", form("position", "1"), false},
		{http.MethodPost, "/rooms/" + s + "/board/columns/" + c + "/delete", nil, false},
		{http.MethodGet, p, nil, false},
		{http.MethodGet, cm, nil, false},
		{http.MethodGet, p + "/edit", nil, false},
		{http.MethodGet, p + "/view", nil, false},
		{http.MethodGet, p + "/stream", nil, false},
		{http.MethodGet, p + "/reply", nil, false},
		{http.MethodPost, p + "/edit", editorForm("text", "stolen", ""), false},
		{http.MethodPost, p + "/delete", nil, false},
		{http.MethodPost, p + "/replies", editorForm("text", "intruder", ""), false},
		{http.MethodPost, p + "/reactions", form("emoji", "👍"), false},
		{http.MethodGet, attachmentPathOf(sc.sFile), nil, false},
		{http.MethodGet, "/messages/" + v, nil, false},
		{http.MethodGet, "/messages/" + v + "/older", nil, false},
		{http.MethodGet, "/messages/" + v + "/stream", nil, false},
		{http.MethodPost, "/messages/" + v, form("body", "intruder"), false},
		{http.MethodPost, "/messages/" + v + "/members", form("users", "admin"), false},
		{http.MethodPost, "/messages/" + v + "/rename", form("name", "stolen"), false},
		{http.MethodPost, "/messages/" + v + "/leave", nil, false},
		{http.MethodGet, "/messages/m/" + m + "/view", nil, false},
		{http.MethodGet, "/messages/m/" + m + "/edit", nil, false},
		{http.MethodGet, "/messages/m/" + m + "/reply", nil, false},
		{http.MethodPost, "/messages/m/" + m + "/edit", form("body", "stolen"), false},
		{http.MethodPost, "/messages/m/" + m + "/delete", nil, false},
		{http.MethodPost, "/messages/m/" + m + "/react", form("emoji", "👍"), false},
		{http.MethodGet, messageAttachmentPathOf(sc.msgFile), nil, false},
	}
	users := []string{"carol", "admin"}
	for _, user := range users {
		for _, rq := range requests {
			if user == "admin" && rq.adminMay {
				continue
			}
			t.Run(user+" "+rq.method+" "+rq.path, func(t *testing.T) {
				// The conversation is of alice and bob; carol and the admin are
				// outsiders to both. The private room is alice's and bob's.
				rec := sc.roomsEnv.send(sc.send(user, rq))
				if rec.Code == http.StatusSeeOther && rq.method == http.MethodGet {
					// A plain request for the part of a page (the feed, the
					// view of a post, older messages) is sent to the page,
					// which is the 404 page for them.
					rec = sc.roomsEnv.send(get(rec.Header().Get("Location"), sc.cookies[user]))
				}
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404; body:\n%.400s", rec.Code, rec.Body)
				}
				requireBody(t, rec, nil, "secret plans", "secret comment", "private words", "memo.txt", "notes.txt", "secret task")
			})
		}
	}
	// Nothing changed.
	if r := sc.room("secret"); r.Name != "secret" || !sc.isMember("bob", "secret") || sc.isMember("carol", "secret") {
		t.Errorf("the room changed: %+v", r)
	}
	if got := sc.getPost("alice", sc.sPost.ID); got.Body != "secret plans" || len(got.Reactions) != 0 || len(got.Attachments) != 1 {
		t.Errorf("the post changed: %+v", got)
	}
	if got := sc.history("alice", sc.conv); len(got) != 1 || got[0].Body != "private words" || got[0].Deleted {
		t.Errorf("the conversation changed: %+v", got)
	}
	if got := sc.boardOf("alice", "secret"); len(got.Columns) != 3 || got.Columns[0].Name != "To do" || got.Columns[0].Tasks[0].Title != "secret task" {
		t.Errorf("the board changed: %+v", got)
	}
	for _, f := range []string{"stolen", "intruder"} {
		if n := len(sc.feed("alice", "secret")); n != 1 {
			t.Errorf("the feed of the room has %d posts after %q", n, f)
		}
	}
}

// attachmentPathOf is the path that downloads an attachment of a post.
func attachmentPathOf(a app.AttachmentView) string {
	return "/files/" + idText(a.ID) + "/" + a.FileName
}

// messageAttachmentPathOf is the path that downloads an attachment of a
// message.
func messageAttachmentPathOf(a app.AttachmentView) string {
	return "/messages/files/" + idText(a.ID) + "/" + a.FileName
}

// TestObjectsOfAnotherParentAreRefused: the IDs in a path, a form or a query
// are checked to belong to the parent they are given with. alice is a member of
// both rooms and of both conversations, so only the pairing is wrong.
func TestObjectsOfAnotherParentAreRefused(t *testing.T) {
	t.Parallel()
	sc := newScope(t)
	g, s := idText(sc.general.ID), idText(sc.secret.ID)
	gt, st := idText(sc.gTask.ID), idText(sc.sTask.ID)
	gc, scol := idText(sc.gCol), idText(sc.sColumn)
	form := func(kv ...string) url.Values {
		u := url.Values{}
		for i := 0; i < len(kv); i += 2 {
			u.Set(kv[i], kv[i+1])
		}
		return u
	}
	tests := []struct {
		name string
		rq   request
	}{
		{"a card of another room on the board of this one", request{http.MethodPost, "/rooms/" + g + "/board/tasks/" + st + "/move", form("column", gc), false}},
		{"a card of another room, edited", request{http.MethodPost, "/rooms/" + g + "/board/tasks/" + st, form("title", "moved"), false}},
		{"a card of another room, opened", request{http.MethodGet, "/rooms/" + g + "/board/tasks/" + st + "/edit", nil, false}},
		{"a column of another room as the target of a move", request{http.MethodPost, "/rooms/" + g + "/board/tasks/" + gt + "/move", form("column", scol), false}},
		{"a column of another room, renamed", request{http.MethodPost, "/rooms/" + g + "/board/columns/" + scol + "/rename", form("name", "moved"), false}},
		{"a column of another room, moved", request{http.MethodPost, "/rooms/" + g + "/board/columns/" + scol + "/move", form("position", "2"), false}},
		{"a column of another room, deleted", request{http.MethodPost, "/rooms/" + g + "/board/columns/" + scol + "/delete", nil, false}},
		{"a card of this room under the other room", request{http.MethodPost, "/rooms/" + s + "/board/tasks/" + gt + "/move", form("column", scol), false}},
		{"a member of another room removed here", request{http.MethodPost, "/rooms/" + g + "/members/" + idText(sc.carol.ID) + "/remove", nil, false}},
	}
	before := layoutOf(sc)
	for _, tt := range tests {
		for _, datastar := range []bool{false, true} {
			name := tt.name
			if datastar {
				name += " by Datastar"
			}
			t.Run(name, func(t *testing.T) {
				req := sc.send("alice", tt.rq)
				if datastar {
					asDatastar()(req)
				}
				rec := sc.roomsEnv.send(req)
				if rec.Code == http.StatusSeeOther {
					t.Fatalf("the request was accepted: %d to %s", rec.Code, rec.Header().Get("Location"))
				}
				if !datastar && rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 404 or 403", rec.Code)
				}
			})
		}
	}
	if after := layoutOf(sc); after != before {
		t.Errorf("a board changed:\nbefore %s\nafter  %s", before, after)
	}
}

// layoutOf describes both boards, to compare them before and after.
func layoutOf(sc *scope) string {
	var sb strings.Builder
	for _, room := range []string{"general", "secret"} {
		view := sc.boardOf("alice", room)
		sb.WriteString(room + ":")
		for _, col := range view.Columns {
			sb.WriteString("[" + idText(col.ID) + " " + col.Name)
			for _, c := range col.Tasks {
				sb.WriteString(" " + idText(c.ID) + "=" + c.Title)
			}
			sb.WriteString("]")
		}
	}
	return sb.String()
}

// TestEditCannotKeepTheFileOfAnotherPost: the "keep" of an edit lists
// attachments of the post being edited; the ID of one of another post, in a
// room the editor reads too, is refused and the file stays where it was.
func TestEditCannotKeepTheFileOfAnotherPost(t *testing.T) {
	t.Parallel()
	sc := newScope(t)
	form := editorForm("text", "general plans", "")
	form.Add("keep", idText(sc.gFile.ID))
	form.Add("keep", idText(sc.sFile.ID))
	rec := sc.post("alice", postPathOf(sc.gPost, "edit"), form, true)
	requireSSE(t, rec)
	if got := sc.getPost("alice", sc.sPost.ID); len(got.Attachments) != 1 || got.Attachments[0].ID != sc.sFile.ID {
		t.Errorf("the file of the other post moved: %+v", got.Attachments)
	}
	got := sc.getPost("alice", sc.gPost.ID)
	if len(got.Attachments) != 1 || got.Attachments[0].ID != sc.gFile.ID {
		t.Errorf("the post has the attachments %+v, want only its own", got.Attachments)
	}
	// The same through the removal list.
	form = editorForm("text", "general plans", "")
	form.Add("keep", idText(sc.gFile.ID))
	form.Add("remove", idText(sc.sFile.ID))
	sc.post("alice", postPathOf(sc.gPost, "edit"), form, true)
	if got := sc.getPost("alice", sc.sPost.ID); len(got.Attachments) != 1 {
		t.Errorf("the file of the other post was removed: %+v", got.Attachments)
	}
}

// TestMessageCannotReplyToAnotherConversation: the "reply_to" of a message is a
// message of the same conversation; one of another conversation of the sender
// is refused, and the quote of it never shows.
func TestMessageCannotReplyToAnotherConversation(t *testing.T) {
	t.Parallel()
	sc := newScope(t)
	form := url.Values{"body": {"quoting"}, "reply_to": {idText(sc.msg2.ID)}}
	rec := sc.post("alice", "/messages/"+idText(sc.conv), form, true)
	requireSSE(t, rec)
	if got := sc.history("alice", sc.conv); len(got) != 1 {
		t.Errorf("the conversation has %d messages, want the one it had: %+v", len(got), got)
	}
	requireBody(t, rec, nil, "words for carol")
	// The reply link of the page ignores a message of another conversation.
	page := sc.getAs("alice", "/messages/"+idText(sc.conv)+"?reply="+idText(sc.msg2.ID), false)
	requireBody(t, page, nil, "words for carol")
}

// TestAdminRoutesRefuseMembers: every admin route is the 404 page to a member
// (the admin area is invisible to them) and changes nothing.
func TestAdminRoutesRefuseMembers(t *testing.T) {
	t.Parallel()
	sc := newScope(t)
	u, g := idText(sc.bob.ID), idText(sc.general.ID)
	requests := []request{
		{http.MethodGet, "/admin/users", nil, false},
		{http.MethodPost, "/admin/users/" + u + "/role", url.Values{"role": {"admin"}}, false},
		{http.MethodPost, "/admin/users/" + u + "/disabled", url.Values{"disabled": {"true"}}, false},
		{http.MethodPost, "/admin/users/" + u + "/reset-link", nil, false},
		{http.MethodPost, "/admin/invitations", url.Values{"role": {"admin"}}, false},
		{http.MethodGet, "/admin/rooms", nil, false},
		{http.MethodGet, "/admin/rooms/" + g + "/members", nil, false},
		{http.MethodPost, "/admin/rooms/" + g + "/rename", url.Values{"name": {"stolen"}}, false},
		{http.MethodPost, "/admin/rooms/" + g + "/delete", nil, false},
		{http.MethodPost, "/admin/rooms/" + g + "/members/" + u + "/remove", nil, false},
		{http.MethodGet, "/admin/audit", nil, false},
	}
	for _, rq := range requests {
		t.Run(rq.method+" "+rq.path, func(t *testing.T) {
			rec := sc.roomsEnv.send(sc.send("alice", rq))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body:\n%.300s", rec.Code, rec.Body)
			}
		})
	}
	if r := sc.room("general"); r.Name != "general" || !sc.isMember("bob", "general") {
		t.Errorf("the room changed: %+v", r)
	}
	if sc.users("bob").Role != app.RoleMember {
		t.Error("bob got another role")
	}
}

// countingBody is an endless body that counts the bytes read from it.
type countingBody struct {
	prefix string
	read   atomic.Int64
	pos    int64
}

// Read gives prefix and then "a"s, up to a fixed size.
func (b *countingBody) Read(p []byte) (int, error) {
	const size = 12 << 20
	if b.pos >= size {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > size-b.pos {
		n = int(size - b.pos)
	}
	for i := 0; i < n; i++ {
		if j := b.pos + int64(i); j < int64(len(b.prefix)) {
			p[i] = b.prefix[j]
		} else {
			p[i] = 'a'
		}
	}
	b.pos += int64(n)
	b.read.Add(int64(n))
	return n, nil
}

// TestEveryPostRouteCapsItsBody is the check of "Input limits": a body of
// 12 MiB sent to any route that takes a body is read for at most the cap of the
// route (64 KiB for a form, 512 KiB for a form with a post, and for an upload
// the files and a megabyte), then refused. The test goes through every POST
// route of the server, so a new one has to be added to the table.
func TestEveryPostRouteCapsItsBody(t *testing.T) {
	t.Parallel()
	sc := newScope(t)
	paths := sc.postPaths()
	// The routes that read the body of a post or a message.
	bodyRoutes := map[string]bool{
		"POST /rooms/{id}/posts": true, "POST /posts/{id}/edit": true, "POST /posts/{id}/replies": true,
		"POST /preview": true, "POST /messages/{id}": true, "POST /messages/m/{msgID}/edit": true,
	}
	covered := map[string]bool{}
	for _, pattern := range sc.srv.patterns {
		if !strings.HasPrefix(pattern, "POST ") {
			continue
		}
		path, ok := paths[pattern]
		if !ok {
			t.Errorf("the route %q has no row in the table of this test", pattern)
			continue
		}
		covered[pattern] = true
		for _, multipart := range []bool{false, true} {
			name := pattern
			if multipart {
				name += " multipart"
			}
			t.Run(name, func(t *testing.T) {
				// A fresh environment per request, so that a route that acts
				// (a logout) does not change the next one.
				e := newScope(t)
				body := &countingBody{prefix: "x="}
				contentType := "application/x-www-form-urlencoded"
				if multipart {
					body.prefix = "--B\r\nContent-Disposition: form-data; name=\"body\"\r\n\r\n"
					contentType = "multipart/form-data; boundary=B"
				}
				req := httpPost(path, body, contentType)
				e.cookies["admin"](req)
				rec := e.roomsEnv.send(req)
				// A form is read for 64 KiB at most; the editors of posts and
				// messages take the body of a post, 512 KiB.
				limit := int64(maxFormBytes)
				if bodyRoutes[pattern] {
					limit = maxPostFormBytes
				}
				if pattern == "POST /me/settings/avatar" && multipart {
					limit = app.AvatarMaxBytes + avatarFormSlack
				}
				if got := body.read.Load(); got > limit+(8<<10) {
					t.Errorf("read %d bytes, want at most %d; status %d", got, limit, rec.Code)
				}
			})
		}
	}
	for pattern := range paths {
		if !covered[pattern] {
			t.Errorf("the table has the row %q, which is no route", pattern)
		}
	}
}

// httpPost makes a POST request with the body.
func httpPost(path string, body io.Reader, contentType string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, body)
	r.Header.Set("Content-Type", contentType)
	return r
}

// hostile is text with every character that matters in HTML, in an attribute
// and in the JavaScript of a Datastar expression, and a marker to find it.
const hostile = `ZZMARK'"\</script><img src=x onerror=alert(1)>@get('/x')${a}`

// hostileFile is hostile without the slashes, which a file name loses.
const hostileFile = `ZZMARK'"\<img src=x onerror=alert(1)>@get('x')${a}.txt`

var (
	// expressionAttr finds the attributes whose value Datastar runs as
	// JavaScript.
	expressionAttr = regexp.MustCompile(`\sdata-(?:on[^=\s]*|init|effect|signals[^=\s]*|text|show|class[^=\s]*|attr[^=\s]*|bind[^=\s]*|computed[^=\s]*|style[^=\s]*)="([^"]*)"`)
	// dangerousURL finds an href or src with a script or data address.
	dangerousURL = regexp.MustCompile(`(?i)\s(?:href|src|action)="\s*(?:javascript|vbscript|data):`)
	// quotedValue finds a quoted attribute value, to take it out before the
	// attributes are looked at.
	quotedValue = regexp.MustCompile(`="[^"]*"`)
	// inlineAttr finds an element with an event handler or a style attribute,
	// which the Content-Security-Policy does not allow.
	inlineAttr = regexp.MustCompile(`<[a-zA-Z][^>]*\s(?:on[a-z]+|style)=`)
)

// TestUserTextIsEscapedEverywhere: text of people (names, descriptions, bodies in
// both formats, tags, file names, task titles, column names, conversation
// names, messages) with quotes, backslashes, "</script>" and Datastar syntax
// never becomes markup, never reaches a data-* expression, and never makes an
// inline handler.
func TestUserTextIsEscapedEverywhere(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	ctx := t.Context()
	alice := e.actor(e.alice)
	room, err := e.rooms.Create(ctx, alice, app.RoomInput{Name: hostile[:40], Description: hostile, Visibility: app.VisibilityPrivate})
	if err != nil {
		t.Fatalf("Create room: %+v", err)
	}
	if err := e.rooms.AddMember(ctx, alice, room.ID, e.bob.ID); err != nil {
		t.Fatal(err)
	}
	up, err := e.posts.Upload(ctx, alice, hostileFile, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	var posts []app.Post
	for _, f := range []app.Format{app.FormatText, app.FormatMarkdown} {
		p, err := e.posts.Create(ctx, alice, room.ID, app.PostInput{
			Format: f, Body: hostile + "\n\n[x](javascript:alert(1)) ![y](data:text/html,x) <b onmouseover=alert(1)>hi</b> @bob",
			Tags: []string{"zzmark"}, Uploads: []app.Upload{up}, Tasks: []app.TaskInput{{Title: hostile}},
		})
		if err != nil {
			t.Fatalf("Create post: %+v", err)
		}
		posts = append(posts, p)
		up, err = e.posts.Upload(ctx, alice, hostileFile, strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.posts.Reply(ctx, e.actor(e.bob), p.ID, app.PostInput{Format: f, Body: "reply " + hostile}); err != nil {
			t.Fatalf("Reply: %+v", err)
		}
	}
	if _, err := e.board.AddColumn(ctx, alice, room.ID, hostile[:40]); err != nil {
		t.Fatalf("AddColumn: %+v", err)
	}
	conv, err := e.msgs.StartGroup(ctx, alice, []uint64{e.bob.ID, e.carol.ID}, hostile[:50])
	if err != nil {
		t.Fatalf("StartGroup: %+v", err)
	}
	e.say("alice", conv, hostile)
	msgUp := e.uploadMessage("alice", hostileFile)
	e.sayIn("alice", conv, app.MessageInput{Body: hostile, Uploads: []app.Upload{msgUp}})

	r := idText(room.ID)
	pages := []struct{ user, path string }{
		{"alice", "/"}, {"alice", "/rooms"}, {"alice", "/rooms/" + r}, {"bob", "/rooms/" + r},
		{"alice", "/rooms/" + r + "/board"}, {"alice", postPathOf(posts[0], "")}, {"alice", postPathOf(posts[1], "")},
		{"alice", postPathOf(posts[0], "edit")}, {"alice", postPathOf(posts[0], "reply")},
		{"alice", "/search?q=" + url.QueryEscape("#zzmark")}, {"alice", "/search?q=" + url.QueryEscape(hostile)},
		{"alice", "/messages"}, {"bob", "/messages/" + idText(conv)}, {"bob", "/messages"},
		{"bob", "/notifications"}, {"admin", "/admin/rooms"}, {"admin", "/admin/rooms?members=" + r},
		{"admin", "/admin/audit"}, {"admin", "/admin/users"},
	}
	for _, pg := range pages {
		for _, datastar := range []bool{false, true} {
			t.Run(pg.user+" "+pg.path, func(t *testing.T) {
				var rec *httptest.ResponseRecorder
				if datastar {
					rec = e.getAs(pg.user, pg.path, true)
				} else {
					rec = e.getAs(pg.user, pg.path, false)
				}
				if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
					t.Skipf("status %d", rec.Code)
				}
				body := rec.Body.String()
				for _, bad := range []string{"<img src=x", "</script><img", "<b onmouseover", "<script>alert", `ZZMARK'"`} {
					if strings.Contains(body, bad) {
						t.Errorf("the page holds %q:\n%.600s", bad, body)
					}
				}
				if (pg.path == "/rooms/"+r || pg.path == postPathOf(posts[1], "")) && !strings.Contains(body, "ZZMARK") {
					t.Errorf("the page does not show the text, so the test checks nothing:\n%.300s", body)
				}
				if m := dangerousURL.FindString(body); m != "" {
					t.Errorf("a link or an image has a dangerous address: %s", m)
				}
				for _, m := range expressionAttr.FindAllStringSubmatch(body, -1) {
					v := html.UnescapeString(m[1])
					for _, bad := range []string{"ZZMARK'", `ZZMARK"`, `ZZMARK\`, "<img", "</script", "onerror=", "@get('/x')", "${a}"} {
						if strings.Contains(v, bad) {
							t.Errorf("a data-* expression holds user text (%q): %s", bad, m[0])
						}
					}
				}
				if m := inlineAttr.FindString(quotedValue.ReplaceAllString(body, `=""`)); m != "" {
					t.Errorf("an element has an inline handler or style: %s", m)
				}
			})
		}
	}
}

// postPaths maps the pattern of every POST route to a path of it that names
// real things of the scope.
func (sc *scope) postPaths() map[string]string {
	g, s := idText(sc.general.ID), idText(sc.secret.ID)
	gp, gm := idText(sc.gPost.ID), idText(sc.msg.ID)
	gt, gc, cv := idText(sc.gTask.ID), idText(sc.gCol), idText(sc.conv)
	u := idText(sc.bob.ID)
	return map[string]string{
		"POST /login":                                      "/login",
		"POST /logout":                                     "/logout",
		"POST /invite/{token}":                             "/invite/sometoken",
		"POST /reset/{token}":                              "/reset/sometoken",
		"POST /admin/users/{id}/reset-link":                "/admin/users/" + u + "/reset-link",
		"POST /admin/users/{id}/role":                      "/admin/users/" + u + "/role",
		"POST /admin/users/{id}/disabled":                  "/admin/users/" + u + "/disabled",
		"POST /admin/invitations":                          "/admin/invitations",
		"POST /admin/rooms/{id}/rename":                    "/admin/rooms/" + g + "/rename",
		"POST /admin/rooms/{id}/delete":                    "/admin/rooms/" + g + "/delete",
		"POST /admin/rooms/{id}/members/{userID}/remove":   "/admin/rooms/" + g + "/members/" + u + "/remove",
		"POST /rooms":                                      "/rooms",
		"POST /rooms/{id}/join":                            "/rooms/" + g + "/join",
		"POST /rooms/{id}/leave":                           "/rooms/" + g + "/leave",
		"POST /rooms/{id}/rename":                          "/rooms/" + g + "/rename",
		"POST /rooms/{id}/delete":                          "/rooms/" + g + "/delete",
		"POST /rooms/{id}/members":                         "/rooms/" + s + "/members",
		"POST /rooms/{id}/members/{userID}/remove":         "/rooms/" + s + "/members/" + u + "/remove",
		"POST /rooms/{id}/board/tasks/{taskID}":            "/rooms/" + g + "/board/tasks/" + gt,
		"POST /rooms/{id}/board/tasks/{taskID}/move":       "/rooms/" + g + "/board/tasks/" + gt + "/move",
		"POST /rooms/{id}/board/columns":                   "/rooms/" + g + "/board/columns",
		"POST /rooms/{id}/board/columns/{columnID}/rename": "/rooms/" + g + "/board/columns/" + gc + "/rename",
		"POST /rooms/{id}/board/columns/{columnID}/move":   "/rooms/" + g + "/board/columns/" + gc + "/move",
		"POST /rooms/{id}/board/columns/{columnID}/delete": "/rooms/" + g + "/board/columns/" + gc + "/delete",
		"POST /rooms/{id}/posts":                           "/rooms/" + g + "/posts",
		"POST /posts/{id}/edit":                            "/posts/" + gp + "/edit",
		"POST /posts/{id}/delete":                          "/posts/" + gp + "/delete",
		"POST /posts/{id}/replies":                         "/posts/" + gp + "/replies",
		"POST /posts/{id}/reactions":                       "/posts/" + gp + "/reactions",
		"POST /posts/{id}/follow":                          "/posts/" + gp + "/follow",
		"POST /posts/{id}/unfollow":                        "/posts/" + gp + "/unfollow",
		"POST /preview":                                    "/preview",
		"POST /messages":                                   "/messages",
		"POST /messages/{id}":                              "/messages/" + cv,
		"POST /messages/{id}/members":                      "/messages/" + cv + "/members",
		"POST /messages/{id}/rename":                       "/messages/" + cv + "/rename",
		"POST /messages/{id}/leave":                        "/messages/" + cv + "/leave",
		"POST /messages/m/{msgID}/edit":                    "/messages/m/" + gm + "/edit",
		"POST /messages/m/{msgID}/delete":                  "/messages/m/" + gm + "/delete",
		"POST /messages/m/{msgID}/react":                   "/messages/m/" + gm + "/react",
		"POST /notifications/read":                         "/notifications/read",
		"POST /me/sessions/{id}/end":                       "/me/sessions/99999/end",
		"POST /me/settings/profile":                        "/me/settings/profile",
		"POST /me/settings/avatar":                         "/me/settings/avatar",
		"POST /me/settings/avatar/remove":                  "/me/settings/avatar/remove",
		"POST /me/settings/password":                       "/me/settings/password",
		"POST /me/settings/appearance":                     "/me/settings/appearance",
		"POST /me/settings/notifications":                  "/me/settings/notifications",
		"POST /me/settings/ssh-keys":                       "/me/settings/ssh-keys",
		"POST /me/settings/ssh-keys/{id}/remove":           "/me/settings/ssh-keys/99999/remove",
		"POST /rooms/{id}/notifications":                   "/rooms/" + g + "/notifications",
		"POST /messages/{id}/notifications":                "/messages/" + cv + "/notifications",
	}
}

// TestEveryPostRouteRefusesCrossOriginRequests is the check of "Cross-origin
// protection": a POST that the browser marks cross-site, or that comes from
// another origin, is refused with 403 before its handler runs, for every POST
// route of the server, uploads included. The one GET that changes state is the
// opening of a conversation, which marks it read (D33).
func TestEveryPostRouteRefusesCrossOriginRequests(t *testing.T) {
	t.Parallel()
	sc := newScope(t)
	paths := sc.postPaths()
	headers := map[string]map[string]string{
		"cross-site":   {"Sec-Fetch-Site": "cross-site"},
		"same-site":    {"Sec-Fetch-Site": "same-site"},
		"other origin": {"Origin": "https://evil.example"},
	}
	before := layoutOf(sc)
	for _, pattern := range sc.srv.patterns {
		if !strings.HasPrefix(pattern, "POST ") {
			continue
		}
		path, ok := paths[pattern]
		if !ok {
			t.Errorf("the route %q has no row in the table", pattern)
			continue
		}
		for name, hdr := range headers {
			for _, multipart := range []bool{false, true} {
				t.Run(pattern+" "+name+fmt.Sprint(multipart), func(t *testing.T) {
					body, contentType := "a=b", "application/x-www-form-urlencoded"
					if multipart {
						body, contentType = "--B\r\nContent-Disposition: form-data; name=\"body\"\r\n\r\nx\r\n--B--\r\n", "multipart/form-data; boundary=B"
					}
					req := httpPost(path, strings.NewReader(body), contentType)
					sc.cookies["admin"](req)
					for k, v := range hdr {
						req.Header.Set(k, v)
					}
					rec := sc.roomsEnv.send(req)
					if rec.Code != http.StatusForbidden {
						t.Fatalf("status = %d, want 403", rec.Code)
					}
				})
			}
		}
	}
	if after := layoutOf(sc); after != before {
		t.Errorf("a board changed")
	}
	if !sc.exists("general") || len(sc.feed("alice", "general")) != 1 {
		t.Error("something changed")
	}
}
