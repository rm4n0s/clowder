package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/api"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/errors"
)

const testPassword = "demo-password-1234"

// startAPI runs the real API on a test store with the users alice (member)
// and root (admin), and returns its address.
func startAPI(t *testing.T) string {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC))
	st := storetest.Open(t, clk)
	accounts := app.NewAccounts(st, clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: 7 * 24 * time.Hour, SessionLifetime: 30 * 24 * time.Hour,
	})
	for _, u := range []struct {
		name string
		role store.Role
	}{{"alice", store.RoleMember}, {"root", store.RoleAdmin}} {
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
	srv, err := api.New(api.Options{Services: api.Services{
		Accounts:      accounts,
		Notifications: app.NewNotifications(st, nil),
		Messages:      app.NewMessages(st, clk, fileStore, app.MessagesConfig{MaxAttachmentSize: 1 << 20, MaxAttachments: 2}, nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, rt := range srv.Routes() {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestRunAgainstTheRealAPI(t *testing.T) {
	t.Parallel()
	base := startAPI(t)
	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		wantCode   int
		wantOut    []string
		notWantOut []string
		wantErr    string
	}{
		{
			name:     "the session scenario",
			args:     []string{"-base-url", base, "-username", "alice", "-password", testPassword},
			wantCode: 0,
			wantOut: []string{
				"== session:", "> POST /api/v1/auth/login", `"client_name": "clowder-apicheck"`, `"password": "<password>"`,
				"< 200 OK", `"token": "<token>"`, "> GET /api/v1/me", "> Authorization: Bearer <token>",
				`"unread_notifications": 0`, "> GET /api/v1/me/sessions", `"current": true`, `"kind": "api"`,
				"> GET /api/v1/users?q=al", `"username": "alice"`, "> POST /api/v1/auth/logout", "< 204 No Content",
				"< 401 Unauthorized", "< WWW-Authenticate: Bearer", `"code": "unauthenticated"`,
				"ok session", "all scenarios passed (6 requests)",
			},
			notWantOut: []string{testPassword},
		},
		{
			name:     "the password from the environment and another client name",
			args:     []string{"-base-url", base + "/", "-username", "root", "-client-name", "Clowder Desktop"},
			env:      map[string]string{passwordEnv: testPassword},
			wantCode: 0,
			wantOut:  []string{`"client_name": "Clowder Desktop"`, `"role": "admin"`, "all scenarios passed"},
		},
		{
			name:     "only a scenario",
			args:     []string{"-base-url", base, "-username", "alice", "-password", testPassword, "-only", "session"},
			wantCode: 0, wantOut: []string{"ok session"},
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
			wantCode: 0, wantOut: []string{"session\t"},
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
			var out, errOut bytes.Buffer
			code := run(t.Context(), tt.args, func(k string) string { return tt.env[k] }, &out, &errOut)
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
	code := run(t.Context(), []string{"-base-url", spy.URL, "-username", "alice", "-password", testPassword}, func(string) string { return "" }, &out, &errOut)
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
		{"nested", `{"a":{"b":[{"token":"x"}]}}`, "\"token\": \"<token>\""},
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
