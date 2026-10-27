package api

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// search sends GET /search as the user.
func (f *forum) search(user, query string, extra ...string) map[string]any {
	f.t.Helper()
	target := "/api/v1/search?q=" + url.QueryEscape(query)
	for _, e := range extra {
		target += "&" + e
	}
	return f.ok(user, http.MethodGet, target, nil, http.StatusOK)
}

// resultIDs returns the post IDs of the results of an answer.
func resultIDs(t *testing.T, got map[string]any) []uint64 {
	t.Helper()
	ids := []uint64{}
	for _, r := range list(t, got["results"]) {
		ids = append(ids, uint64(obj(t, r)["post_id"].(float64)))
	}
	return ids
}

// segments reads the snippet of a result as "text" and "[match]" pieces.
func segmentsOf(t *testing.T, result any) string {
	t.Helper()
	var b strings.Builder
	for _, s := range list(t, obj(t, result)["snippet"]) {
		seg := obj(t, s)
		if seg["match"] == true {
			b.WriteString("[" + seg["text"].(string) + "]")
		} else {
			b.WriteString(seg["text"].(string))
		}
	}
	return b.String()
}

func TestSearchResults(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	post := f.write("bob", "lobby", "Lunch at the Café Münster", "food", "plan")
	f.clk.Advance(time.Minute)
	comment := f.say("admin", post.ID, "Café sounds good")
	f.clk.Advance(time.Minute)
	script := f.write("bob", "lobby", "Hello <script>alert('x')</script> & \"quotes\" world")

	t.Run("a post", func(t *testing.T) {
		got := f.search("carol", "lunch")
		if got["page"] != float64(1) || got["has_more"] != false {
			t.Errorf("answer = %v", got)
		}
		results := list(t, got["results"])
		if len(results) != 1 {
			t.Fatalf("results = %v", results)
		}
		r := obj(t, results[0])
		if r["post_id"] != float64(post.ID) || r["root_id"] != float64(post.ID) || r["kind"] != "post" || r["created_at"] != rfc3339(post.CreatedAt) {
			t.Errorf("result = %v", r)
		}
		if room := obj(t, r["room"]); room["id"] != float64(f.lobby.ID) || room["name"] != "lobby" {
			t.Errorf("room = %v", room)
		}
		if author := obj(t, r["author"]); author["username"] != "bob" || author["id"] != float64(f.userID("bob")) {
			t.Errorf("author = %v", author)
		}
		if got := segmentsOf(t, r); got != "[Lunch] at the Café Münster" {
			t.Errorf("snippet = %q", got)
		}
	})
	t.Run("a comment", func(t *testing.T) {
		got := f.search("carol", "sounds")
		results := list(t, got["results"])
		if len(results) != 1 {
			t.Fatalf("results = %v", results)
		}
		r := obj(t, results[0])
		if r["post_id"] != float64(comment.ID) || r["root_id"] != float64(post.ID) || r["kind"] != "comment" || obj(t, r["author"])["username"] != "admin" {
			t.Errorf("result = %v", r)
		}
		if got := segmentsOf(t, r); got != "Café [sounds] good" {
			t.Errorf("snippet = %q", got)
		}
	})
	t.Run("diacritics are folded and every word is a match", func(t *testing.T) {
		got := f.search("carol", "cafe munster")
		results := list(t, got["results"])
		if ids := resultIDs(t, got); !reflect.DeepEqual(ids, []uint64{post.ID}) {
			t.Fatalf("ids = %v, want only the post that has both words", ids)
		}
		if got := segmentsOf(t, results[0]); got != "Lunch at the [Café] [Münster]" {
			t.Errorf("snippet = %q", got)
		}
		// One word is in the post and in the comment.
		if ids := resultIDs(t, f.search("carol", "CAFÉ")); len(ids) != 2 {
			t.Errorf("ids = %v", ids)
		}
	})
	t.Run("the segments hold the text as written, not escaped", func(t *testing.T) {
		got := f.search("carol", "world")
		results := list(t, got["results"])
		if len(results) != 1 || obj(t, results[0])["post_id"] != float64(script.ID) {
			t.Fatalf("results = %v", results)
		}
		if want := "Hello <script>alert('x')</script> & \"quotes\" [world]"; segmentsOf(t, results[0]) != want {
			t.Errorf("snippet = %q, want %q", segmentsOf(t, results[0]), want)
		}
	})
	t.Run("filters", func(t *testing.T) {
		tests := []struct {
			q    string
			want []uint64
		}{
			{"#food", []uint64{post.ID}},
			{"#food #plan", []uint64{post.ID}},
			{"#food #nothing", []uint64{}},
			{"from:admin", []uint64{comment.ID}},
			{"from:bob lunch", []uint64{post.ID}},
			{"is:comment", []uint64{comment.ID}},
			{"is:post cafe", []uint64{post.ID}},
			{"room:lobby from:bob", []uint64{script.ID, post.ID}},
			{"room:den", []uint64{}},
			{"after:2026-10-27", []uint64{}},
			{"before:2026-10-27 from:bob", []uint64{script.ID, post.ID}},
		}
		for _, tt := range tests {
			if got := resultIDs(t, f.search("carol", tt.q)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%q found %v, want %v", tt.q, got, tt.want)
			}
		}
	})
	t.Run("no query finds nothing", func(t *testing.T) {
		for _, target := range []string{"/api/v1/search", "/api/v1/search?q=", "/api/v1/search?q=+++", "/api/v1/search?q=%21%21"} {
			got := f.ok("carol", http.MethodGet, target, nil, http.StatusOK)
			if len(list(t, got["results"])) != 0 || got["has_more"] != false || got["page"] != float64(1) {
				t.Errorf("%s: %v", target, got)
			}
		}
	})
}

// TestSearchPermissions checks that a search finds only what its user reads:
// the posts of a private room only for its members, admins included.
func TestSearchPermissions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		user  string
		finds map[string]bool // by room
	}{
		{"bob", map[string]bool{"lobby": true, "den": true}},
		{"alice", map[string]bool{"lobby": true, "den": true}},
		{"admin", map[string]bool{"lobby": true, "den": false}},
		{"root", map[string]bool{"lobby": true, "den": false}},
		{"carol", map[string]bool{"lobby": true, "den": false}},
	} {
		t.Run(tt.user, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			lobby := f.write("bob", "lobby", "needle in the lobby")
			den := f.write("bob", "den", "needle in the den")
			f.say("alice", den.ID, "needle comment in the den")
			got := resultIDs(t, f.search(tt.user, "needle"))
			wantLobby, wantDen := tt.finds["lobby"], tt.finds["den"]
			if hasID(got, lobby.ID) != wantLobby || hasID(got, den.ID) != wantDen {
				t.Errorf("%s found %v (lobby %d, den %d)", tt.user, got, lobby.ID, den.ID)
			}
			if !wantDen && len(got) != 1 {
				t.Errorf("found %v, want only the lobby post", got)
			}
			// A filter on the private room finds nothing for an outsider, and says nothing about it.
			byRoom := resultIDs(t, f.search(tt.user, "room:den needle"))
			if hasID(byRoom, den.ID) != wantDen {
				t.Errorf("room:den found %v", byRoom)
			}
		})
	}
	t.Run("a post deleted is not found", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "needle")
		requireStatus(t, f.call("bob", http.MethodDelete, path("/posts/%d", p.ID), nil), http.StatusNoContent)
		if got := resultIDs(t, f.search("carol", "needle")); len(got) != 0 {
			t.Errorf("found %v", got)
		}
	})
}

func hasID(ids []uint64, id uint64) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}

func TestSearchPaging(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	var made []uint64
	for i := 0; i < 25; i++ {
		made = append(made, f.write("bob", "lobby", fmt.Sprintf("zebra number %d", i)).ID)
		f.clk.Advance(time.Minute)
	}
	// Equal relevance: newest first.
	newest := make([]uint64, len(made))
	for i, id := range made {
		newest[len(made)-1-i] = id
	}
	first := f.search("carol", "zebra")
	if got := resultIDs(t, first); !reflect.DeepEqual(got, newest[:20]) || first["has_more"] != true || first["page"] != float64(1) {
		t.Errorf("page 1 = %v, has_more %v", got, first["has_more"])
	}
	second := f.search("carol", "zebra", "page=2")
	if got := resultIDs(t, second); !reflect.DeepEqual(got, newest[20:]) || second["has_more"] != false || second["page"] != float64(2) {
		t.Errorf("page 2 = %v, has_more %v", got, second["has_more"])
	}
	third := f.search("carol", "zebra", "page=3")
	if len(list(t, third["results"])) != 0 || third["has_more"] != false || third["page"] != float64(3) {
		t.Errorf("page 3 = %v", third)
	}
	t.Run("a page after the 1000th is empty", func(t *testing.T) {
		for _, page := range []string{"1001", "99999", "2147483647"} {
			got := f.search("carol", "zebra", "page="+page)
			if len(list(t, got["results"])) != 0 || got["has_more"] != false {
				t.Errorf("page %s = %v", page, got)
			}
		}
	})
	for _, query := range []string{"page=0", "page=-1", "page=abc", "page=", "page=1.5", "page=99999999999999999999"} {
		t.Run(query, func(t *testing.T) {
			rec := f.call("carol", http.MethodGet, "/api/v1/search?q=zebra&"+query, nil)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)["page"] == nil {
				t.Errorf("fields = %v", errorFields(t, rec))
			}
		})
	}
}

func TestSearchQueryMistakes(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	for _, q := range []string{"after:yesterday", "before:2026-13-45", "zebra after:soon"} {
		rec := f.call("carol", http.MethodGet, "/api/v1/search?q="+url.QueryEscape(q), nil)
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["q"] == nil {
			t.Errorf("%q: fields = %v", q, errorFields(t, rec))
		}
	}
}

// TestSearchSnippetOfALongPost checks that the segments of a search without
// words are the start of the post and mark nothing.
func TestSearchSnippetOfALongPost(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.write("bob", "lobby", strings.Repeat("long ", 80))
	got := f.search("carol", "from:bob")
	results := list(t, got["results"])
	if len(results) != 1 {
		t.Fatalf("results = %v", results)
	}
	var text string
	for _, s := range list(t, obj(t, results[0])["snippet"]) {
		seg := obj(t, s)
		if seg["match"] != false {
			t.Errorf("segment %v is a match", seg)
		}
		text += seg["text"].(string)
	}
	if n := len([]rune(text)); n == 0 || n > 205 {
		t.Errorf("the snippet has %d characters", n)
	}
}
