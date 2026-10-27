package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// wildcard matches a {name} of a route pattern.
var wildcard = regexp.MustCompile(`\{[^}]+\}`)

// TestIDsAboveTheDatabaseRangeAreNotFoundOnThePages sends every route of the
// pages an ID above the range of the databases (a signed 64-bit column), in
// its path, its query and its form, as an admin. The review of step 17.1
// found that such an ID, which the page parsed as a number, reached the
// driver and was a 500 page with an error in the log; it has to be the 404
// page, or be ignored where the value is only a cursor.
func TestIDsAboveTheDatabaseRangeAreNotFoundOnThePages(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	for _, huge := range []string{"9223372036854775808", "18446744073709551615"} {
		for _, pattern := range e.srv.patterns {
			method, path, _ := strings.Cut(pattern, " ")
			if path == "/{$}" || path == "/me/stream" || path == "/logout" || strings.HasPrefix(path, "/static/") {
				continue
			}
			path = strings.NewReplacer("{name}", "a.txt", "{token}", "x").Replace(path)
			path = wildcard.ReplaceAllString(path, huge)
			query := "?before=" + huge + "&seen=" + huge + "&page=" + huge + "&members=" + huge + "&room_id=" + huge + "&actor_id=" + huge
			form := url.Values{"user": {huge}, "column": {huge}, "position": {"1"}, "name": {"x"}, "title": {"x"}, "emoji": {"👍"},
				"up_to": {huge}, "reply_to": {huge}, "users": {"alice"}}
			var r *http.Request
			switch method {
			case http.MethodGet:
				r = get(path+query, e.cookies["admin"])
			default:
				r = postForm(path, form, e.cookies["admin"])
			}
			done := make(chan int, 1)
			go func() { done <- e.send(r).Code }()
			select {
			case code := <-done:
				if code >= 500 {
					t.Errorf("%s %s -> %d", method, path, code)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("%s %s did not answer", method, path)
			}
		}
	}
	// The requests did not end the session of the admin (they would have been
	// sent to the login page, and the test would have proved nothing).
	if rec := e.send(get("/rooms", e.cookies["admin"])); rec.Code != http.StatusOK {
		t.Errorf("the admin is not logged in any more: status %d", rec.Code)
	}
	for _, le := range logEntries(t, e.log) {
		if le.Level == "ERROR" {
			t.Errorf("a failure was logged: %+v", le)
		}
	}
}
