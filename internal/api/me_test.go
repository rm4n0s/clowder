package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

func TestMe(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.addUser("alice", store.RoleMember)
	admin := e.addUser("admin", store.RoleAdmin)
	tests := []struct {
		name     string
		username string
		id       uint64
		role     string
	}{
		{"a member", "alice", alice.ID, "member"},
		{"an admin", "admin", admin.ID, "admin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(e.token(tt.username))))
			requireStatus(t, rec, http.StatusOK)
			body := decode(t, rec)
			user := body["user"].(map[string]any)
			if user["id"] != float64(tt.id) || user["username"] != tt.username || user["role"] != tt.role {
				t.Errorf("user = %v", user)
			}
			if user["display_name"] != strings.ToUpper(tt.username[:1])+tt.username[1:] || user["created_at"] != "2026-10-26T09:30:00Z" {
				t.Errorf("user = %v", user)
			}
			if body["unread_notifications"] != float64(0) || body["unread_conversations"] != float64(0) {
				t.Errorf("counts = %v, %v", body["unread_notifications"], body["unread_conversations"])
			}
			if _, leaked := user["password_hash"]; leaked {
				t.Error("the password hash is in the answer")
			}
		})
	}
}

func TestMeCountsUnreadConversations(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.addUser("alice", store.RoleMember)
	bob := e.addUser("bob", store.RoleMember)
	bobActor := app.Actor{UserID: bob.ID, Username: bob.Username, Role: bob.Role}
	conv, err := e.msgs.StartDirect(t.Context(), bobActor, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"one", "two"} {
		if _, err := e.msgs.Send(t.Context(), bobActor, conv, app.MessageInput{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	rec := e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(e.token("alice"))))
	requireStatus(t, rec, http.StatusOK)
	if got := decode(t, rec)["unread_conversations"]; got != float64(1) {
		t.Errorf("unread_conversations = %v, want 1 (conversations, not messages)", got)
	}
}

func TestSessions(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	e.addUser("bob", store.RoleMember)
	e.browserToken("alice")
	e.clk.Advance(time.Hour)
	first := e.token("alice")
	e.clk.Advance(time.Hour)
	second := e.token("alice")
	e.token("bob") // someone else's session

	rec := e.send(request(http.MethodGet, "/api/v1/me/sessions", nil, bearer(first)))
	requireStatus(t, rec, http.StatusOK)
	list := decode(t, rec)["sessions"].([]any)
	if len(list) != 3 {
		t.Fatalf("%d sessions, want alice's 3: %v", len(list), list)
	}
	var kinds, current []string
	for _, item := range list {
		s := item.(map[string]any)
		kinds = append(kinds, s["kind"].(string))
		if s["current"] == true {
			current = append(current, fmt.Sprint(s["id"]))
		}
		if _, leaked := s["token"]; leaked {
			t.Error("a token is in the list")
		}
		if _, leaked := s["token_hash"]; leaked {
			t.Error("a token hash is in the list")
		}
	}
	// Newest first: second, first, the browser one.
	if strings.Join(kinds, ",") != "api,api,browser" {
		t.Errorf("kinds = %v, want api,api,browser (newest first)", kinds)
	}
	if first := list[1].(map[string]any); len(current) != 1 || current[0] != fmt.Sprint(first["id"]) {
		t.Errorf("current = %v, want only the session of the request (%v)", current, first["id"])
	}
	if list[0].(map[string]any)["created_at"] != "2026-10-26T11:30:00Z" {
		t.Errorf("created_at = %v", list[0].(map[string]any)["created_at"])
	}

	// Seen from the browser token's own request the current one differs: the
	// list is the same, the marker moves.
	list2 := decode(t, e.send(request(http.MethodGet, "/api/v1/me/sessions", nil, bearer(second))))["sessions"].([]any)
	if list2[0].(map[string]any)["current"] != true || list2[1].(map[string]any)["current"] != false {
		t.Errorf("sessions seen by the newest token = %v", list2)
	}
}

func TestSessionsListsOnlyLiveOnes(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	old := e.token("alice")
	e.clk.Advance(20 * 24 * time.Hour)
	live := e.token("alice")
	e.clk.Advance(20 * 24 * time.Hour) // old is 40 days old and unused: expired
	list := decode(t, e.send(request(http.MethodGet, "/api/v1/me/sessions", nil, bearer(live))))["sessions"].([]any)
	if len(list) != 1 {
		t.Errorf("sessions = %v, want only the live one", list)
	}
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(old))), http.StatusUnauthorized)
}

func TestEndSession(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	e.addUser("bob", store.RoleMember)
	mine := e.token("alice")
	other := e.token("alice")
	browser := e.browserToken("alice")
	bobs := e.token("bob")
	// idOf finds the ID of a session of the user of token in its list: the
	// current one or not, of the kind.
	idOf := func(token string, current bool, kind string) uint64 {
		t.Helper()
		list := decode(t, e.send(request(http.MethodGet, "/api/v1/me/sessions", nil, bearer(token))))["sessions"].([]any)
		for _, item := range list {
			s := item.(map[string]any)
			if (s["current"] == true) == current && s["kind"] == kind {
				return uint64(s["id"].(float64))
			}
		}
		t.Fatalf("no session (current %v, kind %s) in %v", current, kind, list)
		return 0
	}
	bobsID := idOf(bobs, true, "api")
	myID := idOf(mine, true, "api")
	browserID := idOf(mine, false, "browser")
	// The other api session is the one that is neither current nor a browser.
	var otherID uint64
	for _, item := range decode(t, e.send(request(http.MethodGet, "/api/v1/me/sessions", nil, bearer(mine))))["sessions"].([]any) {
		s := item.(map[string]any)
		if s["current"] == false && s["kind"] == "api" {
			otherID = uint64(s["id"].(float64))
		}
	}
	if otherID == 0 || otherID == myID {
		t.Fatalf("otherID = %d", otherID)
	}
	e.rec.Reset()

	// Someone else's session, a number that is none, and a non-number: 404.
	for _, tt := range []struct {
		name string
		id   string
	}{
		{"someone else's", strconv.FormatUint(bobsID, 10)},
		{"unknown", "999999"},
		{"zero", "0"},
		{"not a number", "abc"},
		{"negative", "-1"},
		{"too big", "99999999999999999999999"},
	} {
		rec := e.send(request(http.MethodDelete, "/api/v1/me/sessions/"+tt.id, nil, bearer(mine)))
		requireStatus(t, rec, http.StatusNotFound)
		if errorCode(t, rec) != "not_found" {
			t.Errorf("%s: code = %q", tt.name, errorCode(t, rec))
		}
	}
	// Bob's session is still there, and nothing was published.
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(bobs))), http.StatusOK)
	if got := len(e.rec.Events()); got != 0 {
		t.Errorf("%d events published by failed requests", got)
	}

	// Ending another client's session and a browser's logs them out.
	requireStatus(t, e.send(request(http.MethodDelete, "/api/v1/me/sessions/"+strconv.FormatUint(otherID, 10), nil, bearer(mine))), http.StatusNoContent)
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(other))), http.StatusUnauthorized)
	requireStatus(t, e.send(request(http.MethodDelete, "/api/v1/me/sessions/"+strconv.FormatUint(browserID, 10), nil, bearer(mine))), http.StatusNoContent)
	if _, err := e.accounts.SessionActor(t.Context(), browser); err == nil {
		t.Error("the browser session is still there")
	}
	// Ending the same one again is 404.
	requireStatus(t, e.send(request(http.MethodDelete, "/api/v1/me/sessions/"+strconv.FormatUint(browserID, 10), nil, bearer(mine))), http.StatusNotFound)
	// The two ended sessions published user.changed on alice's topic.
	events := e.rec.Events()
	if len(events) != 2 {
		t.Fatalf("%d events, want 2: %v", len(events), events)
	}
	for _, ev := range events {
		if ev.Kind != live.KindUserChanged || ev.Topic != live.UserTopic(e.userID("alice")) {
			t.Errorf("event = %+v, want user.changed on alice's topic", ev)
		}
	}
	// Ending the session of the request works and ends its token.
	requireStatus(t, e.send(request(http.MethodDelete, "/api/v1/me/sessions/"+strconv.FormatUint(myID, 10), nil, bearer(mine))), http.StatusNoContent)
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(mine))), http.StatusUnauthorized)
}

func TestLogout(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.addUser("alice", store.RoleMember)
	first, second := e.token("alice"), e.token("alice")
	browser := e.browserToken("alice")
	e.rec.Reset()

	rec := e.send(request(http.MethodPost, "/api/v1/auth/logout", nil, bearer(first)))
	requireStatus(t, rec, http.StatusNoContent)
	if rec.Body.Len() != 0 {
		t.Errorf("the 204 has a body: %q", rec.Body)
	}
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(first))), http.StatusUnauthorized)
	// The other sessions are not touched.
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(second))), http.StatusOK)
	if _, err := e.accounts.SessionActor(t.Context(), browser); err != nil {
		t.Errorf("the browser session was ended: %v", err)
	}
	if n, _ := e.st.CountSessionsOfUser(t.Context(), alice.ID); n != 2 {
		t.Errorf("%d sessions left, want 2", n)
	}
	// Logging out twice: the second is 401, since the token is gone.
	requireStatus(t, e.send(request(http.MethodPost, "/api/v1/auth/logout", nil, bearer(first))), http.StatusUnauthorized)
	// The logout published user.changed, as the page's logout does (D37).
	events := e.rec.Events()
	if len(events) != 1 || events[0].Kind != live.KindUserChanged || events[0].UserID != alice.ID {
		t.Errorf("events = %+v, want one user.changed for alice", events)
	}
}

func TestUsers(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	for _, name := range []string{"alice", "alfred", "albert", "bob", "carol"} {
		e.addUser(name, store.RoleMember)
	}
	// A display name that starts like another username.
	inv := e.invitation(store.RoleMember)
	if _, err := e.accounts.SignUp(t.Context(), inv, "zed", "Alma Zed", testPassword); err != nil {
		t.Fatal(err)
	}
	disabled := e.addUser("alina", store.RoleMember)
	if err := e.st.SetUserDisabled(t.Context(), disabled.ID, true); err != nil {
		t.Fatal(err)
	}
	token := e.token("bob")
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"a prefix", "?q=al", []string{"albert", "alfred", "alice", "zed"}},
		{"in capitals", "?q=AL", []string{"albert", "alfred", "alice", "zed"}},
		{"a display name", "?q=alma", []string{"zed"}},
		{"a display name in capitals", "?q=ALMA%20Z", []string{"zed"}},
		{"a longer prefix", "?q=alb", []string{"albert"}},
		{"a disabled user is left out", "?q=alin", nil},
		{"the middle of a name is not a prefix", "?q=ice", nil},
		{"nothing matches", "?q=xyz", nil},
		{"spaces around are trimmed", "?q=%20bob%20", []string{"bob"}},
		{"too long to match", "?q=" + strings.Repeat("a", 65), nil},
		{"empty q lists the first", "?q=", []string{"albert", "alfred", "alice", "bob", "carol", "zed"}},
		{"no q lists the first", "", []string{"albert", "alfred", "alice", "bob", "carol", "zed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(request(http.MethodGet, "/api/v1/users"+tt.query, nil, bearer(token)))
			requireStatus(t, rec, http.StatusOK)
			var got []string
			for _, item := range decode(t, rec)["users"].([]any) {
				got = append(got, item.(map[string]any)["username"].(string))
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("users = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestUsersTreatWildcardsAsPlainCharacters checks that a % or a _ in q is
// itself and not a LIKE wildcard.
func TestUsersTreatWildcardsAsPlainCharacters(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	for _, name := range []string{"xay", "x_y", "xby"} {
		e.addUser(name, store.RoleMember)
	}
	inv := e.invitation(store.RoleMember)
	if _, err := e.accounts.SignUp(t.Context(), inv, "pct", "100% Done", testPassword); err != nil {
		t.Fatal(err)
	}
	token := e.token("xay")
	tests := []struct {
		query string
		want  []string
	}{
		{"x_", []string{"x_y"}},
		{"x%25", nil},
		{"100%25", []string{"pct"}},
		{"1%25", nil},
		{"%25", nil},
		{"_", nil},
		{"%5C", nil},
	}
	for _, tt := range tests {
		rec := e.send(request(http.MethodGet, "/api/v1/users?q="+tt.query, nil, bearer(token)))
		requireStatus(t, rec, http.StatusOK)
		var got []string
		for _, item := range decode(t, rec)["users"].([]any) {
			got = append(got, item.(map[string]any)["username"].(string))
		}
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("q=%q: users = %v, want %v", tt.query, got, tt.want)
		}
	}
}

func TestUsersAreLimitedToTwenty(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	for i := 0; i < 25; i++ {
		e.addUser(fmt.Sprintf("user%02d", i), store.RoleMember)
	}
	token := e.token("user24")
	for _, q := range []string{"", "?q=user", "?q=USER0"} {
		rec := e.send(request(http.MethodGet, "/api/v1/users"+q, nil, bearer(token)))
		requireStatus(t, rec, http.StatusOK)
		got := decode(t, rec)["users"].([]any)
		want := 20
		if q == "?q=USER0" {
			want = 10
		}
		if len(got) != want {
			t.Errorf("q=%q: %d users, want %d", q, len(got), want)
		}
		if first := got[0].(map[string]any)["username"]; first != "user00" {
			t.Errorf("q=%q: first = %v, want user00 (ordered by username)", q, first)
		}
	}
}
