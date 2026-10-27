package api

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/store"
)

// The IDs of the tests below: the databases keep an ID in a signed 64-bit
// column, so a number above the largest one fails in the driver. It is what
// the review of step 17.1 found: such an ID was a 500 and an error in the log
// where it has to be "not found" (or "not valid") and nothing else.
const (
	idAboveInt64   = "9223372036854775808"
	idAboveUint64  = "18446744073709551616"
	idLargestUint  = "18446744073709551615"
	idLargestInt64 = "9223372036854775807"
)

// TestIDsAboveTheDatabaseRangeNeverFail sends every route that takes an ID in
// its path, its query or its body an ID above the range of the database. The
// answer must be a client error, and nothing may be logged as a failure.
func TestIDsAboveTheDatabaseRangeNeverFail(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleAdmin)
	e.addUser("bob", store.RoleMember)
	token := e.token("alice")
	create := func(method, path string, body any) map[string]any {
		t.Helper()
		rec := e.send(request(method, path, body, bearer(token)))
		if rec.Code >= 300 {
			t.Fatalf("%s %s: status %d: %s", method, path, rec.Code, rec.Body)
		}
		return decode(t, rec)
	}
	idOf := func(m map[string]any) string { return strconv.FormatUint(uint64(m["id"].(float64)), 10) }
	room := idOf(create(http.MethodPost, "/api/v1/rooms", map[string]any{"name": "den", "visibility": "visible"}))
	post := idOf(create(http.MethodPost, "/api/v1/rooms/"+room+"/posts", map[string]any{"body": "hello"}))
	conv := idOf(create(http.MethodPost, "/api/v1/conversations", map[string]any{"usernames": []string{"bob"}}))
	msg := idOf(create(http.MethodPost, "/api/v1/conversations/"+conv+"/messages", map[string]any{"body": "hi"}))

	for _, huge := range []string{idAboveInt64, idLargestUint, idAboveUint64} {
		requests := []struct {
			name, method, path string
			body               any
		}{
			{"a room", "GET", "/api/v1/rooms/" + huge, nil},
			{"the candidates of a room", "GET", "/api/v1/rooms/" + huge + "/candidates?q=a", nil},
			{"a member to remove", "DELETE", "/api/v1/rooms/" + room + "/members/" + huge, nil},
			{"a member to add", "POST", "/api/v1/rooms/" + room + "/members", map[string]any{"user_id": rawNumber(huge)}},
			{"a post", "GET", "/api/v1/posts/" + huge, nil},
			{"the feed before", "GET", "/api/v1/rooms/" + room + "/posts?before=" + huge, nil},
			{"a session to end", "DELETE", "/api/v1/me/sessions/" + huge, nil},
			{"a column", "POST", "/api/v1/columns/" + huge + "/move", map[string]any{"position": 1}},
			{"the column of a card", "POST", "/api/v1/tasks/1/move", map[string]any{"column_id": rawNumber(huge), "position": 1}},
			{"an assignee of a card", "PATCH", "/api/v1/tasks/1", map[string]any{"assignee_ids": []any{rawNumber(huge)}}},
			{"an assignee of a new task", "POST", "/api/v1/rooms/" + room + "/posts",
				map[string]any{"body": "x", "tasks": []any{map[string]any{"title": "t", "assignee_ids": []any{rawNumber(huge)}}}}},
			{"a task of an edit", "PATCH", "/api/v1/posts/" + post, map[string]any{"tasks": []any{map[string]any{"id": rawNumber(huge), "title": "t"}}}},
			{"an attachment to keep", "PATCH", "/api/v1/posts/" + post, map[string]any{"attachment_ids": []any{rawNumber(huge)}}},
			{"the notifications before", "GET", "/api/v1/notifications?before=" + huge, nil},
			{"the notifications to read", "POST", "/api/v1/notifications/read", map[string]any{"up_to_id": rawNumber(huge)}},
			{"the people of a conversation", "POST", "/api/v1/conversations", map[string]any{"user_ids": []any{rawNumber(huge)}}},
			{"a person to add", "POST", "/api/v1/conversations/" + conv + "/members", map[string]any{"user_ids": []any{rawNumber(huge)}}},
			{"the messages before", "GET", "/api/v1/conversations/" + conv + "/messages?before=" + huge, nil},
			{"a message to reply to", "POST", "/api/v1/conversations/" + conv + "/messages", map[string]any{"body": "x", "reply_to_id": rawNumber(huge)}},
			{"the messages to read", "POST", "/api/v1/conversations/" + conv + "/read", map[string]any{"up_to_id": rawNumber(huge)}},
			{"a message", "PATCH", "/api/v1/messages/" + huge, map[string]any{"body": "x"}},
			{"a message file", "GET", "/api/v1/messages/attachments/" + huge, nil},
			{"the audit page", "GET", "/api/v1/admin/audit?page=" + huge, nil},
			{"the audit room", "GET", "/api/v1/admin/audit?room_id=" + huge, nil},
			{"the audit actor", "GET", "/api/v1/admin/audit?actor_id=" + huge, nil},
			{"a user to change", "PATCH", "/api/v1/admin/users/" + huge, map[string]any{"disabled": true}},
			{"the members of a room (admin)", "GET", "/api/v1/admin/rooms/" + huge + "/members", nil},
			{"the search page", "GET", "/api/v1/search?q=a&page=" + huge, nil},
		}
		for _, rq := range requests {
			rec := e.sendRaw(request(rq.method, rq.path, rq.body, bearer(token)))
			if rec.Code < 400 || rec.Code >= 500 {
				t.Errorf("%s with %s: %s %s -> %d, want a 4xx; body: %s", rq.name, huge, rq.method, rq.path, rec.Code, rec.Body)
			}
		}
	}
	if logged := e.log.String(); strings.Contains(logged, `"level":"ERROR"`) {
		t.Errorf("a failure was logged:\n%.2000s", logged)
	}
	_ = msg
}

// rawNumber is a number that is written into a JSON body as it is, above the range
// of a Go integer.
type rawNumber string

// MarshalJSON writes the digits.
func (j rawNumber) MarshalJSON() ([]byte, error) { return []byte(j), nil }

// TestTheLargestIDOfTheDatabaseIsAnOrdinaryMissingID checks the other side of
// the bound: 2^63-1 is a possible ID, and finds nothing.
func TestTheLargestIDOfTheDatabaseIsAnOrdinaryMissingID(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	token := e.token("alice")
	for _, path := range []string{"/api/v1/rooms/" + idLargestInt64, "/api/v1/posts/" + idLargestInt64, "/api/v1/me/sessions/" + idLargestInt64} {
		method := http.MethodGet
		if strings.Contains(path, "sessions") {
			method = http.MethodDelete
		}
		requireStatus(t, e.send(request(method, path, nil, bearer(token))), http.StatusNotFound)
	}
	if logged := e.log.String(); strings.Contains(logged, `"level":"ERROR"`) {
		t.Errorf("a failure was logged:\n%.2000s", logged)
	}
}

// TestOversizedIDInABodyNamesTheField checks the answer to a number above the
// range of the database in a body: 422 with the name of the field (the
// top-level field, whatever list or object the number is in).
func TestOversizedIDInABodyNamesTheField(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	token := e.token("alice")
	tests := []struct {
		name, path, body, field string
	}{
		{"a number", "/api/v1/notifications/read", `{"up_to_id": ` + idAboveInt64 + `}`, "up_to_id"},
		{"the largest unsigned number", "/api/v1/notifications/read", `{"up_to_id": ` + idLargestUint + `}`, "up_to_id"},
		{"a list", "/api/v1/conversations", `{"user_ids": [1, ` + idAboveInt64 + `]}`, "user_ids"},
		{"a list in a list of objects", "/api/v1/rooms/1/posts", `{"tasks": [{"title": "t", "assignee_ids": [` + idAboveInt64 + `]}]}`, "tasks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(request(http.MethodPost, tt.path, tt.body, bearer(token)))
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if fields := errorFields(t, rec); fields[tt.field] == nil {
				t.Errorf("fields = %v, want %q", fields, tt.field)
			}
		})
	}
	// The largest signed number is valid in the body.
	rec := e.send(request(http.MethodPost, "/api/v1/notifications/read", `{"up_to_id": `+idLargestInt64+`}`, bearer(token)))
	requireStatus(t, rec, http.StatusNoContent)
}

// TestHostileJSONBodies checks bodies built to break a decoder: very deep
// nesting, numbers out of range, a body of nothing but a quote, a NUL, invalid
// UTF-8 and a huge string. All of them are 4xx and none is logged as a failure.
func TestHostileJSONBodies(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleAdmin)
	token := e.token("alice")
	deep := strings.Repeat("[", 30000) + strings.Repeat("]", 30000)
	deepObject := strings.Repeat(`{"a":`, 10000) + "1" + strings.Repeat("}", 10000)
	tests := []struct {
		name, path, body string
		status           int
	}{
		{"deep arrays in a known field", "/api/v1/rooms/1/posts", `{"tags": ` + deep + `}`, http.StatusUnprocessableEntity},
		{"deep arrays in an unknown field", "/api/v1/rooms", `{"nope": ` + deep + `}`, http.StatusUnprocessableEntity},
		{"deep objects in an unknown field", "/api/v1/rooms", `{"nope": ` + deepObject + `}`, http.StatusUnprocessableEntity},
		{"a deep array as the body", "/api/v1/rooms", deep, http.StatusUnprocessableEntity},
		{"an exponent past a float", "/api/v1/columns/1/move", `{"position": 1e999}`, http.StatusUnprocessableEntity},
		{"a huge integer", "/api/v1/columns/1/move", `{"position": ` + strings.Repeat("9", 5000) + `}`, http.StatusUnprocessableEntity},
		{"a negative huge integer", "/api/v1/columns/1/move", `{"position": -` + strings.Repeat("9", 5000) + `}`, http.StatusUnprocessableEntity},
		{"a fraction", "/api/v1/columns/1/move", `{"position": 1.5}`, http.StatusUnprocessableEntity},
		{"a lone quote", "/api/v1/rooms", `"`, http.StatusUnprocessableEntity},
		{"a NUL", "/api/v1/rooms", "{\"name\": \"a\x00b\"}", http.StatusUnprocessableEntity},
		{"invalid UTF-8", "/api/v1/rooms", "{\"name\": \"a\xff\xfeb\", \"visibility\": \"visible\"}", http.StatusCreated},
		{"a string of 60 KiB", "/api/v1/rooms", `{"name": "` + strings.Repeat("a", 60<<10) + `", "visibility": "visible"}`, http.StatusUnprocessableEntity},
		{"a body over the limit", "/api/v1/rooms", `{"name": "` + strings.Repeat("a", 65<<10) + `"}`, http.StatusRequestEntityTooLarge},
		{"a trailing value", "/api/v1/rooms", `{"name": "x", "visibility": "visible"} {}`, http.StatusUnprocessableEntity},
		{"null for the body", "/api/v1/rooms", `null`, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.sendRaw(request(http.MethodPost, tt.path, tt.body, bearer(token)))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body: %.300s", rec.Code, tt.status, rec.Body)
			}
		})
	}
	if logged := e.log.String(); strings.Contains(logged, `"level":"ERROR"`) {
		t.Errorf("a failure was logged:\n%.2000s", logged)
	}
}

// TestSearchNeverFailsOnOddQueries sends the search the characters of every
// query language it could be mistaken for (FTS5, tsquery, LIKE, SQL). None may
// be a 5xx: the same test runs on PostgreSQL with scripts/test-docker.sh.
func TestSearchNeverFailsOnOddQueries(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleAdmin)
	token := e.token("alice")
	rec := e.send(request(http.MethodPost, "/api/v1/rooms", map[string]any{"name": "den", "visibility": "visible"}, bearer(token)))
	requireStatus(t, rec, http.StatusCreated)
	e.send(request(http.MethodPost, "/api/v1/rooms/1/posts", map[string]any{"body": "deploy the release, it's fine & dandy"}, bearer(token)))
	queries := []string{
		`(`, `)`, `((`, `"`, `""`, `"deploy`, `deploy"`, `'`, `''`, `\`, `\\`, `deploy\`, `*`, `deploy*`, `deploy:*`, `:*`, `&`, `|`, `!`, `!deploy`,
		`deploy & release`, `deploy | release`, `deploy <-> release`, `<->`, `<1>`, `deploy:`, `:deploy`, `NEAR(deploy release)`, `deploy OR release`,
		`deploy AND`, `NOT deploy`, `-deploy`, `+deploy`, `^deploy`, `col:deploy`, `{deploy}`, `[deploy]`, `deploy;`, `'; DROP TABLE posts; --`,
		`deploy' OR '1'='1`, `%`, `_`, `\%`, `#`, `##`, `#tag`, `#"x"`, `from:`, `room:`, `after:`, `before:`, `after:2026-13-45`, `from:' OR 1=1`,
		"deploy\x00", "\u0001\u0002", "￾", "😀", "deṕloy", strings.Repeat("a", 600), strings.Repeat("a ", 100), strings.Repeat("(", 300),
		`posts_fts MATCH`, `rank`, `tsvector`, `to_tsquery('x')`,
	}
	for _, q := range queries {
		path := "/api/v1/search?q=" + urlEscape(q)
		rec := e.sendRaw(request(http.MethodGet, path, nil, bearer(token)))
		if rec.Code >= 500 {
			t.Errorf("search %q -> %d %s", q, rec.Code, rec.Body)
		}
	}
	if logged := e.log.String(); strings.Contains(logged, `"level":"ERROR"`) {
		t.Errorf("a failure was logged:\n%.2000s", logged)
	}
}

// urlEscape escapes a query value.
func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(strconv.FormatInt(int64(c)+256, 16)[1:]))
	}
	return b.String()
}

// TestWSDoesNotNegotiateCompression checks that a client that asks for
// permessage-deflate does not get it: a compressed message could be a bomb
// inside the 16 KiB read limit.
func TestWSDoesNotNegotiateCompression(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	ctx, cancel := context.WithTimeout(t.Context(), wsWait)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, w.url(), &websocket.DialOptions{
		HTTPHeader:      http.Header{"Authorization": {"Bearer " + w.as("alice")}},
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	if got := resp.Header.Get("Sec-WebSocket-Extensions"); got != "" {
		t.Errorf("Sec-WebSocket-Extensions = %q, want none", got)
	}
}

// TestNoTokenInAnyAnswerOrLog logs in, uses the token on a spread of routes
// (also wrong ones) and checks that neither an answer body nor the log holds
// it, a password or the Authorization header.
func TestNoTokenInAnyAnswerOrLog(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	rec := e.send(request(http.MethodPost, "/api/v1/auth/login",
		map[string]any{"username": "alice", "password": testPassword, "client_name": "app"}))
	requireStatus(t, rec, http.StatusOK)
	token, _ := decode(t, rec)["token"].(string)
	if token == "" {
		t.Fatal("no token")
	}
	var answers bytes.Buffer
	for _, rq := range []struct{ method, path string }{
		{"GET", "/api/v1/me"}, {"GET", "/api/v1/me/sessions"}, {"GET", "/api/v1/nope"}, {"PUT", "/api/v1/me"},
		{"GET", "/api/v1/rooms/" + idAboveUint64}, {"GET", "/api/v1/admin/users"}, {"POST", "/api/v1/uploads"},
	} {
		rec := e.sendRaw(request(rq.method, rq.path, nil, bearer(token)))
		answers.Write(rec.Body.Bytes())
		for k, v := range rec.Header() {
			answers.WriteString(k + ": " + strings.Join(v, ",") + "\n")
		}
	}
	// The sessions list names sessions, never tokens or their hashes.
	if strings.Contains(answers.String(), token) {
		t.Error("an answer holds the token")
	}
	logged := e.log.String()
	for _, secret := range []string{token, testPassword, "Authorization", "Bearer " + token} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
}
