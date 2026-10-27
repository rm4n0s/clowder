package api

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// settingsEnv is a testEnv with alice, bob and an admin.
func settingsEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, nil)
	e.addUser("admin", store.RoleAdmin)
	e.addUser("alice", store.RoleMember)
	e.addUser("bob", store.RoleMember)
	return e
}

// putAvatar sends the picture in the field file as the token.
func (e *testEnv) putAvatar(token, content string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.send(multipartRequest(http.MethodPut, "/api/v1/me/avatar",
		[]formPart{{name: "file", fileName: "me.png", content: content}}, bearer(token)))
}

func TestPatchMe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		body   any
		status int
		want   string
		field  string
	}{
		{"a new name", map[string]any{"display_name": "Alice B"}, http.StatusOK, "Alice B", ""},
		{"trimmed", map[string]any{"display_name": "  Alice B  "}, http.StatusOK, "Alice B", ""},
		{"64 characters", map[string]any{"display_name": strings.Repeat("é", 64)}, http.StatusOK, strings.Repeat("é", 64), ""},
		{"65 characters", map[string]any{"display_name": strings.Repeat("é", 65)}, http.StatusUnprocessableEntity, "", "display_name"},
		{"empty", map[string]any{"display_name": ""}, http.StatusUnprocessableEntity, "", "display_name"},
		{"spaces", map[string]any{"display_name": "   "}, http.StatusUnprocessableEntity, "", "display_name"},
		{"missing", map[string]any{}, http.StatusUnprocessableEntity, "", "display_name"},
		{"null", `{"display_name": null}`, http.StatusUnprocessableEntity, "", "display_name"},
		{"a number", `{"display_name": 5}`, http.StatusUnprocessableEntity, "", "display_name"},
		{"an unknown field", map[string]any{"display_name": "x", "role": "admin"}, http.StatusUnprocessableEntity, "", "role"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := settingsEnv(t)
			rec := e.send(request(http.MethodPatch, "/api/v1/me", tt.body, bearer(e.token("alice"))))
			requireStatus(t, rec, tt.status)
			if tt.status != http.StatusOK {
				if _, ok := errorFields(t, rec)[tt.field]; !ok {
					t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
				}
				return
			}
			user := decode(t, rec)
			if user["display_name"] != tt.want || user["username"] != "alice" || user["avatar_url"] != nil {
				t.Errorf("user = %v", user)
			}
			me := decode(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(e.token("alice")))))["user"].(map[string]any)
			if me["display_name"] != tt.want {
				t.Errorf("GET /me display_name = %v", me["display_name"])
			}
			if bob := e.accountUser("bob"); bob.DisplayName != "Bob" {
				t.Errorf("bob's name = %q", bob.DisplayName)
			}
			var got []live.Event
			for _, ev := range e.rec.Events() {
				if ev.Kind == live.KindUserUpdated {
					got = append(got, ev)
				}
			}
			if len(got) != 1 || got[0].Topic != live.UserTopic(e.userID("alice")) {
				t.Errorf("user.updated events = %+v", got)
			}
		})
	}
}

// accountUser reads the user from the store.
func (e *testEnv) accountUser(username string) store.User {
	e.t.Helper()
	u, err := e.st.UserByUsername(e.t.Context(), username)
	if err != nil {
		e.t.Fatalf("UserByUsername: %+v", err)
	}
	return u
}

func TestSettingsRoutes(t *testing.T) {
	t.Parallel()
	e := settingsEnv(t)
	alice := bearer(e.token("alice"))
	get := func(opts ...reqOpt) map[string]any {
		return decode(t, e.send(request(http.MethodGet, "/api/v1/me/settings", nil, opts...)))
	}
	want := func(theme string, sound, browser bool) map[string]any {
		return map[string]any{"theme": theme, "sound": sound, "browser_notifications": browser}
	}
	sameJSON(t, "defaults", get(alice), want("system", true, false))

	tests := []struct {
		name   string
		body   any
		status int
		want   map[string]any
		field  string
	}{
		{"the theme", map[string]any{"theme": "dark"}, 200, want("dark", true, false), ""},
		{"the sound keeps the theme", map[string]any{"sound": false}, 200, want("dark", false, false), ""},
		{"the browser notifications", map[string]any{"browser_notifications": true}, 200, want("dark", false, true), ""},
		{"nulls keep the values", `{"theme": null, "sound": true, "browser_notifications": null}`, 200, want("dark", true, true), ""},
		{"all three", map[string]any{"theme": "light", "sound": false, "browser_notifications": false}, 200, want("light", false, false), ""},
		{"back to system", map[string]any{"theme": "system"}, 200, want("system", false, false), ""},
		{"an unknown theme", map[string]any{"theme": "neon"}, 422, nil, "theme"},
		{"an empty theme", map[string]any{"theme": ""}, 422, nil, "theme"},
		{"no setting", map[string]any{}, 422, nil, "theme"},
		{"only nulls", `{"theme": null}`, 422, nil, "sound"},
		{"a wrong type", `{"sound": "yes"}`, 422, nil, "sound"},
		{"an unknown field", map[string]any{"volume": 3}, 422, nil, "volume"},
	}
	state := want("system", true, false)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(request(http.MethodPatch, "/api/v1/me/settings", tt.body, alice))
			requireStatus(t, rec, tt.status)
			if tt.status != http.StatusOK {
				if _, ok := errorFields(t, rec)[tt.field]; !ok {
					t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
				}
				sameJSON(t, "settings after a refused change", get(alice), state)
				return
			}
			sameJSON(t, "answer", decode(t, rec), tt.want)
			sameJSON(t, "stored", get(alice), tt.want)
			state = tt.want
		})
	}
	// Bob has his own.
	sameJSON(t, "bob's settings", get(bearer(e.token("bob"))), want("system", true, false))

	// One settings.changed per change, on alice's own topic.
	changed := 0
	for _, ev := range e.rec.Events() {
		if ev.Kind == live.KindSettingsChanged {
			changed++
			if ev.Topic != live.UserTopic(e.userID("alice")) {
				t.Errorf("settings.changed on %s", ev.Topic)
			}
		}
	}
	if changed != 6 {
		t.Errorf("%d settings.changed events, want 6", changed)
	}
}

func TestAvatarRoutes(t *testing.T) {
	t.Parallel()
	e := settingsEnv(t)
	alice, bob := e.token("alice"), e.token("bob")
	aliceID := e.userID("alice")

	rec := e.send(request(http.MethodGet, "/api/v1/avatars/"+strconv.FormatUint(aliceID, 10), nil, bearer(bob)))
	requireStatus(t, rec, http.StatusNotFound)

	rec = e.putAvatar(alice, pngBytes(t, 60, 40))
	requireStatus(t, rec, http.StatusOK)
	user := decode(t, rec)
	url, _ := user["avatar_url"].(string)
	prefix := "/api/v1/avatars/" + strconv.FormatUint(aliceID, 10) + "?v="
	if !strings.HasPrefix(url, prefix) || len(url) != len(prefix)+64 {
		t.Fatalf("avatar_url = %q, want %s<key>", url, prefix)
	}

	// Anyone logged in fetches it at that address, as a PNG of 256x256.
	rec = e.send(request(http.MethodGet, url, nil, bearer(bob)))
	requireStatus(t, rec, http.StatusOK)
	h := rec.Header()
	if h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "private, max-age=86400" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", h)
	}
	if img, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil || img.Bounds() != image.Rect(0, 0, 256, 256) {
		t.Errorf("body is not a 256x256 png: %v", err)
	}
	etag := h.Get("ETag")
	rec = e.send(request(http.MethodGet, url, nil, bearer(bob), header("If-None-Match", etag)))
	requireStatus(t, rec, http.StatusNotModified)

	// The user objects show it: me, the user list, a room member list.
	me := decode(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(alice))))["user"].(map[string]any)
	if me["avatar_url"] != url {
		t.Errorf("GET /me avatar_url = %v", me["avatar_url"])
	}
	users := list(t, decode(t, e.send(request(http.MethodGet, "/api/v1/users?q=al", nil, bearer(bob))))["users"])
	if len(users) != 1 || obj(t, users[0])["avatar_url"] != url {
		t.Errorf("GET /users = %v", users)
	}
	admin := list(t, decode(t, e.send(request(http.MethodGet, "/api/v1/admin/users", nil, bearer(e.token("admin")))))["users"])
	for _, u := range admin {
		m := obj(t, u)
		if (m["username"] == "alice") != (m["avatar_url"] == url) {
			t.Errorf("admin user %v", m)
		}
	}

	// A second picture gets a new address; the old key is gone.
	second := decode(t, e.putAvatar(alice, pngBytes(t, 10, 10)))
	if second["avatar_url"] == url || second["avatar_url"] == nil {
		t.Errorf("the second avatar_url = %v, want a new address", second["avatar_url"])
	}

	// Removing it.
	requireStatus(t, e.send(request(http.MethodDelete, "/api/v1/me/avatar", nil, bearer(alice))), http.StatusNoContent)
	requireStatus(t, e.send(request(http.MethodDelete, "/api/v1/me/avatar", nil, bearer(alice))), http.StatusNoContent)
	me = decode(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(alice))))["user"].(map[string]any)
	if me["avatar_url"] != nil {
		t.Errorf("avatar_url after the removal = %v", me["avatar_url"])
	}
	requireStatus(t, e.send(request(http.MethodGet, url, nil, bearer(bob))), http.StatusNotFound)

	updated := 0
	for _, ev := range e.rec.Events() {
		if ev.Kind == live.KindUserUpdated {
			updated++
		}
	}
	if updated != 3 {
		t.Errorf("%d user.updated events, want 3 (two uploads, one removal)", updated)
	}
}

func TestAvatarRouteRefusals(t *testing.T) {
	t.Parallel()
	e := settingsEnv(t)
	alice := e.token("alice")
	good := pngBytes(t, 20, 20)
	tests := []struct {
		name   string
		req    func() *http.Request
		status int
		field  string
	}{
		{"not a picture", func() *http.Request {
			return multipartRequest(http.MethodPut, "/api/v1/me/avatar", []formPart{{name: "file", fileName: "a.png", content: "hello"}}, bearer(alice))
		}, 422, "file"},
		{"too many bytes", func() *http.Request {
			return multipartRequest(http.MethodPut, "/api/v1/me/avatar", []formPart{{name: "file", fileName: "a.png", content: good + strings.Repeat("x", app.AvatarMaxBytes)}}, bearer(alice))
		}, 422, "file"},
		{"far too many bytes", func() *http.Request {
			return multipartRequest(http.MethodPut, "/api/v1/me/avatar", []formPart{{name: "file", fileName: "a.png", content: strings.Repeat("x", app.AvatarMaxBytes+avatarBodySlack+10)}}, bearer(alice))
		}, 413, ""},
		{"too many pixels", func() *http.Request {
			return multipartRequest(http.MethodPut, "/api/v1/me/avatar", []formPart{{name: "file", fileName: "a.png", content: pngBytes(t, 4097, 1)}}, bearer(alice))
		}, 422, "file"},
		{"the wrong field", func() *http.Request {
			return multipartRequest(http.MethodPut, "/api/v1/me/avatar", []formPart{{name: "avatar", fileName: "a.png", content: good}}, bearer(alice))
		}, 422, "file"},
		{"a field that is not a file", func() *http.Request {
			return multipartRequest(http.MethodPut, "/api/v1/me/avatar", []formPart{{name: "file", content: good}}, bearer(alice))
		}, 422, "file"},
		{"two files", func() *http.Request {
			return multipartRequest(http.MethodPut, "/api/v1/me/avatar", []formPart{{name: "file", fileName: "a.png", content: good}, {name: "file", fileName: "b.png", content: good}}, bearer(alice))
		}, 422, "file"},
		{"no parts", func() *http.Request {
			return multipartRequest(http.MethodPut, "/api/v1/me/avatar", nil, bearer(alice))
		}, 422, "file"},
		{"json", func() *http.Request {
			return request(http.MethodPut, "/api/v1/me/avatar", map[string]any{"file": "x"}, bearer(alice))
		}, 415, ""},
		{"a broken multipart body", func() *http.Request {
			r := request(http.MethodPut, "/api/v1/me/avatar", "not multipart", bearer(alice))
			r.Header.Set("Content-Type", "multipart/form-data; boundary=B")
			return r
		}, 422, "file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(tt.req())
			requireStatus(t, rec, tt.status)
			if tt.field != "" {
				if _, ok := errorFields(t, rec)[tt.field]; !ok {
					t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
				}
			}
			if key := e.accountUser("alice").AvatarKey; key != "" {
				t.Errorf("a refused picture left the key %q", key)
			}
		})
	}
	// Another user's avatar is not reachable by writing: only the caller's.
	requireStatus(t, e.putAvatar(alice, good), http.StatusOK)
	if e.accountUser("bob").AvatarKey != "" || e.accountUser("admin").AvatarKey != "" {
		t.Error("another user got the avatar")
	}
	// An address with a wrong ID is 404.
	for _, p := range []string{"/api/v1/avatars/abc", "/api/v1/avatars/0", "/api/v1/avatars/99999999999999999999", "/api/v1/avatars/99999"} {
		requireStatus(t, e.send(request(http.MethodGet, p, nil, bearer(alice))), http.StatusNotFound)
	}
}

func TestChangePasswordRoute(t *testing.T) {
	t.Parallel()
	const fresh = "an entirely new passphrase"
	e := settingsEnv(t)
	main, other, bobToken := e.token("alice"), e.token("alice"), e.token("bob")
	browser := e.browserToken("alice")

	tests := []struct {
		name   string
		body   any
		status int
		field  string
	}{
		{"wrong current", map[string]any{"current_password": "nope nope nope", "new_password": fresh}, 422, "current_password"},
		{"too short", map[string]any{"current_password": testPassword, "new_password": "short"}, 422, "new_password"},
		{"the same", map[string]any{"current_password": testPassword, "new_password": testPassword}, 422, "new_password"},
		{"missing fields", map[string]any{}, 422, "new_password"},
		{"missing current", map[string]any{"new_password": fresh}, 422, "current_password"},
		{"an unknown field", map[string]any{"current_password": testPassword, "new_password": fresh, "x": 1}, 422, "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(request(http.MethodPost, "/api/v1/me/password", tt.body, bearer(main), fromAddr("198.51.100.1:5")))
			requireStatus(t, rec, tt.status)
			if _, ok := errorFields(t, rec)[tt.field]; !ok {
				t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
			}
		})
	}
	// Nothing changed yet: every session works.
	for name, tok := range map[string]string{"main": main, "other": other} {
		requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(tok))), http.StatusOK)
		_ = name
	}

	rec := e.send(request(http.MethodPost, "/api/v1/me/password", map[string]any{"current_password": testPassword, "new_password": fresh}, bearer(main), fromAddr("198.51.100.2:5")))
	requireStatus(t, rec, http.StatusNoContent)
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(main))), http.StatusOK)
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(other))), http.StatusUnauthorized)
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(bobToken))), http.StatusOK)
	if _, err := e.accounts.SessionActor(t.Context(), browser); err == nil {
		t.Error("the browser session of the user is still live")
	}
	// The new password logs in and the old one does not.
	login := func(password string) int {
		rec := e.send(request(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "alice", "password": password, "client_name": "x"}, fromAddr("198.51.100.3:5")))
		return rec.Code
	}
	if got := login(fresh); got != http.StatusOK {
		t.Errorf("login with the new password: %d", got)
	}
	if got := login(testPassword); got != http.StatusUnauthorized {
		t.Errorf("login with the old password: %d", got)
	}
	// The change is in the audit log, without the password.
	audit := list(t, decode(t, e.send(request(http.MethodGet, "/api/v1/admin/audit?entity_type=user", nil, bearer(e.token("admin")))))["events"])
	if len(audit) != 1 || obj(t, audit[0])["action"] != "user.password_changed" || strings.Contains(fmt.Sprint(audit[0]), fresh) {
		t.Errorf("audit = %v", audit)
	}
}

func TestChangePasswordRouteIsRateLimited(t *testing.T) {
	t.Parallel()
	e := settingsEnv(t)
	tok := e.token("alice")
	wrong := map[string]any{"current_password": "nope nope nope", "new_password": "an entirely new passphrase"}
	for range 5 {
		requireStatus(t, e.send(request(http.MethodPost, "/api/v1/me/password", wrong, bearer(tok), fromAddr("198.51.100.9:5"))), http.StatusUnprocessableEntity)
	}
	right := map[string]any{"current_password": testPassword, "new_password": "an entirely new passphrase"}
	rec := e.send(request(http.MethodPost, "/api/v1/me/password", right, bearer(tok), fromAddr("198.51.100.10:5")))
	requireStatus(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") == "" || errorCode(t, rec) != "rate_limited" {
		t.Errorf("headers %v code %q", rec.Header(), errorCode(t, rec))
	}
}

func TestSettingsEventsReachTheWebSocket(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	own := topicOf(topicUser, w.userID("alice"))
	c := w.dial("alice")
	other := w.dial("bob")

	w.ok("alice", http.MethodPatch, "/api/v1/me/settings", map[string]any{"theme": "dark", "sound": false}, http.StatusOK)
	ev := c.await(own, "settings.changed")
	sameJSON(t, "settings.changed data", ev["data"], map[string]any{"theme": "dark", "sound": false, "browser_notifications": false})
	if ev["user_id"] != float64(w.userID("alice")) {
		t.Errorf("settings.changed = %v", ev)
	}

	w.ok("alice", http.MethodPatch, "/api/v1/me", map[string]any{"display_name": "Alice B"}, http.StatusOK)
	ev = c.await(own, "user.updated")
	data := obj(t, ev["data"])
	if data["display_name"] != "Alice B" || data["username"] != "alice" || data["avatar_url"] != nil {
		t.Errorf("user.updated data = %v", data)
	}

	// Only the user's own connections hear it.
	other.requireQuiet()
}

func TestSettingsRoutesNeedALoginAndNeverTouchOthers(t *testing.T) {
	t.Parallel()
	e := settingsEnv(t)
	for _, rt := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPatch, "/api/v1/me", map[string]any{"display_name": "X"}},
		{http.MethodGet, "/api/v1/me/settings", nil},
		{http.MethodPatch, "/api/v1/me/settings", map[string]any{"sound": false}},
		{http.MethodDelete, "/api/v1/me/avatar", nil},
		{http.MethodPost, "/api/v1/me/password", map[string]any{"current_password": testPassword, "new_password": "an entirely new passphrase"}},
		{http.MethodGet, "/api/v1/avatars/1", nil},
	} {
		requireStatus(t, e.send(request(rt.method, rt.path, rt.body)), http.StatusUnauthorized)
	}
	if got := e.accountUser("alice").DisplayName; got != "Alice" {
		t.Errorf("a request without a login changed the name to %q", got)
	}
}

// TestSettingsErrorRoutes checks the path of each error the routes of the
// settings make, and that each is a 422 or the status of its kind.
func TestSettingsErrorRoutes(t *testing.T) {
	t.Parallel()
	e := settingsEnv(t)
	read := func(r *http.Request) error {
		_, err := e.srv.readAvatar(httptest.NewRecorder(), r)
		return err
	}
	tests := []struct {
		name   string
		err    error
		tag    string
		route  string
		status int
	}{
		{"a profile without a name", checkProfilePatch(profilePatch{}), tagProfileEmpty, "checkProfilePatch.ProfileChangeEmpty", 422},
		{"settings without a setting", checkSettingsPatch(settingsPatch{}), tagSettingsEmpty, "checkSettingsPatch.SettingsChangeEmpty", 422},
		{"not multipart", read(request(http.MethodPut, "/x", map[string]any{})), tagUnsupportedMediaType, "Server.readAvatar.UnsupportedMediaType", 415},
		{"no boundary", read(func() *http.Request {
			r := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader("x"))
			r.Header.Set("Content-Type", "multipart/form-data")
			return r
		}()), tagUploadInvalid, "Server.readAvatar.UploadInvalid", 422},
		{"no part", read(multipartRequest(http.MethodPut, "/x", nil)), tagUploadInvalid, "Server.readAvatar.UploadInvalid", 422},
		{"the first part is a field", read(multipartRequest(http.MethodPut, "/x", []formPart{{"note", "", "x"}})), tagUploadInvalid, "Server.readAvatar.UploadInvalid", 422},
		{"two files", read(multipartRequest(http.MethodPut, "/x", []formPart{{"file", "a", "x"}, {"file", "b", "x"}})), tagUploadInvalid, "Server.readAvatar.UploadInvalid", 422},
		{"the body ends inside the file", read(func() *http.Request {
			r := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader("--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.png\"\r\n\r\nhalf a pi"))
			r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
			return r
		}()), tagUploadInvalid, "Server.readAvatar.UploadInvalid", 422},
		{"far over the cap", read(multipartRequest(http.MethodPut, "/x", []formPart{{"file", "a", strings.Repeat("x", app.AvatarMaxBytes+avatarBodySlack+5)}})),
			tagBodyTooLarge, "Server.readAvatar.BodyTooLarge", 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = requireRoute(t, tt.err, tt.tag, tt.route)
			p, internal := problemOf(tt.err)
			if internal || p.status != tt.status {
				t.Errorf("problemOf = status %d internal %v, want %d", p.status, internal, tt.status)
			}
		})
	}
	// A file that is large but within the cap is read whole, for the service to refuse.
	pic, err := e.srv.readAvatar(httptest.NewRecorder(), multipartRequest(http.MethodPut, "/x", []formPart{{"file", "a", strings.Repeat("x", app.AvatarMaxBytes+10)}}))
	if err != nil || len(pic) != app.AvatarMaxBytes+1 {
		t.Errorf("readAvatar of a large file: %d bytes, %v; want %d", len(pic), err, app.AvatarMaxBytes+1)
	}
}
