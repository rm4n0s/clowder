package api

import (
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"testing"
)

// searchMessages sends GET /messages/search as the user.
func (f *forum) searchMessages(user, query string, extra ...string) map[string]any {
	f.t.Helper()
	target := "/api/v1/messages/search?q=" + url.QueryEscape(query)
	for _, e := range extra {
		target += "&" + e
	}
	return f.ok(user, http.MethodGet, target, nil, http.StatusOK)
}

// foundMessageIDs returns the message IDs of the results of an answer.
func foundMessageIDs(t *testing.T, got map[string]any) []uint64 {
	t.Helper()
	ids := []uint64{}
	for _, r := range list(t, got["results"]) {
		ids = append(ids, uint64(obj(t, r)["message_id"].(float64)))
	}
	return ids
}

func TestSearchMessages(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	direct := f.convs("alice", "bob")
	crew := f.convs("alice", "bob", "carol")
	first := idOf(t, f.msg("bob", direct, "the <b>lighthouse</b> plan"))
	second := idOf(t, f.msg("alice", crew, "lighthouse crew call"))
	other := f.convs("carol", "root")
	third := idOf(t, f.msg("root", other, "lighthouse supper"))

	t.Run("results, newest first, with the conversation and the matches", func(t *testing.T) {
		got := f.searchMessages("alice", "lighthouse")
		if ids := foundMessageIDs(t, got); !reflect.DeepEqual(ids, []uint64{second, first}) {
			t.Fatalf("found %v, want %v", ids, []uint64{second, first})
		}
		if got["next_before"] != nil {
			t.Errorf("next_before = %v, want null", got["next_before"])
		}
		results := list(t, got["results"])
		top := obj(t, results[0])
		conv := obj(t, top["conversation"])
		if idOf(t, conv) != crew || conv["kind"] != "group" || conv["title"] != "Bob, Carol" {
			t.Errorf("conversation = %v", conv)
		}
		if obj(t, top["author"])["username"] != "alice" || top["created_at"] == "" {
			t.Errorf("result = %v", top)
		}
		if got := segmentsOf(t, top); got != "[lighthouse] crew call" {
			t.Errorf("snippet = %q", got)
		}
		if got := segmentsOf(t, results[1]); got != "the <b>[lighthouse]</b> plan" {
			t.Errorf("snippet of the second = %q (text is plain, not escaped)", got)
		}
		if conv := obj(t, obj(t, results[1])["conversation"]); idOf(t, conv) != direct || conv["title"] != "Bob" || conv["kind"] != "direct" {
			t.Errorf("conversation = %v", conv)
		}
	})

	t.Run("who finds what", func(t *testing.T) {
		for _, tt := range []struct {
			user string
			want []uint64
		}{
			{"alice", []uint64{second, first}},
			{"bob", []uint64{second, first}},
			{"carol", []uint64{third, second}},
			{"root", []uint64{third}},
			{"admin", []uint64{}}, // an admin who is in no conversation
		} {
			if got := foundMessageIDs(t, f.searchMessages(tt.user, "lighthouse")); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s found %v, want %v", tt.user, got, tt.want)
			}
		}
	})

	t.Run("a member who left finds nothing of the conversation", func(t *testing.T) {
		requireStatus(t, f.call("carol", http.MethodPost, path("/conversations/%d/leave", crew), nil), http.StatusNoContent)
		if got := foundMessageIDs(t, f.searchMessages("carol", "lighthouse")); !reflect.DeepEqual(got, []uint64{third}) {
			t.Errorf("carol found %v, want only %d", got, third)
		}
	})

	t.Run("an edit and a deletion", func(t *testing.T) {
		requireStatus(t, f.call("bob", http.MethodPatch, path("/messages/%d", first), map[string]any{"body": "the beacon plan"}), http.StatusOK)
		if got := foundMessageIDs(t, f.searchMessages("alice", "lighthouse")); !reflect.DeepEqual(got, []uint64{second}) {
			t.Errorf("after the edit alice found %v", got)
		}
		if got := foundMessageIDs(t, f.searchMessages("alice", "beacon")); !reflect.DeepEqual(got, []uint64{first}) {
			t.Errorf("the new word found %v", got)
		}
		requireStatus(t, f.call("bob", http.MethodDelete, path("/messages/%d", first), nil), http.StatusNoContent)
		if got := foundMessageIDs(t, f.searchMessages("alice", "beacon")); len(got) != 0 {
			t.Errorf("a deleted message was found: %v", got)
		}
	})

	t.Run("the posts search does not find a message", func(t *testing.T) {
		if got := resultIDs(t, f.search("alice", "lighthouse")); len(got) != 0 {
			t.Errorf("the search of posts found %v", got)
		}
	})
}

func TestSearchMessagesPaging(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	conv := f.convs("alice", "bob")
	var ids []uint64
	for range 31 {
		ids = append(ids, idOf(t, f.msg("bob", conv, "pager line")))
	}
	first := f.searchMessages("alice", "pager")
	if got := foundMessageIDs(t, first); len(got) != 30 || got[0] != ids[30] || got[29] != ids[1] {
		t.Fatalf("first page = %v", got)
	}
	if first["next_before"] != float64(ids[1]) {
		t.Fatalf("next_before = %v, want %d", first["next_before"], ids[1])
	}
	second := f.searchMessages("alice", "pager", "before="+strconv.FormatUint(ids[1], 10))
	if got := foundMessageIDs(t, second); !reflect.DeepEqual(got, []uint64{ids[0]}) || second["next_before"] != nil {
		t.Errorf("second page = %v, next_before %v", got, second["next_before"])
	}
}

// TestSearchMessagesMistakes checks the queries that are refused, and a
// malformed before.
func TestSearchMessagesMistakes(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	for _, q := range []string{"", "   ", "!!", "#tag", "room:lobby from:bob", "zebra #tag", "zebra room:lobby", "zebra after:2026-10-01", "zebra after:soon"} {
		rec := f.call("alice", http.MethodGet, "/api/v1/messages/search?q="+url.QueryEscape(q), nil)
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["q"] == nil {
			t.Errorf("%q: fields = %v", q, errorFields(t, rec))
		}
	}
	rec := f.call("alice", http.MethodGet, "/api/v1/messages/search", nil)
	requireStatus(t, rec, http.StatusUnprocessableEntity)
	for _, before := range []string{"0", "-1", "abc", "", "1.5", "99999999999999999999"} {
		rec := f.call("alice", http.MethodGet, "/api/v1/messages/search?q=zebra&before="+before, nil)
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["before"] == nil {
			t.Errorf("before=%q: fields = %v", before, errorFields(t, rec))
		}
	}
}
