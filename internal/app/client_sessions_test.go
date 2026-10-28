package app_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// newRecEnv is an env whose service publishes to a Recorder.
func newRecEnv(t *testing.T) (*env, *live.Recorder) {
	t.Helper()
	e := newEnv(t)
	rec := &live.Recorder{}
	e.svc = app.NewAccounts(e.st, e.clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: lifetime, SessionLifetime: sessionLifetime, Publisher: rec,
	})
	return e, rec
}

// loginAs logs the user in as a client of the kind and returns the result.
func (e *env) loginAs(username string, kind app.SessionKind, name string) app.LoginResult {
	e.t.Helper()
	res, err := e.svc.LoginClient(e.t.Context(), app.ClientLogin{
		Username: username, Password: goodPassword, ClientIP: loginIP, Kind: kind, ClientName: name,
	})
	if err != nil {
		e.t.Fatalf("LoginClient %s: %+v", username, err)
	}
	return res
}

func TestCleanClientName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    app.SessionKind
		in      string
		want    string
		wantErr string // the field of the TagInvalidInput error
	}{
		{name: "api name", kind: app.SessionAPI, in: "Clowder Desktop", want: "Clowder Desktop"},
		{name: "api name is trimmed", kind: app.SessionAPI, in: "  app \t", want: "app"},
		{name: "api name of one character", kind: app.SessionAPI, in: "a", want: "a"},
		{name: "api name of 80 characters", kind: app.SessionAPI, in: strings.Repeat("é", 80), want: strings.Repeat("é", 80)},
		{name: "api name of 81 characters", kind: app.SessionAPI, in: strings.Repeat("é", 81), wantErr: app.FieldClientName},
		{name: "api name is empty", kind: app.SessionAPI, in: "", wantErr: app.FieldClientName},
		{name: "api name is spaces", kind: app.SessionAPI, in: " \t ", wantErr: app.FieldClientName},
		{name: "api name with a newline inside", kind: app.SessionAPI, in: "a\nb", wantErr: app.FieldClientName},
		{name: "api name with a NUL", kind: app.SessionAPI, in: "a\x00b", wantErr: app.FieldClientName},
		{name: "api name with a DEL", kind: app.SessionAPI, in: "a\x7fb", wantErr: app.FieldClientName},
		{name: "api name that is not UTF-8", kind: app.SessionAPI, in: "a\xffb", wantErr: app.FieldClientName},
		{name: "api name with emoji", kind: app.SessionAPI, in: "Laptop 💻", want: "Laptop 💻"},
		{name: "browser user agent", kind: app.SessionBrowser, in: "Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0", want: "Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0"},
		{name: "browser user agent is cut to 80", kind: app.SessionBrowser, in: strings.Repeat("ab", 60), want: strings.Repeat("ab", 40)},
		{name: "browser user agent is cut by characters", kind: app.SessionBrowser, in: strings.Repeat("é", 100), want: strings.Repeat("é", 80)},
		{name: "browser user agent loses control characters", kind: app.SessionBrowser, in: "a\x00b\nc\x7fd", want: "abcd"},
		{name: "browser user agent loses bytes that are not UTF-8", kind: app.SessionBrowser, in: "a\xffb", want: "ab"},
		{name: "browser user agent may be empty", kind: app.SessionBrowser, in: "", want: ""},
		{name: "browser user agent of spaces", kind: app.SessionBrowser, in: "   ", want: ""},
		{name: "unknown kind", kind: "robot", in: "x", wantErr: app.FieldForm},
		{name: "no kind", kind: "", in: "x", wantErr: app.FieldForm},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := app.CleanClientName(tt.kind, tt.in)
			if tt.wantErr != "" {
				appErr := requireTag(t, err, app.TagInvalidInput, "CleanClientName.InvalidInput")
				if f := fieldsOf(t, appErr); len(f) != 1 || f[tt.wantErr] == "" {
					t.Errorf("fields = %v, want one for %q", f, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("CleanClientName = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestLoginClient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		in         app.ClientLogin
		wantKind   app.SessionKind
		wantClient string
		wantErr    string
		wantRoute  string
	}{
		{
			name:     "an api client",
			in:       app.ClientLogin{Username: "alice", Password: goodPassword, Kind: app.SessionAPI, ClientName: " Clowder Desktop "},
			wantKind: app.SessionAPI, wantClient: "Clowder Desktop",
		},
		{
			name:     "a browser with its user agent",
			in:       app.ClientLogin{Username: "ALICE", Password: goodPassword, Kind: app.SessionBrowser, ClientName: strings.Repeat("x", 200)},
			wantKind: app.SessionBrowser, wantClient: strings.Repeat("x", 80),
		},
		{
			name:     "a browser without a name",
			in:       app.ClientLogin{Username: "alice", Password: goodPassword, Kind: app.SessionBrowser},
			wantKind: app.SessionBrowser,
		},
		{
			name:    "an api client without a name",
			in:      app.ClientLogin{Username: "alice", Password: goodPassword, Kind: app.SessionAPI},
			wantErr: app.TagInvalidInput, wantRoute: "Accounts.LoginClient->CleanClientName.InvalidInput",
		},
		{
			name:    "an unknown kind",
			in:      app.ClientLogin{Username: "alice", Password: goodPassword, Kind: "robot", ClientName: "x"},
			wantErr: app.TagInvalidInput, wantRoute: "Accounts.LoginClient->CleanClientName.InvalidInput",
		},
		{
			name:    "a wrong password as an api client",
			in:      app.ClientLogin{Username: "alice", Password: "wrong password!", Kind: app.SessionAPI, ClientName: "x"},
			wantErr: app.TagInvalidInput, wantRoute: "Accounts.LoginClient.InvalidInput",
		},
		{
			name:    "an unknown user as an api client",
			in:      app.ClientLogin{Username: "nobody", Password: goodPassword, Kind: app.SessionAPI, ClientName: "x"},
			wantErr: app.TagInvalidInput, wantRoute: "Accounts.LoginClient.InvalidInput",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			alice := e.signUp("alice", store.RoleMember)
			tt.in.ClientIP = loginIP
			res, err := e.svc.LoginClient(t.Context(), tt.in)
			if tt.wantErr != "" {
				_ = requireTag(t, err, tt.wantErr, tt.wantRoute)
				if n, _ := e.st.CountSessionsOfUser(t.Context(), alice.ID); n != 0 {
					t.Errorf("%d sessions after a failed login", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoginClient: %+v", err)
			}
			if res.User.ID != alice.ID || res.Token == "" || !res.ExpiresAt.Equal(start.Add(sessionLifetime)) {
				t.Errorf("result = %+v", res)
			}
			rows, err := e.st.SessionsOfUser(t.Context(), alice.ID)
			if err != nil || len(rows) != 1 {
				t.Fatalf("sessions = %v, %v", rows, err)
			}
			if rows[0].Kind != tt.wantKind || rows[0].ClientName != tt.wantClient {
				t.Errorf("session kind %q client %q, want %q %q", rows[0].Kind, rows[0].ClientName, tt.wantKind, tt.wantClient)
			}
			if rows[0].TokenHash == res.Token || strings.Contains(rows[0].TokenHash, res.Token) {
				t.Error("the token is stored")
			}
		})
	}
}

// TestLoginClientIsLimitedLikeLogin checks that the limiter counts the failures
// of API clients and browsers together.
func TestLoginClientIsLimitedLikeLogin(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.signUp("alice", store.RoleMember)
	for range 3 {
		_, err := e.svc.LoginClient(t.Context(), app.ClientLogin{Username: "alice", Password: "wrong password!", ClientIP: loginIP, Kind: app.SessionAPI, ClientName: "x"})
		_ = requireTag(t, err, app.TagInvalidInput, "Accounts.LoginClient.InvalidInput")
	}
	for range 2 {
		_, err := e.svc.Login(t.Context(), "alice", "wrong password!", loginIP)
		_ = requireTag(t, err, app.TagInvalidInput, "Accounts.Login->Accounts.LoginClient.InvalidInput")
	}
	_, err := e.svc.LoginClient(t.Context(), app.ClientLogin{Username: "alice", Password: goodPassword, ClientIP: loginIP, Kind: app.SessionAPI, ClientName: "x"})
	_ = requireTag(t, err, app.TagRateLimited, "Accounts.LoginClient.RateLimited")
}

// TestSessionKinds checks that a session is accepted only in the place of its
// kind (SPEC D43): a browser session by SessionActor, an API session by
// APISessionActor, and a refusal changes nothing.
func TestSessionKinds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    app.SessionKind
		use     func(e *env, token string) (app.Session, error)
		wantErr bool
	}{
		{"browser token as cookie", app.SessionBrowser, func(e *env, tok string) (app.Session, error) { return e.svc.SessionActor(e.t.Context(), tok) }, false},
		{"api token as bearer", app.SessionAPI, func(e *env, tok string) (app.Session, error) { return e.svc.APISessionActor(e.t.Context(), tok) }, false},
		{"browser token as bearer", app.SessionBrowser, func(e *env, tok string) (app.Session, error) { return e.svc.APISessionActor(e.t.Context(), tok) }, true},
		{"api token as cookie", app.SessionAPI, func(e *env, tok string) (app.Session, error) { return e.svc.SessionActor(e.t.Context(), tok) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			alice := e.signUp("alice", store.RoleMember)
			res := e.loginAs("alice", tt.kind, "client")
			sess, err := tt.use(e, res.Token)
			if tt.wantErr {
				appErr := requireTag(t, err, app.TagUnauthenticated, "Accounts.sessionActor.Unauthenticated")
				if appErr.Metadata["reason"] != "kind" {
					t.Errorf("reason = %v, want kind", appErr.Metadata["reason"])
				}
				// The session is untouched, even when it is long expired: a
				// refusal for the kind never deletes.
				e.clk.Advance(sessionLifetime + time.Hour)
				_, err := tt.use(e, res.Token)
				_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.sessionActor.Unauthenticated")
				if n, _ := e.st.CountSessionsOfUser(t.Context(), alice.ID); n != 1 {
					t.Errorf("%d sessions, want 1: a refused kind must not delete", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("session: %+v", err)
			}
			if sess.Actor.UserID != alice.ID || sess.SessionID == 0 {
				t.Errorf("session = %+v", sess)
			}
		})
	}
}

// TestSessions is the permission table of Sessions.
func TestSessions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.signUp("root", store.RoleAdmin)
	alice := e.signUp("alice", store.RoleMember)
	bob := e.signUp("bob", store.RoleMember)
	dave := e.signUp("dave", store.RoleMember)
	e.loginAs("root", app.SessionBrowser, "Firefox")
	e.clk.Advance(time.Minute)
	aliceBrowser := e.loginAs("alice", app.SessionBrowser, "Firefox on Linux")
	e.clk.Advance(time.Minute)
	aliceApp := e.loginAs("alice", app.SessionAPI, "Clowder Desktop")
	e.loginAs("bob", app.SessionAPI, "bob's app")
	e.loginAs("dave", app.SessionAPI, "dave's app")
	if err := e.svc.SetDisabled(t.Context(), actor(admin), dave.ID, true); err != nil {
		t.Fatal(err)
	}
	appSess, err := e.svc.APISessionActor(t.Context(), aliceApp.Token)
	if err != nil {
		t.Fatal(err)
	}
	browserSess, err := e.svc.SessionActor(t.Context(), aliceBrowser.Token)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		actor   app.Actor
		current uint64
		want    []string // "kind/client/current", newest first
	}{
		{"an admin sees their own", actor(admin), 0, []string{"browser/Firefox/false"}},
		{"a member sees both kinds, newest first", actor(alice), appSess.SessionID, []string{"api/Clowder Desktop/true", "browser/Firefox on Linux/false"}},
		{"the current one moves with the argument", actor(alice), browserSess.SessionID, []string{"api/Clowder Desktop/false", "browser/Firefox on Linux/true"}},
		{"no current session", actor(alice), 0, []string{"api/Clowder Desktop/false", "browser/Firefox on Linux/false"}},
		{"a number that is not theirs marks nothing of theirs", actor(alice), 99999, []string{"api/Clowder Desktop/false", "browser/Firefox on Linux/false"}},
		{"another member sees only theirs", actor(bob), 0, []string{"api/bob's app/false"}},
		{"a disabled user has no sessions left", actor(dave), 0, nil},
		{"a user that does not exist", app.Actor{UserID: 99999, Role: store.RoleMember}, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, err := e.svc.Sessions(t.Context(), tt.actor, tt.current)
			if err != nil {
				t.Fatalf("Sessions: %+v", err)
			}
			var got []string
			for _, s := range list {
				got = append(got, string(s.Kind)+"/"+s.ClientName+"/"+map[bool]string{true: "true", false: "false"}[s.Current])
				if s.ID == 0 || s.CreatedAt.IsZero() || s.LastSeenAt.IsZero() || s.ExpiresAt.IsZero() {
					t.Errorf("session = %+v has a zero field", s)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("sessions = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionsLeavesOutTheExpired(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	e.loginAs("alice", app.SessionAPI, "old")
	e.clk.Advance(sessionLifetime / 2)
	e.loginAs("alice", app.SessionAPI, "new")
	e.clk.Advance(sessionLifetime/2 + time.Second) // "old" ended a second ago
	list, err := e.svc.Sessions(t.Context(), actor(alice), 0)
	if err != nil || len(list) != 1 || list[0].ClientName != "new" {
		t.Errorf("sessions = %+v, %v, want only new", list, err)
	}
}

func TestSessionsStoreErrorPassesUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	_, err := e.svc.Sessions(canceledCtx(t), actor(alice), 0)
	_ = requireTag(t, err, store.TagQueryFailed, "Accounts.Sessions->Store.SessionsOfUser.QueryFailed")
}

// TestEndSession is the permission table of EndSession.
func TestEndSession(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// run ends a session and says what happened.
		run       func(e *env, rec *live.Recorder) error
		wantRoute string // empty: success
		// wantEvents is the number of user.changed events published in all,
		// the setup's included (disabling a user publishes one).
		wantEvents int
	}{
		{
			name: "a member ends their own api session",
			run: func(e *env, _ *live.Recorder) error {
				e.signUp("alice", store.RoleMember)
				res := e.loginAs("alice", app.SessionAPI, "gone")
				return e.svc.EndSession(e.t.Context(), actor(e.mustUser("alice")), e.sessionIDOf(res.Token, app.SessionAPI))
			},
			wantEvents: 1,
		},
		{
			name: "a member ends their own browser session",
			run: func(e *env, _ *live.Recorder) error {
				e.signUp("alice", store.RoleMember)
				res := e.loginAs("alice", app.SessionBrowser, "gone")
				return e.svc.EndSession(e.t.Context(), actor(e.mustUser("alice")), e.sessionIDOf(res.Token, app.SessionBrowser))
			},
			wantEvents: 1,
		},
		{
			name: "an admin ends their own session",
			run: func(e *env, _ *live.Recorder) error {
				e.signUp("root", store.RoleAdmin)
				res := e.loginAs("root", app.SessionAPI, "gone")
				return e.svc.EndSession(e.t.Context(), actor(e.mustUser("root")), e.sessionIDOf(res.Token, app.SessionAPI))
			},
			wantEvents: 1,
		},
		{
			name: "a member cannot end someone else's",
			run: func(e *env, _ *live.Recorder) error {
				e.signUp("alice", store.RoleMember)
				e.signUp("bob", store.RoleMember)
				res := e.loginAs("bob", app.SessionAPI, "bobs")
				return e.svc.EndSession(e.t.Context(), actor(e.mustUser("alice")), e.sessionIDOf(res.Token, app.SessionAPI))
			},
			wantRoute: "Accounts.EndSession->Store.DeleteSessionOfUser.NotFound",
		},
		{
			name: "an admin cannot end someone else's",
			run: func(e *env, _ *live.Recorder) error {
				e.signUp("root", store.RoleAdmin)
				e.signUp("bob", store.RoleMember)
				res := e.loginAs("bob", app.SessionAPI, "bobs")
				return e.svc.EndSession(e.t.Context(), actor(e.mustUser("root")), e.sessionIDOf(res.Token, app.SessionAPI))
			},
			wantRoute: "Accounts.EndSession->Store.DeleteSessionOfUser.NotFound",
		},
		{
			name: "a session that does not exist",
			run: func(e *env, _ *live.Recorder) error {
				e.signUp("alice", store.RoleMember)
				return e.svc.EndSession(e.t.Context(), actor(e.mustUser("alice")), 99999)
			},
			wantRoute: "Accounts.EndSession->Store.DeleteSessionOfUser.NotFound",
		},
		{
			name: "session 0",
			run: func(e *env, _ *live.Recorder) error {
				e.signUp("alice", store.RoleMember)
				return e.svc.EndSession(e.t.Context(), actor(e.mustUser("alice")), 0)
			},
			wantRoute: "Accounts.EndSession->Store.DeleteSessionOfUser.NotFound",
		},
		{
			name: "a disabled user has none to end",
			run: func(e *env, _ *live.Recorder) error {
				admin := e.signUp("root", store.RoleAdmin)
				dave := e.signUp("dave", store.RoleMember)
				res := e.loginAs("dave", app.SessionAPI, "daves")
				id := e.sessionIDOf(res.Token, app.SessionAPI)
				if err := e.svc.SetDisabled(e.t.Context(), actor(admin), dave.ID, true); err != nil {
					e.t.Fatal(err)
				}
				return e.svc.EndSession(e.t.Context(), actor(dave), id)
			},
			wantRoute: "Accounts.EndSession->Store.DeleteSessionOfUser.NotFound", wantEvents: 1,
		},
		{
			name: "the same session twice",
			run: func(e *env, _ *live.Recorder) error {
				e.signUp("alice", store.RoleMember)
				res := e.loginAs("alice", app.SessionAPI, "gone")
				id := e.sessionIDOf(res.Token, app.SessionAPI)
				if err := e.svc.EndSession(e.t.Context(), actor(e.mustUser("alice")), id); err != nil {
					e.t.Fatal(err)
				}
				return e.svc.EndSession(e.t.Context(), actor(e.mustUser("alice")), id)
			},
			wantRoute: "Accounts.EndSession->Store.DeleteSessionOfUser.NotFound", wantEvents: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, rec := newRecEnv(t)
			err := tt.run(e, rec)
			if tt.wantRoute != "" {
				_ = requireTag(t, err, app.TagNotFound, tt.wantRoute)
			} else if err != nil {
				t.Fatalf("EndSession: %+v", err)
			}
			changed := 0
			for _, ev := range rec.Events() {
				if ev.Kind == live.KindUserChanged {
					changed++
				}
			}
			if changed != tt.wantEvents {
				t.Errorf("%d user.changed events, want %d: %v", changed, tt.wantEvents, rec.Events())
			}
		})
	}
}

// TestEndSessionKeepsTheOthers checks that ending one session deletes only it,
// and that the token stops working.
func TestEndSessionKeepsTheOthers(t *testing.T) {
	t.Parallel()
	e, rec := newRecEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	e.signUp("bob", store.RoleMember)
	one := e.loginAs("alice", app.SessionAPI, "one")
	two := e.loginAs("alice", app.SessionAPI, "two")
	browser := e.loginAs("alice", app.SessionBrowser, "browser")
	bobs := e.loginAs("bob", app.SessionAPI, "bobs")
	rec.Reset()
	if err := e.svc.EndSession(t.Context(), actor(alice), e.sessionIDOf(one.Token, app.SessionAPI)); err != nil {
		t.Fatalf("EndSession: %+v", err)
	}
	if _, err := e.svc.APISessionActor(t.Context(), one.Token); err == nil {
		t.Error("the ended session still works")
	}
	for name, check := range map[string]func() error{
		"two":     func() error { _, err := e.svc.APISessionActor(t.Context(), two.Token); return err },
		"browser": func() error { _, err := e.svc.SessionActor(t.Context(), browser.Token); return err },
		"bobs":    func() error { _, err := e.svc.APISessionActor(t.Context(), bobs.Token); return err },
	} {
		if err := check(); err != nil {
			t.Errorf("%s: %+v", name, err)
		}
	}
	events := rec.Events()
	want := live.Event{Topic: live.UserTopic(alice.ID), Kind: live.KindUserChanged, UserID: alice.ID}
	if len(events) != 1 || events[0] != want {
		t.Errorf("events = %+v, want [%+v]", events, want)
	}
}

func TestEndSessionStoreErrorPassesUp(t *testing.T) {
	t.Parallel()
	e, rec := newRecEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	err := e.svc.EndSession(canceledCtx(t), actor(alice), 1)
	_ = requireTag(t, err, store.TagQueryFailed, "Accounts.EndSession->Store.DeleteSessionOfUser.QueryFailed")
	if len(rec.Events()) != 0 {
		t.Error("an event was published for a failure")
	}
}

// TestSearchUsers is the permission table of SearchUsers and its rules.
func TestSearchUsers(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.signUp("root", store.RoleAdmin)
	alice := e.signUp("alice", store.RoleMember)
	dave := e.signUp("dave", store.RoleMember)
	for _, name := range []string{"alfred", "albert", "bob"} {
		e.signUp(name, store.RoleMember)
	}
	inv, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SignUp(t.Context(), inv.Token, "zed", "Alma Zed", goodPassword); err != nil {
		t.Fatal(err)
	}
	gone := e.signUp("alina", store.RoleMember)
	if err := e.svc.SetDisabled(t.Context(), actor(admin), gone.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetDisabled(t.Context(), actor(admin), dave.ID, true); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		actor  app.Actor
		prefix string
		want   []string
		route  string // empty: success
	}{
		{name: "an admin", actor: actor(admin), prefix: "al", want: []string{"albert", "alfred", "alice", "zed"}},
		{name: "a member", actor: actor(alice), prefix: "al", want: []string{"albert", "alfred", "alice", "zed"}},
		{name: "a disabled user", actor: actor(dave), prefix: "al", route: "Accounts.SearchUsers.Unauthenticated"},
		{name: "a user that does not exist", actor: app.Actor{UserID: 99999, Role: store.RoleAdmin}, prefix: "al", route: "Accounts.SearchUsers.Unauthenticated"},
		{name: "any case", actor: actor(alice), prefix: "AlF", want: []string{"alfred"}},
		{name: "the display name", actor: actor(alice), prefix: "alma z", want: []string{"zed"}},
		{name: "trimmed", actor: actor(alice), prefix: "  bob ", want: []string{"bob"}},
		{name: "no match", actor: actor(alice), prefix: "q", want: nil},
		{name: "the middle of a name", actor: actor(alice), prefix: "lice", want: nil},
		{name: "the disabled are left out", actor: actor(alice), prefix: "alin", want: nil},
		{name: "a wildcard is a character", actor: actor(alice), prefix: "%", want: nil},
		{name: "an underscore is a character", actor: actor(alice), prefix: "al_", want: nil},
		{name: "a backslash is a character", actor: actor(alice), prefix: `\`, want: nil},
		{name: "too long to match anyone", actor: actor(alice), prefix: strings.Repeat("a", 65), want: nil},
		{name: "empty", actor: actor(alice), prefix: "", want: []string{"albert", "alfred", "alice", "bob", "root", "zed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users, err := e.svc.SearchUsers(t.Context(), tt.actor, tt.prefix)
			if tt.route != "" {
				_ = requireTag(t, err, app.TagUnauthenticated, tt.route)
				return
			}
			if err != nil {
				t.Fatalf("SearchUsers: %+v", err)
			}
			var got []string
			for _, u := range users {
				got = append(got, u.Username)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("users = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSearchUsersLimit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var first app.User
	for i := range 25 {
		u := e.signUp("user"+string(rune('a'+i)), store.RoleMember)
		if i == 0 {
			first = u
		}
	}
	for _, prefix := range []string{"", "user", "USER"} {
		users, err := e.svc.SearchUsers(t.Context(), actor(first), prefix)
		if err != nil || len(users) != 20 || users[0].Username != "usera" || users[19].Username != "usert" {
			t.Errorf("SearchUsers(%q) = %d users, %v, want the first 20 by username", prefix, len(users), err)
		}
	}
}

func TestSearchUsersStoreErrorsPassUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	_, err := e.svc.SearchUsers(canceledCtx(t), actor(alice), "a")
	_ = requireTag(t, err, store.TagQueryFailed, "Accounts.SearchUsers->Store.UserByID.QueryFailed")
}

func TestInvitationDetails(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	member, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	used, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SignUp(t.Context(), used.Token, "first", "First", goodPassword); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		token string
		want  store.Role
		route string
	}{
		{name: "a member invitation", token: member.Token, want: store.RoleMember},
		{name: "an admin invitation", token: admin.Token, want: store.RoleAdmin},
		{name: "a used one", token: used.Token, route: "Accounts.InvitationDetails->Accounts.usableInvitation.NotFound"},
		{name: "an unknown one", token: "nope", route: "Accounts.InvitationDetails->Accounts.usableInvitation->Store.InvitationByTokenHash.NotFound"},
		{name: "an empty one", token: "", route: "Accounts.InvitationDetails->Accounts.usableInvitation->Store.InvitationByTokenHash.NotFound"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := e.svc.InvitationDetails(t.Context(), tt.token)
			if tt.route != "" {
				_ = requireTag(t, err, app.TagNotFound, tt.route)
				return
			}
			if err != nil || info.Role != tt.want || !info.ExpiresAt.Equal(start.Add(lifetime)) {
				t.Errorf("InvitationDetails = %+v, %v, want role %s expiring %v", info, err, tt.want, start.Add(lifetime))
			}
		})
	}
	e.clk.Advance(lifetime)
	_, err = e.svc.InvitationDetails(t.Context(), member.Token)
	_ = requireTag(t, err, app.TagNotFound, "Accounts.InvitationDetails->Accounts.usableInvitation.NotFound")
}

// canceledCtx returns a context that is already canceled, which makes any
// query fail.
func canceledCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

// mustUser returns the user with the username as the service shows it.
func (e *env) mustUser(username string) app.User {
	e.t.Helper()
	row, err := e.st.UserByUsername(e.t.Context(), username)
	if err != nil {
		e.t.Fatalf("UserByUsername %s: %+v", username, err)
	}
	return app.User{ID: row.ID, Username: row.Username, DisplayName: row.DisplayName, Role: row.Role}
}

// sessionIDOf returns the ID of the session of the token of the kind.
func (e *env) sessionIDOf(token string, kind app.SessionKind) uint64 {
	e.t.Helper()
	var sess app.Session
	var err error
	if kind == app.SessionAPI {
		sess, err = e.svc.APISessionActor(e.t.Context(), token)
	} else {
		sess, err = e.svc.SessionActor(e.t.Context(), token)
	}
	if err != nil {
		e.t.Fatalf("session of the token: %+v", err)
	}
	return sess.SessionID
}
