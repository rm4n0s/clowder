package api

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// postIDs reads the IDs of the posts of a list in an answer.
func postIDs(t *testing.T, v any) []uint64 {
	t.Helper()
	ids := []uint64{}
	for _, p := range list(t, v) {
		ids = append(ids, idOf(t, p))
	}
	return ids
}

// postReadStatuses is the table of who reads the posts of a room: the same as
// the room itself (D26, D29).
var postReadStatuses = roomReadStatuses

// postWriteStatuses is the table of who writes in a room, comments in it and
// reacts: members only. A visible room the user has not joined is 403, a
// private room the user is not in is 404, admins included.
var postWriteStatuses = []struct {
	user, room string
	want       int
}{
	{"bob", "lobby", http.StatusCreated}, {"admin", "lobby", http.StatusCreated},
	{"alice", "lobby", http.StatusForbidden}, {"carol", "lobby", http.StatusForbidden}, {"root", "lobby", http.StatusForbidden},
	{"alice", "den", http.StatusCreated}, {"bob", "den", http.StatusCreated},
	{"admin", "den", http.StatusNotFound}, {"root", "den", http.StatusNotFound}, {"carol", "den", http.StatusNotFound},
}

func TestFeedPaging(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	var made []uint64
	for i := 0; i < 7; i++ {
		made = append(made, f.write("bob", "lobby", fmt.Sprintf("post %d", i)).ID)
		f.clk.Advance(time.Minute)
	}
	newestFirst := make([]uint64, len(made))
	for i, id := range made {
		newestFirst[len(made)-1-i] = id
	}
	feed := func(query string) map[string]any {
		t.Helper()
		return f.ok("carol", http.MethodGet, path("/rooms/%d/posts", f.lobby.ID)+query, nil, http.StatusOK)
	}

	t.Run("the default page holds everything here", func(t *testing.T) {
		got := feed("")
		if ids := postIDs(t, got["posts"]); !reflect.DeepEqual(ids, newestFirst) {
			t.Errorf("ids = %v, want %v", ids, newestFirst)
		}
		if got["next_before"] != nil {
			t.Errorf("next_before = %v on the last page", got["next_before"])
		}
	})
	t.Run("pages of three", func(t *testing.T) {
		var seen []uint64
		query := "?limit=3"
		for page, wantSize := range []int{3, 3, 1} {
			got := feed(query)
			ids := postIDs(t, got["posts"])
			if len(ids) != wantSize {
				t.Fatalf("page %d has %d posts, want %d", page, len(ids), wantSize)
			}
			seen = append(seen, ids...)
			last := page == 2
			if last != (got["next_before"] == nil) {
				t.Fatalf("page %d: next_before = %v", page, got["next_before"])
			}
			if !last {
				if got["next_before"] != float64(ids[len(ids)-1]) {
					t.Errorf("page %d: next_before = %v, want the last ID %d", page, got["next_before"], ids[len(ids)-1])
				}
				query = fmt.Sprintf("?limit=3&before=%d", uint64(got["next_before"].(float64)))
			}
		}
		if !reflect.DeepEqual(seen, newestFirst) {
			t.Errorf("seen = %v, want %v", seen, newestFirst)
		}
	})
	t.Run("the largest page", func(t *testing.T) {
		if got := feed("?limit=100"); len(list(t, got["posts"])) != 7 {
			t.Errorf("posts = %v", got["posts"])
		}
	})
	t.Run("before the oldest", func(t *testing.T) {
		got := feed(fmt.Sprintf("?before=%d", made[0]))
		if len(list(t, got["posts"])) != 0 || got["next_before"] != nil {
			t.Errorf("feed = %v", got)
		}
	})
	t.Run("a before that is no post is not an error", func(t *testing.T) {
		if got := feed("?before=999999"); len(list(t, got["posts"])) != 7 {
			t.Errorf("feed = %v", got)
		}
	})
	t.Run("comments are not in the feed", func(t *testing.T) {
		f.say("admin", made[0], "a comment")
		if got := feed(""); len(list(t, got["posts"])) != 7 {
			t.Errorf("feed = %v", got)
		}
	})

	for _, tt := range []struct {
		query, field string
	}{
		{"?limit=0", "limit"}, {"?limit=-1", "limit"}, {"?limit=101", "limit"}, {"?limit=1000000000000000000000", "limit"},
		{"?limit=abc", "limit"}, {"?limit=", "limit"}, {"?limit=1.5", "limit"},
		{"?before=0", "before"}, {"?before=abc", "before"}, {"?before=-3", "before"}, {"?before=", "before"},
		{"?before=1&limit=0", "limit"},
	} {
		t.Run("the query "+tt.query, func(t *testing.T) {
			rec := f.call("carol", http.MethodGet, path("/rooms/%d/posts", f.lobby.ID)+tt.query, nil)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if _, ok := errorFields(t, rec)[tt.field]; !ok {
				t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
			}
		})
	}
}

func TestFeedPermissions(t *testing.T) {
	t.Parallel()
	for _, tt := range postReadStatuses {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			f.write("bob", tt.room, "a post")
			rec := f.call(tt.user, http.MethodGet, path("/rooms/%d/posts", f.room(tt.room).ID), nil)
			requireStatus(t, rec, tt.want)
			if tt.want == http.StatusOK {
				posts := list(t, decode(t, rec)["posts"])
				if len(posts) != 1 {
					t.Fatalf("posts = %v", posts)
				}
				p := obj(t, posts[0])
				// A reader who has not joined sees the post, and may not touch it.
				if p["can_edit"] != (tt.user == "bob") || p["can_delete"] != (tt.user == "bob" || tt.user == "admin" || tt.user == "root") {
					t.Errorf("can_edit %v, can_delete %v for %s", p["can_edit"], p["can_delete"], tt.user)
				}
			}
		})
	}
}

func TestCreatePost(t *testing.T) {
	t.Parallel()
	t.Run("a markdown post with a tag and a task", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		before := len(f.audit())
		rec := f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{
			"format": "markdown",
			"body":   "**Plan** for <b>today</b> with @admin",
			"tags":   []string{"#Plan", "go", "plan"},
			"tasks": []map[string]any{
				{"title": "Write it", "assignee_ids": []uint64{f.userID("admin")}, "due_date": "2026-11-01"},
				{"title": "Ship it"},
			},
		})
		requireStatus(t, rec, http.StatusCreated)
		p := decode(t, rec)
		if loc := rec.Header().Get("Location"); loc != fmt.Sprintf("/api/v1/posts/%d", uint64(p["id"].(float64))) {
			t.Errorf("Location = %q", loc)
		}
		if p["format"] != "markdown" || p["body"] != "**Plan** for <b>today</b> with @admin" {
			t.Errorf("format %v, body %v", p["format"], p["body"])
		}
		html := p["html"].(string)
		if !strings.Contains(html, "<strong>Plan</strong>") || strings.Contains(html, "<b>") {
			t.Errorf("html = %q: the body is rendered with raw HTML escaped", html)
		}
		if !reflect.DeepEqual(p["tags"], []any{"plan", "go"}) {
			t.Errorf("tags = %v", p["tags"])
		}
		tasks := list(t, p["tasks"])
		if len(tasks) != 2 {
			t.Fatalf("tasks = %v", tasks)
		}
		first, second := obj(t, tasks[0]), obj(t, tasks[1])
		if first["title"] != "Write it" || first["due_date"] != "2026-11-01" || first["column_id"] == float64(0) {
			t.Errorf("task = %v", first)
		}
		if as := list(t, first["assignees"]); len(as) != 1 || obj(t, as[0])["username"] != "admin" {
			t.Errorf("assignees = %v", as)
		}
		if second["due_date"] != nil || len(list(t, second["assignees"])) != 0 {
			t.Errorf("task = %v", second)
		}
		author := obj(t, p["author"])
		if author["username"] != "bob" || author["display_name"] != "Bob" {
			t.Errorf("author = %v", author)
		}
		if p["parent_id"] != nil || p["root_id"] != p["id"] || p["room_id"] != float64(f.lobby.ID) || p["comment_count"] != float64(0) ||
			p["edited_at"] != nil || p["deleted"] != false || p["can_edit"] != true || p["can_delete"] != true ||
			p["created_at"] != "2026-10-26T09:30:00Z" {
			t.Errorf("post = %v", p)
		}
		if len(list(t, p["reactions"])) != 0 || len(list(t, p["attachments"])) != 0 {
			t.Errorf("post = %v", p)
		}
		f.requireAudit(before, "post.created post", "task.created task", "task.created task")
		// The post is what the feed and GET show.
		got := f.ok("carol", http.MethodGet, path("/posts/%d", uint64(p["id"].(float64))), nil, http.StatusOK)
		if got["can_edit"] != false || got["can_delete"] != false {
			t.Errorf("as carol: %v", got)
		}
		got["can_edit"], got["can_delete"] = true, true
		if !reflect.DeepEqual(got, p) {
			t.Errorf("GET differs from the created post:\n%v\n%v", got, p)
		}
	})
	t.Run("a text post is shown as written", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.ok("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "line one\n<script>x</script> https://example.org"}, http.StatusCreated)
		if p["format"] != "text" {
			t.Errorf("format = %v, want text by default", p["format"])
		}
		html := p["html"].(string)
		if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") || !strings.Contains(html, `href="https://example.org"`) {
			t.Errorf("html = %q", html)
		}
	})
	t.Run("a post of tasks only", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.ok("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"tasks": []map[string]any{{"title": "Only a task"}}}, http.StatusCreated)
		if p["body"] != "" || len(list(t, p["tasks"])) != 1 {
			t.Errorf("post = %v", p)
		}
	})
	t.Run("a body ending in spaces is trimmed", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.ok("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "hello \n\n"}, http.StatusCreated)
		if p["body"] != "hello" {
			t.Errorf("body = %q", p["body"])
		}
	})
}

func TestCreatePostPermissions(t *testing.T) {
	t.Parallel()
	for _, tt := range postWriteStatuses {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			before := len(f.audit())
			rec := f.call(tt.user, http.MethodPost, path("/rooms/%d/posts", f.room(tt.room).ID), map[string]any{"body": "hello"})
			requireStatus(t, rec, tt.want)
			if tt.want == http.StatusCreated {
				f.requireAudit(before, "post.created post")
			} else {
				f.requireAudit(before)
			}
		})
	}
}

// postMistakes are the bodies of a new post that break a rule, with the field
// that the answer names. They also are the mistakes of a comment.
var postMistakes = []struct {
	name  string
	body  any
	field string
}{
	{"nothing", map[string]any{}, "body"},
	{"a blank body", map[string]any{"body": " \n "}, "body"},
	{"a body of 20,001 characters", map[string]any{"body": strings.Repeat("é", 20001)}, "body"},
	{"a format that does not exist", map[string]any{"format": "html", "body": "x"}, "format"},
	{"an empty format", map[string]any{"format": "", "body": "x"}, "format"},
	{"a tag with a space inside", map[string]any{"body": "x", "tags": []string{"two words"}}, "tags"},
	{"a tag of 51 characters", map[string]any{"body": "x", "tags": []string{strings.Repeat("t", 51)}}, "tags"},
	{"eleven tags", map[string]any{"body": "x", "tags": []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}}, "tags"},
	{"a task without a title", map[string]any{"tasks": []map[string]any{{"title": " "}}}, "tasks"},
	{"a task title of 201 characters", map[string]any{"tasks": []map[string]any{{"title": strings.Repeat("t", 201)}}}, "tasks"},
	{"a due date that is no day", map[string]any{"tasks": []map[string]any{{"title": "t", "due_date": "27/10/2026"}}}, "tasks"},
	{"a due date with a time", map[string]any{"tasks": []map[string]any{{"title": "t", "due_date": "2026-10-27T10:00:00Z"}}}, "tasks"},
	{"a due date that is not a date", map[string]any{"tasks": []map[string]any{{"title": "t", "due_date": "2026-02-30"}}}, "tasks"},
	{"an assignee who is not a member", map[string]any{"tasks": []map[string]any{{"title": "t", "assignee_ids": []uint64{999999}}}}, "tasks"},
	{"a task with an ID on a new post", map[string]any{"tasks": []map[string]any{{"id": 5, "title": "t"}}}, "tasks"},
	{"twenty-one tasks", map[string]any{"body": "x", "tasks": twentyOneTasks()}, "tasks"},
	{"upload IDs", map[string]any{"body": "x", "upload_ids": []string{"abc"}}, "upload_ids"},
	{"attachment IDs to keep", map[string]any{"body": "x", "attachment_ids": []uint64{1}}, "attachment_ids"},
	{"an unknown field", map[string]any{"body": "x", "title": "y"}, "title"},
	{"a body that is a number", `{"body": 5}`, "body"},
	{"tags that are a string", `{"body": "x", "tags": "go"}`, "tags"},
	{"a task ID that is a string", `{"tasks": [{"id": "1", "title": "t"}]}`, "tasks.0.id"},
}

func twentyOneTasks() []map[string]any {
	tasks := make([]map[string]any, 21)
	for i := range tasks {
		tasks[i] = map[string]any{"title": fmt.Sprintf("task %d", i)}
	}
	return tasks
}

func TestPostMistakes(t *testing.T) {
	t.Parallel()
	for _, tt := range postMistakes {
		for _, kind := range []string{"post", "comment"} {
			t.Run(kind+": "+tt.name, func(t *testing.T) {
				t.Parallel()
				f := newForum(t)
				target := path("/rooms/%d/posts", f.lobby.ID)
				if kind == "comment" {
					target = path("/posts/%d/comments", f.write("admin", "lobby", "the post").ID)
				}
				before := len(f.audit())
				rec := f.call("bob", http.MethodPost, target, tt.body)
				requireStatus(t, rec, http.StatusUnprocessableEntity)
				fields := errorFields(t, rec)
				if len(fields) != 1 || fields[tt.field] == nil || fields[tt.field] == "" {
					t.Errorf("fields = %v, want only %q", fields, tt.field)
				}
				f.requireAudit(before)
			})
		}
	}
	t.Run("all the mistakes at once", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		rec := f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID),
			map[string]any{"format": "x", "body": strings.Repeat("a", 20001), "tags": []string{"a b"}, "tasks": []map[string]any{{"title": ""}}})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		got := errorFields(t, rec)
		if len(got) != 4 {
			t.Errorf("fields = %v", got)
		}
	})
	t.Run("the permission comes before the mistakes", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		requireStatus(t, f.call("carol", http.MethodPost, path("/rooms/%d/posts", f.den.ID), map[string]any{}), http.StatusNotFound)
		requireStatus(t, f.call("carol", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{}), http.StatusForbidden)
	})
	t.Run("a body over 64 KiB", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		rec := f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), `{"body":"`+strings.Repeat("x", 65*1024)+`"}`)
		requireStatus(t, rec, http.StatusRequestEntityTooLarge)
	})
}

func TestGetPost(t *testing.T) {
	t.Parallel()
	for _, tt := range postReadStatuses {
		for _, kind := range []string{"post", "comment"} {
			t.Run(kind+": "+tt.user+" on "+tt.room, func(t *testing.T) {
				t.Parallel()
				f := newForum(t)
				p := f.write("bob", tt.room, "bob's post")
				id := p.ID
				if kind == "comment" {
					id = f.say("bob", p.ID, "bob's comment").ID
				}
				rec := f.call(tt.user, http.MethodGet, path("/posts/%d", id), nil)
				requireStatus(t, rec, tt.want)
				if tt.want == http.StatusOK {
					got := decode(t, rec)
					if kind == "comment" && (got["parent_id"] != float64(p.ID) || got["root_id"] != float64(p.ID)) {
						t.Errorf("comment = %v", got)
					}
				}
			})
		}
	}
	t.Run("a post that is gone", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "going")
		requireStatus(t, f.call("bob", http.MethodDelete, path("/posts/%d", p.ID), nil), http.StatusNoContent)
		requireStatus(t, f.call("bob", http.MethodGet, path("/posts/%d", p.ID), nil), http.StatusNotFound)
	})
	t.Run("a deleted post that stays for its replies", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "going", "tag")
		f.say("admin", p.ID, "a reply")
		if _, err := f.posts.ToggleReaction(t.Context(), actorOf(f.testEnv, "admin"), p.ID, "\U0001F44D"); err != nil {
			t.Fatal(err)
		}
		requireStatus(t, f.call("bob", http.MethodDelete, path("/posts/%d", p.ID), nil), http.StatusNoContent)
		got := f.ok("carol", http.MethodGet, path("/posts/%d", p.ID), nil, http.StatusOK)
		if got["deleted"] != true || got["author"] != nil || got["body"] != nil || got["html"] != nil ||
			len(list(t, got["tags"])) != 0 || len(list(t, got["reactions"])) != 0 || got["can_edit"] != false || got["can_delete"] != false {
			t.Errorf("post = %v", got)
		}
		if got["comment_count"] != float64(1) {
			t.Errorf("comment_count = %v", got["comment_count"])
		}
	})
}

func TestThread(t *testing.T) {
	t.Parallel()
	for _, tt := range postReadStatuses {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			p := f.write("bob", tt.room, "the post")
			f.say("bob", p.ID, "a comment")
			rec := f.call(tt.user, http.MethodGet, path("/posts/%d/thread", p.ID), nil)
			requireStatus(t, rec, tt.want)
		})
	}
	t.Run("a tree of any depth in reading order", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		root := f.write("bob", "lobby", "the post")
		first := f.say("admin", root.ID, "first")
		second := f.say("bob", first.ID, "second")
		other := f.say("admin", root.ID, "a sibling of first")
		// A chain deeper than the six levels the page draws: no cut here.
		parent := second
		chain := []uint64{first.ID, second.ID}
		for i := 3; i <= 9; i++ {
			parent = f.say("admin", parent.ID, fmt.Sprintf("level %d", i))
			chain = append(chain, parent.ID)
		}
		got := f.ok("carol", http.MethodGet, path("/posts/%d/thread", root.ID), nil, http.StatusOK)
		if idOf(t, got["post"]) != root.ID || got["focus_id"] != nil {
			t.Errorf("post = %v, focus_id = %v", got["post"], got["focus_id"])
		}
		if obj(t, got["post"])["comment_count"] != float64(len(chain)+1) {
			t.Errorf("comment_count = %v", obj(t, got["post"])["comment_count"])
		}
		comments := list(t, got["comments"])
		var order []uint64
		var depths []float64
		for _, c := range comments {
			m := obj(t, c)
			order = append(order, idOf(t, m["comment"]))
			depths = append(depths, m["depth"].(float64))
			post := obj(t, m["comment"])
			if post["root_id"] != float64(root.ID) {
				t.Errorf("root_id = %v", post["root_id"])
			}
		}
		wantOrder := append(append([]uint64{}, chain...), other.ID)
		if !reflect.DeepEqual(order, wantOrder) {
			t.Errorf("order = %v, want %v", order, wantOrder)
		}
		if !reflect.DeepEqual(depths, []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 1}) {
			t.Errorf("depths = %v", depths)
		}
		deepest := obj(t, obj(t, comments[8])["comment"])
		if deepest["parent_id"] != float64(chain[7]) {
			t.Errorf("parent_id of the deepest = %v, want %d", deepest["parent_id"], chain[7])
		}
	})
	t.Run("asked for a comment", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		root := f.write("bob", "lobby", "the post")
		c := f.say("admin", root.ID, "a comment")
		got := f.ok("carol", http.MethodGet, path("/posts/%d/thread", c.ID), nil, http.StatusOK)
		if idOf(t, got["post"]) != root.ID || got["focus_id"] != float64(c.ID) || len(list(t, got["comments"])) != 1 {
			t.Errorf("thread = %v", got)
		}
	})
	t.Run("a deleted comment that stays for its replies", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		root := f.write("bob", "lobby", "the post")
		c1 := f.say("admin", root.ID, "going")
		f.say("bob", c1.ID, "stays")
		requireStatus(t, f.call("admin", http.MethodDelete, path("/posts/%d", c1.ID), nil), http.StatusNoContent)
		comments := list(t, f.ok("carol", http.MethodGet, path("/posts/%d/thread", root.ID), nil, http.StatusOK)["comments"])
		if len(comments) != 2 {
			t.Fatalf("comments = %v", comments)
		}
		gone := obj(t, obj(t, comments[0])["comment"])
		if gone["deleted"] != true || gone["author"] != nil || gone["body"] != nil {
			t.Errorf("comment = %v", gone)
		}
	})
	t.Run("a thread without comments", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		root := f.write("bob", "lobby", "alone")
		got := f.ok("carol", http.MethodGet, path("/posts/%d/thread", root.ID), nil, http.StatusOK)
		if c := list(t, got["comments"]); len(c) != 0 {
			t.Errorf("comments = %v", c)
		}
	})
}

func TestCreateComment(t *testing.T) {
	t.Parallel()
	for _, tt := range postWriteStatuses {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			root := f.write("bob", tt.room, "the post")
			before := len(f.audit())
			rec := f.call(tt.user, http.MethodPost, path("/posts/%d/comments", root.ID), map[string]any{"body": "a comment"})
			requireStatus(t, rec, tt.want)
			if tt.want == http.StatusCreated {
				got := decode(t, rec)
				if got["parent_id"] != float64(root.ID) || got["root_id"] != float64(root.ID) ||
					rec.Header().Get("Location") != fmt.Sprintf("/api/v1/posts/%d", uint64(got["id"].(float64))) {
					t.Errorf("comment = %v, Location %q", got, rec.Header().Get("Location"))
				}
				f.requireAudit(before, "post.created comment")
			} else {
				f.requireAudit(before)
			}
		})
	}
	t.Run("a reply to a comment, with a tag and a task", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		root := f.write("bob", "lobby", "the post")
		c1 := f.say("admin", root.ID, "first")
		before := len(f.audit())
		got := f.ok("bob", http.MethodPost, path("/posts/%d/comments", c1.ID), map[string]any{
			"format": "markdown", "body": "- yes", "tags": []string{"re"}, "tasks": []map[string]any{{"title": "do it"}},
		}, http.StatusCreated)
		if got["parent_id"] != float64(c1.ID) || got["root_id"] != float64(root.ID) || len(list(t, got["tasks"])) != 1 {
			t.Errorf("comment = %v", got)
		}
		f.requireAudit(before, "post.created comment", "task.created task")
		thread := f.ok("carol", http.MethodGet, path("/posts/%d/thread", root.ID), nil, http.StatusOK)
		if c := list(t, thread["comments"]); len(c) != 2 || obj(t, c[1])["depth"] != float64(2) {
			t.Errorf("thread = %v", thread)
		}
	})
	t.Run("a post that is deleted", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		root := f.write("bob", "lobby", "the post")
		f.say("admin", root.ID, "keeps it")
		requireStatus(t, f.call("bob", http.MethodDelete, path("/posts/%d", root.ID), nil), http.StatusNoContent)
		requireStatus(t, f.call("bob", http.MethodPost, path("/posts/%d/comments", root.ID), map[string]any{"body": "late"}), http.StatusNotFound)
	})
	t.Run("a post that does not exist", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		requireStatus(t, f.call("bob", http.MethodPost, "/api/v1/posts/999999/comments", map[string]any{"body": "x"}), http.StatusNotFound)
	})
}

// richPost makes a markdown post of bob in lobby with two tags, two tasks (the
// first assigned to admin, due on 2026-11-01) and a file, through the
// services.
func (f *forum) richPost() app.Post {
	f.t.Helper()
	bob := actorOf(f.testEnv, "bob")
	up, err := f.posts.Upload(f.t.Context(), bob, "note.txt", strings.NewReader("remember the milk"))
	if err != nil {
		f.t.Fatalf("Upload: %+v", err)
	}
	due := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	p, err := f.posts.Create(f.t.Context(), bob, f.lobby.ID, app.PostInput{
		Format: app.FormatMarkdown, Body: "**old** body", Tags: []string{"one", "two"}, Uploads: []app.Upload{up},
		Tasks: []app.TaskInput{{Title: "first", Assignees: []uint64{f.userID("admin")}, DueDate: &due}, {Title: "second"}},
	})
	if err != nil {
		f.t.Fatalf("Create: %+v", err)
	}
	return p
}

func TestPatchPostKeepsWhatIsLeftOut(t *testing.T) {
	t.Parallel()
	type check func(t *testing.T, got map[string]any, p app.Post)
	titles := func(got map[string]any) []string {
		var out []string
		for _, task := range got["tasks"].([]any) {
			out = append(out, task.(map[string]any)["title"].(string))
		}
		return out
	}
	tests := []struct {
		name  string
		body  any
		audit []string
		check check
	}{
		{"nothing", map[string]any{}, nil, func(t *testing.T, got map[string]any, p app.Post) {
			if got["edited_at"] != nil {
				t.Errorf("edited_at = %v: nothing changed", got["edited_at"])
			}
		}},
		{"null fields", map[string]any{"format": nil, "body": nil, "tags": nil, "tasks": nil, "attachment_ids": nil}, nil, func(t *testing.T, got map[string]any, p app.Post) {
			if got["edited_at"] != nil {
				t.Errorf("edited_at = %v: nothing changed", got["edited_at"])
			}
		}},
		{"only the body", map[string]any{"body": "new body"}, []string{"post.edited post"}, func(t *testing.T, got map[string]any, p app.Post) {
			if got["body"] != "new body" || got["format"] != "markdown" || got["edited_at"] == nil {
				t.Errorf("post = %v", got)
			}
		}},
		{"only the format", map[string]any{"format": "text"}, []string{"post.edited post"}, func(t *testing.T, got map[string]any, p app.Post) {
			if got["body"] != "**old** body" || got["format"] != "text" || strings.Contains(got["html"].(string), "<strong>") {
				t.Errorf("post = %v", got)
			}
		}},
		{"tags replace the tags", map[string]any{"tags": []string{"three"}}, []string{"post.edited post"}, func(t *testing.T, got map[string]any, p app.Post) {
			if !reflect.DeepEqual(got["tags"], []any{"three"}) {
				t.Errorf("tags = %v", got["tags"])
			}
		}},
		{"an empty list of tags removes them", map[string]any{"tags": []string{}}, []string{"post.edited post"}, func(t *testing.T, got map[string]any, p app.Post) {
			if len(got["tags"].([]any)) != 0 {
				t.Errorf("tags = %v", got["tags"])
			}
		}},
		{"the same tags", map[string]any{"tags": []string{"two", "one"}}, nil, func(t *testing.T, got map[string]any, p app.Post) {
			if got["edited_at"] != nil {
				t.Errorf("edited_at = %v: nothing changed", got["edited_at"])
			}
		}},
		{"an empty list of tasks deletes them", map[string]any{"tasks": []map[string]any{}}, []string{"post.edited post", "task.deleted task", "task.deleted task"}, func(t *testing.T, got map[string]any, p app.Post) {
			if len(got["tasks"].([]any)) != 0 {
				t.Errorf("tasks = %v", got["tasks"])
			}
		}},
		{"a task of the list is edited, the others go", map[string]any{"tasks": []map[string]any{{"id": "FIRST", "title": "renamed"}}}, []string{"post.edited post", "task.deleted task", "task.edited task"}, func(t *testing.T, got map[string]any, p app.Post) {
			task := got["tasks"].([]any)[0].(map[string]any)
			if !reflect.DeepEqual(titles(got), []string{"renamed"}) || len(task["assignees"].([]any)) != 0 || task["due_date"] != nil {
				t.Errorf("tasks = %v: the task is exactly what was sent", got["tasks"])
			}
		}},
		{"a new task is added to the old ones", map[string]any{"tasks": []map[string]any{{"id": "FIRST", "title": "first", "assignee_ids": []string{"ADMIN"}, "due_date": "2026-11-01"}, {"id": "SECOND", "title": "second"}, {"title": "third"}}}, []string{"post.edited post", "task.created task"}, func(t *testing.T, got map[string]any, p app.Post) {
			if !reflect.DeepEqual(titles(got), []string{"first", "second", "third"}) {
				t.Errorf("tasks = %v", got["tasks"])
			}
		}},
		{"an empty list of attachment IDs removes the files", map[string]any{"attachment_ids": []uint64{}}, []string{"post.edited post"}, func(t *testing.T, got map[string]any, p app.Post) {
			if len(got["attachments"].([]any)) != 0 {
				t.Errorf("attachments = %v", got["attachments"])
			}
		}},
		{"the attachment IDs it has", map[string]any{"attachment_ids": []string{"ATTACHMENT"}}, nil, func(t *testing.T, got map[string]any, p app.Post) {
			if len(got["attachments"].([]any)) != 1 {
				t.Errorf("attachments = %v", got["attachments"])
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			p := f.richPost()
			// The kept parts, to be compared after.
			wantBody, wantTags := p.Body, p.Tags
			body := tt.body
			if m, ok := body.(map[string]any); ok {
				body = substitute(m, map[string]any{
					"FIRST": p.Tasks[0].ID, "SECOND": p.Tasks[1].ID, "ADMIN": f.userID("admin"), "ATTACHMENT": p.Attachments[0].ID,
				})
			}
			f.clk.Advance(time.Hour)
			before := len(f.audit())
			got := f.ok("bob", http.MethodPatch, path("/posts/%d", p.ID), body, http.StatusOK)
			f.requireAudit(before, tt.audit...)
			tt.check(t, got, p)
			// What the request does not name stays as it was.
			named := func(key string) bool {
				m, ok := tt.body.(map[string]any)
				return ok && m[key] != nil
			}
			if !named("body") && got["body"] != wantBody {
				t.Errorf("body = %v, want it kept", got["body"])
			}
			if !named("tags") && len(got["tags"].([]any)) != len(wantTags) {
				t.Errorf("tags = %v, want them kept", got["tags"])
			}
			if !named("tasks") && len(got["tasks"].([]any)) != 2 {
				t.Errorf("tasks = %v, want them kept", got["tasks"])
			}
			if !named("attachment_ids") && len(got["attachments"].([]any)) != 1 {
				t.Errorf("attachments = %v, want them kept", got["attachments"])
			}
			// The kept task keeps its assignee and due date.
			if !named("tasks") {
				first := got["tasks"].([]any)[0].(map[string]any)
				if first["due_date"] != "2026-11-01" || len(first["assignees"].([]any)) != 1 {
					t.Errorf("first task = %v", first)
				}
			}
			again := f.ok("carol", http.MethodGet, path("/posts/%d", p.ID), nil, http.StatusOK)
			if again["body"] != got["body"] || again["format"] != got["format"] {
				t.Errorf("GET = %v, PATCH = %v", again, got)
			}
		})
	}
}

// substitute returns a copy of the JSON value with every string that is a key
// of with replaced by its value, at any depth.
func substitute(v map[string]any, with map[string]any) map[string]any {
	var walk func(x any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case string:
			if r, ok := with[t]; ok {
				return r
			}
			return t
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, e := range t {
				out[k] = walk(e)
			}
			return out
		case []map[string]any:
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = walk(e)
			}
			return out
		case []string:
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = walk(e)
			}
			return out
		case []uint64:
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = e
			}
			return out
		}
		return x
	}
	return walk(v).(map[string]any)
}

func TestPatchPostPermissions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		user, room string
		want       int
	}{
		{"bob", "lobby", http.StatusOK},
		{"admin", "lobby", http.StatusForbidden}, {"alice", "lobby", http.StatusForbidden}, {"carol", "lobby", http.StatusForbidden}, {"root", "lobby", http.StatusForbidden},
		{"bob", "den", http.StatusOK}, {"alice", "den", http.StatusForbidden},
		{"admin", "den", http.StatusNotFound}, {"carol", "den", http.StatusNotFound}, {"root", "den", http.StatusNotFound},
	}
	for _, tt := range tests {
		for _, kind := range []string{"post", "comment"} {
			t.Run(kind+": "+tt.user+" on "+tt.room, func(t *testing.T) {
				t.Parallel()
				f := newForum(t)
				p := f.write("bob", tt.room, "bob's post")
				id := p.ID
				if kind == "comment" {
					id = f.say("bob", p.ID, "bob's comment").ID
				}
				before := len(f.audit())
				rec := f.call(tt.user, http.MethodPatch, path("/posts/%d", id), map[string]any{"body": "edited"})
				requireStatus(t, rec, tt.want)
				if tt.want == http.StatusOK {
					want := "post.edited " + kind
					f.requireAudit(before, want)
					if got := decode(t, rec); got["body"] != "edited" || got["edited_at"] != "2026-10-26T09:30:00Z" {
						t.Errorf("post = %v", got)
					}
				} else {
					f.requireAudit(before)
				}
			})
		}
	}
	t.Run("a post that does not exist", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		requireStatus(t, f.call("bob", http.MethodPatch, "/api/v1/posts/999999", map[string]any{"body": "x"}), http.StatusNotFound)
	})
	t.Run("a deleted post", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "going")
		f.say("admin", p.ID, "keeps it")
		requireStatus(t, f.call("bob", http.MethodDelete, path("/posts/%d", p.ID), nil), http.StatusNoContent)
		requireStatus(t, f.call("bob", http.MethodPatch, path("/posts/%d", p.ID), map[string]any{"body": "back"}), http.StatusNotFound)
	})
}

func TestPatchPostMistakes(t *testing.T) {
	t.Parallel()
	for _, tt := range postMistakes {
		if tt.name == "nothing" || tt.name == "a task with an ID on a new post" {
			continue // an edit keeps the body, and a task ID is fine on an edit
		}
		want := tt.field
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			p := f.write("bob", "lobby", "bob's post")
			before := len(f.audit())
			rec := f.call("bob", http.MethodPatch, path("/posts/%d", p.ID), tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			fields := errorFields(t, rec)
			if tt.name == "attachment IDs to keep" {
				// On an edit the attachments are the ones to keep; the post has none.
				want = "attachment_ids"
			}
			if len(fields) != 1 || fields[want] == nil {
				t.Errorf("fields = %v, want only %q", fields, want)
			}
			f.requireAudit(before)
		})
	}
	t.Run("attachment IDs that are not the post's", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		mine := f.richPost()
		other := f.richPost()
		rec := f.call("bob", http.MethodPatch, path("/posts/%d", mine.ID), map[string]any{"attachment_ids": []uint64{other.Attachments[0].ID}})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if fields := errorFields(t, rec); fields["attachment_ids"] == nil || len(fields) != 1 {
			t.Errorf("fields = %v", fields)
		}
		if got := f.ok("bob", http.MethodGet, path("/posts/%d", mine.ID), nil, http.StatusOK); len(got["attachments"].([]any)) != 1 {
			t.Errorf("post = %v: a failed edit changes nothing", got)
		}
	})
	t.Run("a post emptied of everything", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "bob's post")
		rec := f.call("bob", http.MethodPatch, path("/posts/%d", p.ID), map[string]any{"body": ""})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["body"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})
	t.Run("a task of another post", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		mine := f.richPost()
		other := f.richPost()
		rec := f.call("bob", http.MethodPatch, path("/posts/%d", mine.ID), map[string]any{"tasks": []map[string]any{{"id": other.Tasks[0].ID, "title": "stolen"}}})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["tasks"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})
}

func TestAttachmentsOfAPost(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	p := f.richPost()
	got := f.ok("carol", http.MethodGet, path("/posts/%d", p.ID), nil, http.StatusOK)
	atts := list(t, got["attachments"])
	if len(atts) != 1 {
		t.Fatalf("attachments = %v", atts)
	}
	want := map[string]any{
		"id": float64(p.Attachments[0].ID), "file_name": "note.txt", "content_type": "text/plain; charset=utf-8", "size": float64(17),
		"width": nil, "height": nil, "url": fmt.Sprintf("/api/v1/attachments/%d", p.Attachments[0].ID),
	}
	if !reflect.DeepEqual(atts[0], want) {
		t.Errorf("attachment = %v\nwant %v", atts[0], want)
	}
}

func TestDeletePost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		user, room string
		want       int
	}{
		{"bob", "lobby", http.StatusNoContent}, {"admin", "lobby", http.StatusNoContent}, {"root", "lobby", http.StatusNoContent},
		{"alice", "lobby", http.StatusForbidden}, {"carol", "lobby", http.StatusForbidden},
		{"bob", "den", http.StatusNoContent}, {"alice", "den", http.StatusForbidden},
		{"admin", "den", http.StatusNotFound}, {"root", "den", http.StatusNotFound}, {"carol", "den", http.StatusNotFound},
	}
	for _, tt := range tests {
		for _, kind := range []string{"post", "comment"} {
			t.Run(kind+": "+tt.user+" on "+tt.room, func(t *testing.T) {
				t.Parallel()
				f := newForum(t)
				p := f.write("bob", tt.room, "bob's post")
				id := p.ID
				if kind == "comment" {
					id = f.say("bob", p.ID, "bob's comment").ID
				}
				before := len(f.audit())
				rec := f.call(tt.user, http.MethodDelete, path("/posts/%d", id), nil)
				requireStatus(t, rec, tt.want)
				if tt.want == http.StatusNoContent {
					f.requireAudit(before, "post.deleted "+kind)
					requireStatus(t, f.call("bob", http.MethodGet, path("/posts/%d", id), nil), http.StatusNotFound)
					requireStatus(t, f.call(tt.user, http.MethodDelete, path("/posts/%d", id), nil), http.StatusNotFound)
				} else {
					f.requireAudit(before)
				}
			})
		}
	}
	t.Run("a post with replies stays as deleted", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "the post")
		c := f.say("admin", p.ID, "a reply")
		before := len(f.audit())
		requireStatus(t, f.call("bob", http.MethodDelete, path("/posts/%d", p.ID), nil), http.StatusNoContent)
		f.requireAudit(before, "post.deleted post")
		if got := f.ok("bob", http.MethodGet, path("/posts/%d", p.ID), nil, http.StatusOK); got["deleted"] != true {
			t.Errorf("post = %v", got)
		}
		requireStatus(t, f.call("admin", http.MethodDelete, path("/posts/%d", c.ID), nil), http.StatusNoContent)
		// The last reply went, and the placeholder with it.
		requireStatus(t, f.call("bob", http.MethodGet, path("/posts/%d", p.ID), nil), http.StatusNotFound)
	})
	t.Run("the tasks leave the board with the post", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.richPost()
		before := len(f.audit())
		requireStatus(t, f.call("bob", http.MethodDelete, path("/posts/%d", p.ID), nil), http.StatusNoContent)
		f.requireAudit(before, "post.deleted post", "task.deleted task", "task.deleted task")
	})
}

func TestReactions(t *testing.T) {
	t.Parallel()
	const (
		thumbs = "\U0001F44D"
		flag   = "\U0001F1EC\U0001F1F7"
		family = "\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466"
		keycap = "1\uFE0F\u20E3"
		heart  = "❤\uFE0F"
		wave   = "\U0001F44B\U0001F3FD"
		scot   = "\U0001F3F4\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F"
	)
	for _, emoji := range []string{thumbs, flag, family, keycap, heart, wave, scot} {
		t.Run("the emoji "+url.PathEscape(emoji), func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			p := f.write("bob", "lobby", "react to me")
			target := path("/posts/%d/reactions/", p.ID) + url.PathEscape(emoji)
			got := f.ok("bob", http.MethodPut, target, nil, http.StatusOK)
			rs := list(t, got["reactions"])
			if len(rs) != 1 || obj(t, rs[0])["emoji"] != emoji || obj(t, rs[0])["count"] != float64(1) || obj(t, rs[0])["mine"] != true {
				t.Fatalf("reactions = %v", rs)
			}
			// Then the post shows it.
			post := f.ok("carol", http.MethodGet, path("/posts/%d", p.ID), nil, http.StatusOK)
			shown := obj(t, list(t, post["reactions"])[0])
			if shown["emoji"] != emoji || shown["mine"] != false {
				t.Errorf("reaction = %v", shown)
			}
			got = f.ok("bob", http.MethodDelete, target, nil, http.StatusOK)
			if len(list(t, got["reactions"])) != 0 {
				t.Errorf("after the removal: %v", got)
			}
		})
	}
	for _, tt := range []struct{ name, emoji string }{
		{"a letter", "a"}, {"a word", "ok"}, {"a digit", "1"}, {"two emoji", thumbs + thumbs}, {"a space", "%20"},
		{"an emoji and a letter", thumbs + "a"}, {"a lone regional indicator", "\U0001F1EC"}, {"markup", "<b>"},
		{"too long", strings.Repeat(thumbs, 9)},
	} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			t.Run(method+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				f := newForum(t)
				p := f.write("bob", "lobby", "react to me")
				emoji := tt.emoji
				if !strings.HasPrefix(emoji, "%") {
					emoji = url.PathEscape(emoji)
				}
				rec := f.call("bob", method, path("/posts/%d/reactions/", p.ID)+emoji, nil)
				requireStatus(t, rec, http.StatusUnprocessableEntity)
				if errorFields(t, rec)["emoji"] == nil {
					t.Errorf("fields = %v", errorFields(t, rec))
				}
			})
		}
	}
	t.Run("putting twice and taking off twice", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "react to me")
		target := path("/posts/%d/reactions/", p.ID) + url.PathEscape(thumbs)
		before := len(f.audit())
		f.ok("bob", http.MethodDelete, target, nil, http.StatusOK)
		f.ok("bob", http.MethodPut, target, nil, http.StatusOK)
		got := f.ok("bob", http.MethodPut, target, nil, http.StatusOK)
		if rs := list(t, got["reactions"]); len(rs) != 1 || obj(t, rs[0])["count"] != float64(1) {
			t.Errorf("reactions = %v", rs)
		}
		f.ok("bob", http.MethodDelete, target, nil, http.StatusOK)
		got = f.ok("bob", http.MethodDelete, target, nil, http.StatusOK)
		if rs := list(t, got["reactions"]); len(rs) != 0 {
			t.Errorf("reactions = %v", rs)
		}
		f.requireAudit(before) // reactions are not audited
	})
	t.Run("two people", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "den", "react to me")
		target := path("/posts/%d/reactions/", p.ID) + url.PathEscape(thumbs)
		f.ok("bob", http.MethodPut, target, nil, http.StatusOK)
		got := f.ok("alice", http.MethodPut, target, nil, http.StatusOK)
		r := obj(t, list(t, got["reactions"])[0])
		users := list(t, r["users"])
		if r["count"] != float64(2) || r["mine"] != true || len(users) != 2 ||
			obj(t, users[0])["username"] != "bob" || obj(t, users[1])["username"] != "alice" || obj(t, users[1])["display_name"] != "Alice" {
			t.Errorf("reaction = %v", r)
		}
		asBob := f.ok("bob", http.MethodGet, path("/posts/%d", p.ID), nil, http.StatusOK)
		if obj(t, list(t, asBob["reactions"])[0])["mine"] != true {
			t.Errorf("as bob: %v", asBob["reactions"])
		}
		got = f.ok("alice", http.MethodDelete, target, nil, http.StatusOK)
		r = obj(t, list(t, got["reactions"])[0])
		if r["count"] != float64(1) || r["mine"] != false {
			t.Errorf("after alice took hers off: %v", r)
		}
	})
	t.Run("on a comment", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "the post")
		c := f.say("admin", p.ID, "the comment")
		f.ok("bob", http.MethodPut, path("/posts/%d/reactions/", c.ID)+url.PathEscape(thumbs), nil, http.StatusOK)
		thread := f.ok("carol", http.MethodGet, path("/posts/%d/thread", p.ID), nil, http.StatusOK)
		comment := obj(t, obj(t, list(t, thread["comments"])[0])["comment"])
		if len(list(t, comment["reactions"])) != 1 || len(list(t, obj(t, thread["post"])["reactions"])) != 0 {
			t.Errorf("thread = %v", thread)
		}
	})
	t.Run("on a post that is deleted or missing", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "going")
		f.say("admin", p.ID, "keeps it")
		requireStatus(t, f.call("bob", http.MethodDelete, path("/posts/%d", p.ID), nil), http.StatusNoContent)
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			requireStatus(t, f.call("bob", method, path("/posts/%d/reactions/", p.ID)+url.PathEscape(thumbs), nil), http.StatusNotFound)
			requireStatus(t, f.call("bob", method, "/api/v1/posts/999999/reactions/"+url.PathEscape(thumbs), nil), http.StatusNotFound)
		}
	})
}

func TestReactionPermissions(t *testing.T) {
	t.Parallel()
	for _, tt := range postWriteStatuses {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			t.Run(method+" "+tt.user+" on "+tt.room, func(t *testing.T) {
				t.Parallel()
				f := newForum(t)
				p := f.write("bob", tt.room, "the post")
				want := tt.want
				if want == http.StatusCreated {
					want = http.StatusOK
				}
				requireStatus(t, f.call(tt.user, method, path("/posts/%d/reactions/", p.ID)+url.PathEscape("\U0001F44D"), nil), want)
			})
		}
	}
}

func TestTags(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.write("bob", "lobby", "one", "go", "golang")
	f.write("bob", "den", "two", "gopher", "rust")
	tests := []struct {
		user, query string
		want        []string
	}{
		{"carol", "?prefix=go", []string{"go", "golang"}},
		{"alice", "?prefix=go", []string{"go", "golang", "gopher"}},
		{"bob", "?prefix=%23GO", []string{"go", "golang", "gopher"}},
		{"alice", "?prefix=gop", []string{"gopher"}},
		{"carol", "?prefix=gop", []string{}},
		{"alice", "?prefix=zzz", []string{}},
		{"alice", "", []string{"go", "golang", "gopher", "rust"}},
		{"admin", "?prefix=r", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.user+tt.query, func(t *testing.T) {
			body := f.ok(tt.user, http.MethodGet, "/api/v1/tags"+tt.query, nil, http.StatusOK)
			got := []string{}
			for _, tag := range list(t, body["tags"]) {
				got = append(got, tag.(string))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tags = %v, want %v", got, tt.want)
			}
		})
	}
	t.Run("at most ten", func(t *testing.T) {
		g := newForum(t)
		var names []string
		for i := 0; i < 12; i++ {
			names = append(names, fmt.Sprintf("tag%02d", i))
		}
		g.write("bob", "lobby", "many", names[:10]...)
		g.write("bob", "lobby", "more", names[10:]...)
		body := g.ok("carol", http.MethodGet, "/api/v1/tags?prefix=tag", nil, http.StatusOK)
		if got := list(t, body["tags"]); len(got) != 10 || got[0] != "tag00" {
			t.Errorf("tags = %v", got)
		}
	})
}

// TestAuditThroughTheAPIForPosts is the check, for every audited action of a
// post, a comment and a task, that the call through the API writes its event
// and a call that fails writes none. The events of each action are in the
// tests above as well.
func TestAuditThroughTheAPIForPosts(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	step := func(user, method, p string, body any, want int, events ...string) map[string]any {
		t.Helper()
		before := len(f.audit())
		rec := f.call(user, method, p, body)
		requireStatus(t, rec, want)
		f.requireAudit(before, events...)
		if rec.Body.Len() == 0 {
			return nil
		}
		return decode(t, rec)
	}
	post := step("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "hi", "tasks": []map[string]any{{"title": "t"}}},
		http.StatusCreated, "post.created post", "task.created task")
	id := uint64(post["id"].(float64))
	comment := step("admin", http.MethodPost, path("/posts/%d/comments", id), map[string]any{"body": "re"}, http.StatusCreated, "post.created comment")
	cid := uint64(comment["id"].(float64))
	step("carol", http.MethodPost, path("/posts/%d/comments", id), map[string]any{"body": "no"}, http.StatusForbidden)
	step("bob", http.MethodPatch, path("/posts/%d", id), map[string]any{"body": "hi again"}, http.StatusOK, "post.edited post")
	step("admin", http.MethodPatch, path("/posts/%d", cid), map[string]any{"body": "re again"}, http.StatusOK, "post.edited comment")
	step("admin", http.MethodPatch, path("/posts/%d", id), map[string]any{"body": "mine now"}, http.StatusForbidden)
	step("bob", http.MethodPut, path("/posts/%d/reactions/", id)+url.PathEscape("\U0001F44D"), nil, http.StatusOK)
	step("admin", http.MethodDelete, path("/posts/%d", cid), nil, http.StatusNoContent, "post.deleted comment")
	step("bob", http.MethodDelete, path("/posts/%d", id), nil, http.StatusNoContent, "post.deleted post", "task.deleted task")
}
