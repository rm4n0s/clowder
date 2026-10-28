package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/api"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/errors"
)

const testPassword = "demo-password-1234"

// startAPI runs the real API on a test store with the users alice and bob
// (members) and root (admin), and returns its address.
func startAPI(t *testing.T) string {
	t.Helper()
	first, _ := startAPIWith(t, nil)
	return first
}

// startAPIWith is startAPI with the hub the WebSocket listens to: nil makes the
// hub the services publish to, another one is a hub that hears nothing. It
// returns the address of a second server over the same services, standing for
// a second process of the same installation.
func startAPIWith(t *testing.T, listen *live.Hub) (first, second string) {
	t.Helper()
	first, second, _ = startAPIFull(t, listen, false)
	return first, second
}

// apiParts are the services of a test API that a test reaches into.
type apiParts struct {
	st       *store.Store
	accounts *app.Accounts
	keys     *app.SSHKeys
	clk      *clock.Fake
}

// startAPIFull is startAPIWith, and sshServer says that the accounts ask for
// the SSH code of a user who turned it on (the test runs the SSH server). It
// returns the services too.
func startAPIFull(t *testing.T, listen *live.Hub, sshServer bool) (first, second string, parts *apiParts) {
	t.Helper()
	hub := live.New()
	t.Cleanup(hub.Close)
	if listen == nil {
		listen = hub
	}
	clk := clock.NewFake(time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC))
	st := storetest.Open(t, clk)
	avatarStore, err := files.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accounts := app.NewAccounts(st, clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: 7 * 24 * time.Hour, SessionLifetime: 30 * 24 * time.Hour,
		Publisher: hub, Avatars: avatarStore, SSHServer: sshServer,
	})
	for _, u := range []struct {
		name string
		role store.Role
	}{{"alice", store.RoleMember}, {"bob", store.RoleMember}, {"carol", store.RoleMember}, {"root", store.RoleAdmin}} {
		inv, err := accounts.CreateSystemInvitation(t.Context(), u.role)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := accounts.SignUp(t.Context(), inv.Token, u.name, strings.ToUpper(u.name[:1])+u.name[1:], testPassword); err != nil {
			t.Fatal(err)
		}
	}
	fileStore, err := files.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rooms := app.NewRooms(st, fileStore, nil, hub)
	root, err := st.UserByUsername(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	// A visible room that alice has not joined, for the scenario to join.
	if _, err := rooms.Create(t.Context(), app.Actor{UserID: root.ID, Username: root.Username, Role: root.Role},
		app.RoomInput{Name: "lobby", Visibility: app.VisibilityVisible}); err != nil {
		t.Fatal(err)
	}
	keys := app.NewSSHKeys(st, clk, accounts, app.SSHServerInfo{Addr: ":2222", PublicHost: "chat.example.org", HostFingerprint: "SHA256:testhostkey"})
	srv, err := api.New(api.Options{Services: api.Services{
		Accounts:      accounts,
		Notifications: app.NewNotifications(st, hub),
		Messages:      app.NewMessages(st, clk, fileStore, app.MessagesConfig{MaxAttachmentSize: 1 << 20, MaxAttachments: 2}, hub),
		Rooms:         rooms,
		Posts: app.NewPosts(st, clk, markdown.New(), fileStore, app.PostsConfig{
			MaxAttachmentSize: 1 << 20, MaxAttachments: 2,
		}, hub),
		Board:   app.NewBoard(st, clk, hub),
		Search:  app.NewSearch(st),
		Audit:   app.NewAudit(st),
		Prefs:   app.NewPrefs(st, hub),
		SSHKeys: keys,
		Hub:     listen,
	}, MaxUploadSize: 1 << 20, BaseURL: "http://clowder.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, rt := range srv.Routes() {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	ts, other := httptest.NewServer(mux), httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	t.Cleanup(other.Close)
	return ts.URL, other.URL, &apiParts{st: st, accounts: accounts, keys: keys, clk: clk}
}

func TestRunAgainstTheRealAPI(t *testing.T) {
	t.Parallel()
	base := startAPI(t)
	tests := []struct {
		name string
		args []string
		// ownServer runs the case on a server of its own: a case that runs the
		// rooms scenario writes, and two runs of the same user on one database
		// can join and leave the same room at once (a double join is a unique
		// violation, 409).
		ownServer  bool
		env        map[string]string
		wantCode   int
		wantOut    []string
		notWantOut []string
		wantErr    string
	}{
		{
			name:      "the session scenario",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-admin-username", "root"},
			ownServer: true,
			wantCode:  0,
			wantOut: []string{
				"== session:", "> POST /api/v1/auth/login", `"client_name": "clowder-apicheck"`, `"password": "<password>"`,
				"< 200 OK", `"token": "<token>"`, "> GET /api/v1/me", "> Authorization: Bearer <token>",
				`"unread_notifications": 0`, "> GET /api/v1/me/sessions", `"current": true`, `"kind": "api"`,
				"> GET /api/v1/users?q=al", `"username": "alice"`, "> POST /api/v1/auth/logout", "< 204 No Content",
				"< 401 Unauthorized", "< WWW-Authenticate: Bearer", `"code": "unauthenticated"`,
				"ok session", "== rooms:", "ok rooms", "== messages:", "ok messages", "== admin:", "ok admin", "== watch:", "ok watch", "all scenarios passed (",
			},
			notWantOut: []string{testPassword},
		},
		{
			name:      "the password from the environment and another client name",
			args:      []string{"-base-url", base + "/", "-username", "root", "-client-name", "Clowder Desktop", "-admin-username", "root"},
			ownServer: true,
			env:       map[string]string{passwordEnv: testPassword},
			wantCode:  0,
			wantOut:   []string{`"client_name": "Clowder Desktop"`, `"role": "admin"`, "all scenarios passed"},
		},
		{
			name:     "only a scenario",
			args:     []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "session"},
			wantCode: 0, wantOut: []string{"ok session", "all scenarios passed (6 requests)"}, notWantOut: []string{"== rooms"},
		},
		{
			name:      "the rooms scenario",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "rooms"},
			ownServer: true,
			wantCode:  0,
			wantOut: []string{
				"== rooms:", "> GET /api/v1/rooms\n", `"name": "lobby"`, "> POST /api/v1/rooms/", "/join", "/leave",
				`"visibility": "private"`, "< 201 Created", "/candidates", "> POST /api/v1/rooms/", "/members",
				"/posts", `"format": "markdown"`, `"assignee_ids"`, `"due_date": "2030-01-31"`, "<strong>first</strong>",
				"/comments", "/reactions/%F0%9F%91%8D", "> PUT ", "> DELETE ", "> PATCH ", "<strong>edited</strong>",
				"/thread", `"depth": 2`, `"deleted": true`, "> GET /api/v1/tags?prefix=apic", "< 404 Not Found", "< 204 No Content",
				"ok rooms", "all scenarios passed",
			},
			notWantOut: []string{testPassword, "== session"},
		},
		{
			name:      "the rooms scenario as an admin",
			args:      []string{"-base-url", base, "-username", "root", "-password", testPassword, "-only", "rooms"},
			ownServer: true,
			wantCode:  0, wantOut: []string{"ok rooms"},
		},
		{
			name:      "the board scenario",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "board"},
			ownServer: true,
			env:       nil,
			wantCode:  0,
			wantOut: []string{
				"== board:", "> POST /api/v1/uploads", "> Content-Type: multipart/form-data; boundary=<boundary>",
				"> (the field file: notes.txt, 57 bytes)", "> (the field file: pixel.png,", `"content_type": "image/png"`,
				`"upload_ids"`, `"title": "Move this card"`, "> GET /api/v1/rooms/", "/board", `"name": "In progress"`,
				"/move", "< 204 No Content", `"due_date": "2030-02-15"`, "/columns", `"name": "In review"`,
				"> DELETE /api/v1/columns/", "> GET /api/v1/attachments/", "< Content-Type: application/octet-stream",
				"< Content-Type: image/png", "< Content-Disposition: attachment; filename=\"notes.txt\"",
				"< Content-Disposition: inline; filename=\"pixel.png\"", "< X-Content-Type-Options: nosniff",
				"< Cache-Control: private, max-age=86400", "< (57 bytes)", "> GET /api/v1/search?q=boardcheck+%23apicheck",
				`"match": true`, "> GET /api/v1/rooms\n", `"unread_posts": 1`, "/read", `"post_id": 999999999`, `"unread_posts": 0`,
				"> GET /api/v1/notifications", `"kind": "assigned"`, "> POST /api/v1/notifications/read",
				`"unread_notifications": 0`, "ok board", "all scenarios passed",
			},
			notWantOut: []string{testPassword, "Board check: a small text"},
		},
		{
			name:      "the messages scenario",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "messages"},
			ownServer: true,
			wantCode:  0,
			wantOut: []string{
				"== messages:", "> POST /api/v1/conversations", `"usernames": [`, "< 201 Created", `"kind": "direct"`,
				"> (the field file: note.txt, 59 bytes)", "> POST /api/v1/conversations/", "/messages", `"upload_ids"`,
				"&lt;bob&gt;", "/api/v1/messages/attachments/", "< Content-Disposition: attachment; filename=\"note.txt\"",
				"< (59 bytes)", `"unread": true`, `"reply_to_id"`, `"excerpt"`, "/read", "< 204 No Content",
				`"unread_conversations": 1`, "/reactions/%F0%9F%91%8D", "> PUT ", "> PATCH /api/v1/messages/",
				"< 403 Forbidden", "> DELETE /api/v1/messages/", `"deleted": true`, `"seen_up_to_id"`,
				`"kind": "group"`, "-renamed", "/leave", "< 422 Unprocessable Entity", "< 404 Not Found", "ok messages", "all scenarios passed",
			},
			notWantOut: []string{testPassword, "Message check: a small text"},
		},
		{
			name:      "the messages scenario twice finds the conversation again",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "messages,messages"},
			ownServer: true,
			wantCode:  0,
			wantOut:   []string{"ok messages"},
		},
		{
			name:      "the messages scenario with a third user that is not there",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "messages", "-third-username", "nobody"},
			ownServer: true,
			wantCode:  1, wantOut: []string{"FAILED messages", "got=422"},
		},
		{
			name:      "the admin scenario",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "admin", "-admin-username", "root"},
			ownServer: true,
			wantCode:  0,
			wantOut: []string{
				"== admin:", "> GET /api/v1/admin/users", `"disabled": false`, "> POST /api/v1/admin/invitations", "< 201 Created",
				`"url": "http://clowder.example.com/invite/<token>"`, "> GET /api/v1/auth/invitations/<secret>", "> GET /api/v1/admin/rooms",
				"/members", "> GET /api/v1/admin/audit?action=room.created&page=1", `"action": "room.created"`, "has_more",
				"audit?from=yesterday", "< 422 Unprocessable Entity", "< 403 Forbidden", "ok admin", "all scenarios passed",
			},
			notWantOut: []string{testPassword},
		},
		{
			name:      "the admin scenario with the password from the environment",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "admin", "-admin-username", "root"},
			ownServer: true,
			env:       map[string]string{adminPasswordEnv: "a wrong password!"},
			wantCode:  1, wantOut: []string{"FAILED admin", "got=401"}, notWantOut: []string{"a wrong password!"},
		},
		{
			name:      "the admin scenario with a member as the admin",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "admin", "-admin-username", "bob"},
			ownServer: true,
			wantCode:  1, wantOut: []string{"FAILED admin", "the admin user is not an admin"},
		},
		{
			name:      "the admin scenario as the admin who runs it",
			args:      []string{"-base-url", base, "-username", "root", "-password", testPassword, "-only", "admin", "-admin-username", "root"},
			ownServer: true,
			wantCode:  0, wantOut: []string{"ok admin"},
		},
		{
			name:     "no admin leaves the admin scenario out",
			args:     []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "session", "-admin-username", ""},
			wantCode: 0, wantOut: []string{"ok session"}, notWantOut: []string{"== admin"},
		},
		{
			name:     "the admin scenario asked for without an admin",
			args:     []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "session, admin", "-admin-username", ""},
			wantCode: 2, wantErr: "the admin scenario needs -admin-username",
		},
		{
			name:      "the watch scenario",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-watch"},
			ownServer: true,
			wantCode:  0,
			wantOut: []string{
				"== watch:", "> GET /api/v1/ws", "> Upgrade: websocket", "< 101 Switching Protocols",
				`> ws {"id":1,"topics":["room:`, "< ws {\"id\":1,\"topics\":[", `"type":"ack"`,
				"> POST /api/v1/rooms/", "/posts",
				"< ws event room:", " post.created ", "board:", " board.changed ", "board of room", "To do(1)", "In progress(1)", "Watch this card",
				"comment.created", "reaction.changed", "reactions: ", "message.created", "conversation.changed",
				"room.deleted", "error not_found topic=room:", "error not_found topic=board:", "error not_found topic=post:",
				"> POST /api/v1/auth/logout", "closed by the server: code 4001", "ok watch", "all scenarios passed",
			},
			notWantOut: []string{testPassword, "== session"},
		},
		{
			name:     "the watch scenario and -only",
			args:     []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-watch", "-only", "session"},
			wantCode: 2, wantErr: "-watch and -only cannot be used together",
		},
		{
			name:      "the board scenario with another user that is not there",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "board", "-other-username", "nobody"},
			ownServer: true,
			wantCode:  1, wantOut: []string{"FAILED board", "the other user is not among the users"},
		},
		{
			name:      "the board scenario with the password of the other user from the environment",
			args:      []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "board", "-other-username", "root"},
			ownServer: true,
			env:       map[string]string{otherPasswordEnv: "a wrong password!"},
			wantCode:  1, wantOut: []string{"FAILED board", "got=401"}, notWantOut: []string{"a wrong password!"},
		},
		{
			name:       "a wrong password",
			args:       []string{"-base-url", base, "-username", "alice", "-password", "a wrong password!"},
			wantCode:   1,
			wantOut:    []string{"< 401 Unauthorized", `"password": "<password>"`, "FAILED session", "StatusUnexpected", "want=200", "got=401"},
			notWantOut: []string{"a wrong password!"},
			wantErr:    "session failed",
		},
		{
			name:     "an unknown user",
			args:     []string{"-base-url", base, "-username", "nobody", "-password", testPassword},
			wantCode: 1, wantOut: []string{"FAILED session"}, notWantOut: []string{testPassword},
		},
		{
			name:     "no password",
			args:     []string{"-base-url", base, "-username", "alice"},
			wantCode: 2, wantErr: "-username and -password",
		},
		{
			name:     "no username",
			args:     []string{"-base-url", base, "-password", testPassword},
			wantCode: 2, wantErr: "-username and -password",
		},
		{
			name:     "an unknown scenario",
			args:     []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "nope"},
			wantCode: 2, wantErr: "no scenario has this name",
		},
		{
			name:     "an unknown flag",
			args:     []string{"-nope"},
			wantCode: 2, wantErr: "flag provided but not defined",
		},
		{
			name:     "an argument",
			args:     []string{"-username", "a", "-password", "b", "extra"},
			wantCode: 2, wantErr: "unexpected arguments: extra",
		},
		{
			name:     "the list",
			args:     []string{"-list"},
			wantCode: 0, wantOut: []string{"session\t", "rooms\t", "board\t", "messages\t", "msgsearch\t", "prefs\t", "follow\t", "admin\t", "watch\t"},
		},
		{
			name:     "a server that is not there",
			args:     []string{"-base-url", "http://127.0.0.1:1", "-username", "alice", "-password", testPassword},
			wantCode: 1, wantOut: []string{"FAILED session", "RequestFailed"}, notWantOut: []string{testPassword},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args := slices.Clone(tt.args)
			if tt.ownServer {
				own := startAPI(t)
				for i, a := range args {
					args[i] = strings.Replace(a, base, own, 1)
				}
			}
			var out, errOut bytes.Buffer
			code := run(t.Context(), args, func(k string) string { return tt.env[k] }, &out, &errOut)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.wantCode, out.String(), errOut.String())
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("stdout does not contain %q:\n%s", want, out.String())
				}
			}
			for _, bad := range tt.notWantOut {
				if strings.Contains(out.String(), bad) || strings.Contains(errOut.String(), bad) {
					t.Errorf("the output contains %q", bad)
				}
			}
			if tt.wantErr != "" && !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr does not contain %q:\n%s", tt.wantErr, errOut.String())
			}
		})
	}
}

// TestTheTokenIsNeverPrinted logs in against the real API and checks that the
// bearer token the server gave appears nowhere in the output.
func TestTheTokenIsNeverPrinted(t *testing.T) {
	t.Parallel()
	base := startAPI(t)
	// Ask the server for a token of our own, as the scenario would, to know
	// what must not appear.
	var token string
	spy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.DefaultClient.Do(func() *http.Request {
			req, _ := http.NewRequestWithContext(r.Context(), r.Method, base+r.URL.RequestURI(), r.Body)
			req.Header = r.Header.Clone()
			return req
		}())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		if r.URL.Path == "/api/v1/auth/login" {
			for _, part := range strings.Split(buf.String(), `"`) {
				if len(part) > 30 && !strings.Contains(part, ":") {
					token = part
				}
			}
		}
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(spy.Close)
	var out, errOut bytes.Buffer
	// The spy forwards plain requests, not a WebSocket, so the watch scenario
	// is left out.
	code := run(t.Context(), []string{"-base-url", spy.URL, "-username", "alice", "-password", testPassword, "-admin-username", "root",
		"-only", "session,rooms,board,messages,admin"}, func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s\n%s", code, out.String(), errOut.String())
	}
	if token == "" {
		t.Fatal("the spy did not see the token")
	}
	if strings.Contains(out.String(), token) || strings.Contains(errOut.String(), token) {
		t.Error("the token is in the output")
	}
}

// TestUnexpectedStatusesFail uses a fake server to check the exit status for
// answers the real one does not give.
func TestUnexpectedStatusesFail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantOut []string
	}{
		{
			name: "a 500 on the login",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"Something went wrong."}}`))
			},
			wantOut: []string{"< 500 Internal Server Error", "got=500"},
		},
		{
			name: "a login without a token",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"user":{"username":"alice"}}`))
			},
			wantOut: []string{"AnswerUnexpected", "no token"},
		},
		{
			name: "html instead of JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("<html>hello</html>"))
			},
			wantOut: []string{"<html>hello</html>", "AnswerUnexpected"},
		},
		{
			name: "a me that names another user",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/login" {
					_, _ = w.Write([]byte(`{"token":"abcdefghijklmnop","expires_at":"2026-11-01T00:00:00Z","user":{"username":"alice"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"user":{"username":"mallory"}}`))
			},
			wantOut: []string{"/me names another user", "got=mallory"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(tt.handler)
			t.Cleanup(ts.Close)
			var out, errOut bytes.Buffer
			code := run(t.Context(), []string{"-base-url", ts.URL, "-username", "alice", "-password", testPassword}, func(string) string { return "" }, &out, &errOut)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1\n%s", code, out.String())
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("stdout does not contain %q:\n%s", want, out.String())
				}
			}
			if strings.Contains(out.String(), "abcdefghijklmnop") {
				t.Error("the token is in the output")
			}
		})
	}
}

func TestRedaction(t *testing.T) {
	t.Parallel()
	r := &redactor{}
	r.add("hunter2-long-secret")
	r.add("short") // too short to be hidden
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a token field", `{"token":"abc","user":{"id":1}}`, "\"token\": \"<token>\""},
		{"an invitation token", `{"invitation_token":"abc"}`, "\"invitation_token\": \"<token>\""},
		{"a password", `{"password":"abc"}`, "\"password\": \"<password>\""},
		{"the message of a password field", `{"error":{"fields":{"current_password":"is not right"}}}`, `"current_password": "is not right"`},
		{"nested", `{"a":{"b":[{"token":"x"}]}}`, "\"token\": \"<token>\""},
		{"the link of an invitation", `{"url":"https://c.example/invite/abc"}`, `"url": "https://c.example/invite/<token>"`},
		{"another url", `{"url":"https://c.example/other"}`, `"url": "https://c.example/other"`},
		{"a url that is not text", `{"url":5}`, `"url": 5`},
		{"the path of a request", "GET /api/v1/auth/invitations/hunter2-long-secret", "GET /api/v1/auth/invitations/<secret>"},
		{"a known secret in a message", `{"error":{"message":"bad hunter2-long-secret here"}}`, "bad <secret> here"},
		{"a known secret in text", "plain hunter2-long-secret text", "plain <secret> text"},
		{"a short secret is left", `{"m":"short"}`, `"m": "short"`},
		{"an empty body", "", ""},
		{"spaces only", "  \n", ""},
	}
	for _, tt := range tests {
		got := r.printable([]byte(tt.in))
		if !strings.Contains(got, tt.want) && got != tt.want {
			t.Errorf("%s: printable = %q, want it to contain %q", tt.name, got, tt.want)
		}
		if strings.Contains(got, "hunter2-long-secret") || strings.Contains(got, `"abc"`) {
			t.Errorf("%s: a secret survived: %q", tt.name, got)
		}
	}
	if got := redactJSON(nil); got != nil {
		t.Errorf("redactJSON(nil) = %v", got)
	}
	// The input is not changed.
	in := map[string]any{"token": "abc"}
	_ = redactJSON(in)
	if in["token"] != "abc" {
		t.Error("redactJSON changed its input")
	}
}

func TestChoose(t *testing.T) {
	t.Parallel()
	all := []scenario{{name: "a"}, {name: "b"}, {name: "c"}}
	tests := []struct {
		only string
		want []string
		err  bool
	}{
		{"", []string{"a", "b", "c"}, false},
		{"  ", []string{"a", "b", "c"}, false},
		{"b", []string{"b"}, false},
		{"c, a", []string{"a", "c"}, false},
		{"a,x", nil, true},
	}
	for _, tt := range tests {
		got, err := choose(all, tt.only)
		if tt.err {
			appErr, ok := errors.FromError(err)
			if !ok || !appErr.HasRoute("choose.ScenarioUnknown") {
				t.Errorf("choose(%q) error = %v, want choose.ScenarioUnknown", tt.only, err)
			}
			continue
		}
		var names []string
		for _, s := range got {
			names = append(names, s.name)
		}
		if err != nil || strings.Join(names, ",") != strings.Join(tt.want, ",") {
			t.Errorf("choose(%q) = %v, %v, want %v", tt.only, names, err, tt.want)
		}
	}
}

// TestErrorRoutes checks the route of each tag the client makes.
func TestErrorRoutes(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	t.Cleanup(ts.Close)
	var out bytes.Buffer
	c := &client{base: ts.URL, http: ts.Client(), out: &out, redac: &redactor{}}
	ctx := context.Background()
	tests := []struct {
		name  string
		call  func() error
		route string
	}{
		{"a status that is not wanted", func() error { _, err := c.do(ctx, "GET", "/x", nil, 200); return err }, "client.do.StatusUnexpected"},
		{"a body that cannot be encoded", func() error { _, err := c.do(ctx, "POST", "/x", make(chan int), 200); return err }, "client.do.RequestEncodeFailed"},
		{"a bad method", func() error { _, err := c.do(ctx, "BAD METHOD", "/x", nil, 200); return err }, "client.do.RequestFailed"},
		{"a server that is gone", func() error {
			dead := &client{base: "http://127.0.0.1:1", http: ts.Client(), out: &out, redac: &redactor{}}
			_, err := dead.do(ctx, "GET", "/x", nil, 200)
			return err
		}, "client.do.RequestFailed"},
		{"a login without a token", func() error {
			r := &runner{c: &client{base: ts.URL, http: ts.Client(), out: &out, redac: &redactor{}}, username: "a", password: "p", clientName: "c"}
			_, err := r.login(ctx)
			return err
		}, "client.do.StatusUnexpected"},
	}
	for _, tt := range tests {
		err := tt.call()
		appErr, ok := errors.FromError(err)
		if !ok || !appErr.HasRoute(tt.route) {
			t.Errorf("%s: error %v, want the route %s", tt.name, err, tt.route)
		}
	}
}

// TestRoomsScenarioFailures uses a fake server that answers the steps of the
// rooms scenario wrongly, to check the exit status and what is printed.
func TestRoomsScenarioFailures(t *testing.T) {
	t.Parallel()
	reply := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	login := `{"token":"abcdefghijklmnop","expires_at":"2026-11-01T00:00:00Z","user":{"id":5,"username":"alice"}}`
	tests := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		wantOut []string
	}{
		{
			name: "the rooms cannot be listed",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					reply(w, 200, login)
					return
				}
				reply(w, 500, `{"error":{"code":"internal","message":"Something went wrong."}}`)
			},
			wantOut: []string{"FAILED rooms", "StatusUnexpected", "path=/api/v1/rooms", "got=500"},
		},
		{
			name: "a new room that is not the one made",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v1/auth/login":
					reply(w, 200, login)
				case r.Method == "GET":
					reply(w, 200, `{"rooms":[]}`)
				default:
					reply(w, 201, `{"id":9,"name":"other","is_member":true,"created_by":{"id":5}}`)
				}
			},
			wantOut: []string{"FAILED rooms", "AnswerUnexpected", "the new room is not as made"},
		},
		{
			name: "nobody to add",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v1/auth/login":
					reply(w, 200, login)
				case r.Method == "POST":
					var in struct{ Name string }
					_ = json.NewDecoder(r.Body).Decode(&in)
					reply(w, 201, `{"id":9,"name":"`+in.Name+`","is_member":true,"created_by":{"id":5}}`)
				case strings.HasSuffix(r.URL.Path, "/candidates"):
					reply(w, 200, `{"users":[{"id":5,"username":"alice"}]}`)
				default:
					reply(w, 200, `{"rooms":[]}`)
				}
			},
			wantOut: []string{"FAILED rooms", "there is nobody to add to the room"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(http.HandlerFunc(tt.handler))
			t.Cleanup(ts.Close)
			var out, errOut bytes.Buffer
			code := run(t.Context(), []string{"-base-url", ts.URL, "-username", "alice", "-password", testPassword, "-only", "rooms"},
				func(string) string { return "" }, &out, &errOut)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1\n%s", code, out.String())
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("stdout does not contain %q:\n%s", want, out.String())
				}
			}
			if strings.Contains(out.String(), "abcdefghijklmnop") {
				t.Error("the token is in the output")
			}
		})
	}
}

// TestRoomsScenarioLeavesTheRoomsAsItFoundThem checks the order of the visible
// room's join and leave: a user who is not in it joins and leaves, a member
// leaves and joins again, and either way the user ends as they started.
func TestRoomsScenarioLeavesTheRoomsAsItFoundThem(t *testing.T) {
	t.Parallel()
	tests := []struct {
		user        string
		first, then string
	}{
		{"alice", "/join", "/leave"},
		{"root", "/leave", "/join"},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			t.Parallel()
			base := startAPI(t)
			var out, errOut bytes.Buffer
			code := run(t.Context(), []string{"-base-url", base, "-username", tt.user, "-password", testPassword, "-only", "rooms"},
				func(string) string { return "" }, &out, &errOut)
			if code != 0 {
				t.Fatalf("exit code = %d\n%s\n%s", code, out.String(), errOut.String())
			}
			first := strings.Index(out.String(), "> POST /api/v1/rooms/1"+tt.first)
			then := strings.Index(out.String(), "> POST /api/v1/rooms/1"+tt.then)
			if first < 0 || then < 0 || first > then {
				t.Errorf("%s came at %d and %s at %d, want %s first", tt.first, first, tt.then, then, tt.first)
			}
			// The user is in the room again, or still out of it, as at the start.
			if got := isMemberOfLobby(t, base, tt.user); got != (tt.user == "root") {
				t.Errorf("%s is a member of lobby at the end = %v", tt.user, got)
			}
		})
	}
}

// isMemberOfLobby logs in as the user and returns whether the user is a
// member of the room lobby.
func isMemberOfLobby(t *testing.T, base, username string) bool {
	t.Helper()
	var out bytes.Buffer
	c := &client{base: base, http: http.DefaultClient, out: &out, redac: &redactor{}}
	r := &runner{c: c, username: username, password: testPassword, clientName: "check"}
	if _, err := r.login(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, err := c.do(t.Context(), "GET", "/api/v1/rooms", nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range a.items("rooms") {
		room, _ := item.(map[string]any)
		if room["name"] == "lobby" {
			return room["is_member"] == true
		}
	}
	t.Fatal("no lobby")
	return false
}

// TestWatchAcrossTwoServers has the other user write through a second server
// over the same services, the way a second process of an installation does.
func TestWatchAcrossTwoServers(t *testing.T) {
	t.Parallel()
	first, second := startAPIWith(t, nil)
	var out, errOut bytes.Buffer
	args := []string{"-base-url", first, "-other-base-url", second + "/", "-username", "alice", "-password", testPassword, "-watch"}
	code := run(t.Context(), args, func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{"# bob writes through " + second, "ok watch", "message.created", "room.deleted"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout does not contain %q:\n%s", want, out.String())
		}
	}
}

// TestWatchMissedEvent runs the scenario against a server whose WebSocket
// hears nothing: the first write has no event, and the run fails.
func TestWatchMissedEvent(t *testing.T) {
	t.Parallel()
	deaf := live.New()
	t.Cleanup(deaf.Close)
	base, _ := startAPIWith(t, deaf)
	var out, errOut bytes.Buffer
	args := []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-watch", "-event-timeout", "300ms"}
	code := run(t.Context(), args, func(string) string { return "" }, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstdout:\n%s", code, out.String())
	}
	for _, want := range []string{"FAILED watch", "EventMissed", "event room:", "post.created", "board.changed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout does not contain %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "ok watch") {
		t.Error("the scenario passed")
	}
}

// TestWatchNeedsTheWebSocket runs the scenario against a server that has no
// WebSocket route behind its token: the handshake is refused.
func TestWatchWithAWrongBaseURL(t *testing.T) {
	t.Parallel()
	base := startAPI(t)
	var out, errOut bytes.Buffer
	args := []string{"-base-url", base, "-other-base-url", "http://127.0.0.1:1", "-username", "alice", "-password", testPassword, "-watch"}
	code := run(t.Context(), args, func(string) string { return "" }, &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), "FAILED watch") || !strings.Contains(out.String(), "RequestFailed") {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, out.String())
	}
}

// TestSettingsScenario runs the settings scenario and checks what it prints and
// what it leaves behind: the name, the theme and the password as they were.
func TestSettingsScenario(t *testing.T) {
	t.Parallel()
	base := startAPI(t)
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", settingsScenarioName},
		func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s\n%s", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"== settings:", "> GET /api/v1/me/settings", `"theme": "system"`, "> PATCH /api/v1/me", `"display_name": "Alice (apicheck)"`,
		"> PATCH /api/v1/me/settings", `"theme": "dark"`, "> PUT /api/v1/me/avatar", "> (the field file: avatar.png,",
		"> GET /api/v1/avatars/", "< Content-Type: image/png", "< Cache-Control: private, max-age=86400",
		"> POST /api/v1/me/password", `"current_password": "<password>"`, `"new_password": "<password>"`,
		"< 422 Unprocessable Entity", "< 204 No Content", "< 401 Unauthorized", "> DELETE /api/v1/me/avatar", "ok settings",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the output does not contain %q:\n%s", want, text)
		}
	}
	for _, secret := range []string{testPassword, testPassword + "-changed-by-apicheck"} {
		if strings.Contains(text, secret) || strings.Contains(errOut.String(), secret) {
			t.Errorf("the password %q is in the output", secret)
		}
	}
	// Everything is as it was: the same password logs in, the name is back, no avatar.
	var out2, err2 bytes.Buffer
	if code := run(t.Context(), []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "session"}, func(string) string { return "" }, &out2, &err2); code != 0 {
		t.Fatalf("the password was not put back: exit %d\n%s\n%s", code, out2.String(), err2.String())
	}
}

// TestSSHKeysScenario runs the SSH keys scenario and checks what it prints and
// that it leaves the user with the keys they had.
func TestSSHKeysScenario(t *testing.T) {
	t.Parallel()
	base := startAPI(t)
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", sshKeysScenarioName},
		func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s\n%s", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"== sshkeys:", "> GET /api/v1/me/ssh-keys", `"command": "ssh -p 2222 alice@chat.example.org"`, "> POST /api/v1/me/ssh-keys",
		`"public_key": "ssh-ed25519 `, `"current_password": "<password>"`, "< 201 Created", "< 409 Conflict", "< 422 Unprocessable Entity",
		"> DELETE /api/v1/me/ssh-keys/", "< 204 No Content", "< 404 Not Found", "ok sshkeys",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the output does not contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, testPassword) || strings.Contains(errOut.String(), testPassword) {
		t.Error("the password is in the output")
	}
}

// TestPrefsScenario runs the preferences scenario and checks what it prints.
func TestPrefsScenario(t *testing.T) {
	t.Parallel()
	base := startAPI(t)
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", prefsScenarioName},
		func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s\n%s", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"== prefs:", "> GET /api/v1/me/settings", `"sound_name"`, "/notification-prefs", `"notify": "mentions"`,
		`"muted": true`, "< 422 Unprocessable Entity", "< 404 Not Found", "ok prefs",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the output does not contain %q:\n%s", want, text)
		}
	}
}

// TestMsgSearchScenario runs the scenario of the search of private messages
// and checks what it prints.
func TestMsgSearchScenario(t *testing.T) {
	t.Parallel()
	base := startAPI(t)
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", msgSearchScenarioName},
		func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s\n%s", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"== msgsearch:", "> GET /api/v1/messages/search?q=apicheck", `"message_id"`, `"match": true`, `"conversation": {`,
		"< 422 Unprocessable Entity", "> PATCH /api/v1/messages/", `"results": []`, "> DELETE /api/v1/messages/", "ok msgsearch",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the output does not contain %q:\n%s", want, text)
		}
	}
}

// TestFollowScenario runs the follow scenario and checks what it prints.
func TestFollowScenario(t *testing.T) {
	t.Parallel()
	base := startAPI(t)
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", followScenarioName},
		func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s\n%s", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"== follow:", "PUT /api/v1/posts/", "/follow", "DELETE /api/v1/posts/", `"following": true`, `"following": false`,
		"/group-mention-count", `"count": 0`, "/api/v1/me/presence?user_ids=", `"online": {}`,
		`"keywords"`, "apicheck keyword", "< 422 Unprocessable Entity", "< 404 Not Found", "ok follow",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the output does not contain %q:\n%s", want, text)
		}
	}
}
