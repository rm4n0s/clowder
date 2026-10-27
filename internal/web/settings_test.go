package web

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
)

// settingsEnv is a testEnv with alice, bob and a session of alice.
type settingsEnv struct {
	*testEnv
	alice, bob app.User
	token      string
}

func newSettingsEnv(t *testing.T) *settingsEnv {
	t.Helper()
	e := newTestEnv(t, nil)
	s := &settingsEnv{testEnv: e}
	s.alice = e.addUser("alice", "Alice", store.RoleMember)
	s.bob = e.addUser("bob", "Bob", store.RoleMember)
	s.token = e.session("alice")
	return s
}

// postAvatar sends the avatar form with the data in the file field "avatar".
func (e *settingsEnv) postAvatar(data []byte, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	files := []upload{{name: "avatar", filename: "me.png", data: data}}
	if data == nil {
		files = nil
	}
	body, contentType := multipartBody(e.t, nil, files)
	req := httptest.NewRequest(http.MethodPost, "/me/settings/avatar", body)
	req.Header.Set("Content-Type", contentType)
	for _, o := range append([]reqOpt{withCookie(e.token)}, opts...) {
		o(req)
	}
	return e.send(req)
}

func TestSettingsPage(t *testing.T) {
	t.Parallel()
	e := newSettingsEnv(t)
	rec := e.send(get("/me/settings", withCookie(e.token)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d:\n%s", rec.Code, rec.Body)
	}
	requireBody(t, rec, []string{
		"<title>Settings · Clowder</title>", "<h1>Settings</h1>",
		`<section id="profile" class="settings-section">`, `<section id="password" class="settings-section">`,
		`<section id="appearance" class="settings-section">`, `<section id="sessions" class="settings-section">`,
		`<form id="avatar-form" method="post" action="/me/settings/avatar" enctype="multipart/form-data"`, `type="file"`,
		`<form id="profile-form" method="post" action="/me/settings/profile"`, `name="displayName"`, `value="Alice"`,
		`<form id="password-form" method="post" action="/me/settings/password"`,
		`name="currentPassword"`, `name="newPassword"`, `name="newPasswordConfirm"`,
		`<form id="appearance-form" method="post" action="/me/settings/appearance"`,
		`value="system" checked`, `value="light"`, `value="dark"`,
		`<span class="avatar avatar-lg avatar-c`, `>A</span>`,
		`href="#sessions"`, `id="live-stream"`,
	}, "Saved", `data-theme`, "TODO", "avatar-remove-form", "Sign-in")
	// Alice has no avatar: no remove button. With saved=profile the section says so.
	rec = e.send(get("/me/settings?saved=password", withCookie(e.token)))
	requireBody(t, rec, []string{`<p class="saved" role="status">Saved</p>`})
	if n := strings.Count(rec.Body.String(), "Saved"); n != 1 {
		t.Errorf("Saved appears %d times, want once", n)
	}
	rec = e.send(get("/me/settings?saved=nonsense", withCookie(e.token)))
	requireBody(t, rec, nil, "Saved")
}

func TestSettingsProfile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		form      url.Values
		opts      []reqOpt
		wantCode  int
		want      []string
		wantName  string
		wantEvent bool
	}{
		{name: "datastar saves", form: url.Values{"displayName": {"  Alice B  "}}, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{`id="profile"`, `<p class="saved" role="status">Saved</p>`, `value="Alice B"`}, wantName: "Alice B", wantEvent: true},
		{name: "plain redirects back", form: url.Values{"displayName": {"Alice B"}}, wantCode: http.StatusSeeOther, wantName: "Alice B", wantEvent: true},
		{name: "datastar empty", form: url.Values{"displayName": {" "}}, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{`id="profile"`, `id="profile-display-name-error"`, "Use 1 to 64 characters."}, wantName: "Alice"},
		{name: "datastar too long", form: url.Values{"displayName": {strings.Repeat("x", 65)}}, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{"Use 1 to 64 characters.", strings.Repeat("x", 65)}, wantName: "Alice"},
		{name: "plain empty shows the page", form: url.Values{"displayName": {""}}, wantCode: http.StatusUnprocessableEntity,
			want: []string{"<title>Settings · Clowder</title>", "Use 1 to 64 characters.", `id="sessions"`}, wantName: "Alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newSettingsEnv(t)
			sub := e.hub.Subscribe(t.Context(), "user:"+strconv.FormatUint(e.alice.ID, 10))
			defer sub.Close()
			rec := e.send(postForm("/me/settings/profile", tt.form, append([]reqOpt{withCookie(e.token)}, tt.opts...)...))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d:\n%s", rec.Code, tt.wantCode, rec.Body)
			}
			requireBody(t, rec, tt.want)
			if tt.wantCode == http.StatusSeeOther && rec.Header().Get("Location") != "/me/settings?saved=profile#profile" {
				t.Errorf("Location = %q", rec.Header().Get("Location"))
			}
			if len(tt.opts) > 0 {
				requireSSE(t, rec)
			}
			row, _ := e.st.UserByID(t.Context(), e.alice.ID)
			if row.DisplayName != tt.wantName {
				t.Errorf("display name = %q, want %q", row.DisplayName, tt.wantName)
			}
			select {
			case ev := <-sub.C():
				if !tt.wantEvent || ev.Kind != "user.updated" {
					t.Errorf("event = %+v", ev)
				}
			default:
				if tt.wantEvent {
					t.Error("no user.updated event")
				}
			}
		})
	}
}

func TestSettingsProfileNeedsALogin(t *testing.T) {
	t.Parallel()
	e := newSettingsEnv(t)
	for _, target := range []string{"/me/settings/profile", "/me/settings/password", "/me/settings/appearance", "/me/settings/avatar/remove"} {
		rec := e.send(postForm(target, url.Values{"displayName": {"x"}}))
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("%s: status %d Location %q, want the login page", target, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := e.send(httptest.NewRequest(http.MethodPost, "/me/settings/avatar", strings.NewReader("")))
	if rec.Code != http.StatusFound {
		t.Errorf("avatar without a login: status %d, want the login page", rec.Code)
	}
}

func TestSettingsAvatar(t *testing.T) {
	t.Parallel()
	good := pngOf(t, 40, 30)
	tests := []struct {
		name     string
		data     []byte
		opts     []reqOpt
		wantCode int
		want     []string
		stored   bool
	}{
		{name: "datastar upload", data: good, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{`id="profile"`, `<img class="avatar avatar-lg" src="/avatars/`, `id="avatar-remove-form"`, "Saved"}, stored: true},
		{name: "plain upload redirects", data: good, wantCode: http.StatusSeeOther, stored: true},
		{name: "no file", data: nil, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, want: []string{"Choose a picture.", `id="avatar-file-error"`}},
		{name: "not a picture", data: []byte("hello"), opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, want: []string{"Use a PNG, JPEG or GIF picture."}},
		{name: "too many bytes", data: append(append([]byte{}, good...), make([]byte, app.AvatarMaxBytes)...), opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{"Use a picture of at most 2 MiB."}},
		{name: "plain failure shows the page", data: []byte("hello"), wantCode: http.StatusUnprocessableEntity,
			want: []string{"<title>Settings · Clowder</title>", "Use a PNG, JPEG or GIF picture."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newSettingsEnv(t)
			rec := e.postAvatar(tt.data, tt.opts...)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d:\n%.500s", rec.Code, tt.wantCode, rec.Body)
			}
			requireBody(t, rec, tt.want)
			row, _ := e.st.UserByID(t.Context(), e.alice.ID)
			if (row.AvatarKey != "") != tt.stored {
				t.Errorf("avatar key = %q, stored want %v", row.AvatarKey, tt.stored)
			}
		})
	}
}

func TestSettingsAvatarWrongBodies(t *testing.T) {
	t.Parallel()
	e := newSettingsEnv(t)
	// Not multipart: the 400 page.
	rec := e.send(postForm("/me/settings/avatar", url.Values{"avatar": {"x"}}, withCookie(e.token)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("urlencoded form: status = %d, want 400", rec.Code)
	}
	// A broken multipart body.
	req := httptest.NewRequest(http.MethodPost, "/me/settings/avatar", strings.NewReader("--B\r\nContent-Disposition: form-data; name=\"avatar\"; filename=\"a\"\r\n\r\nabc"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
	withCookie(e.token)(req)
	if rec = e.send(req); rec.Code != http.StatusBadRequest {
		t.Errorf("truncated multipart body: status = %d, want 400", rec.Code)
	}
}

func TestAvatarServing(t *testing.T) {
	t.Parallel()
	e := newSettingsEnv(t)
	if rec := e.postAvatar(pngOf(t, 40, 30), asDatastar()); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d", rec.Code)
	}
	row, _ := e.st.UserByID(t.Context(), e.alice.ID)
	bobToken := e.session("bob")
	path := "/avatars/" + strconv.FormatUint(e.alice.ID, 10)

	// Any logged-in user reads it.
	for name, token := range map[string]string{"alice": e.token, "bob": bobToken} {
		rec := e.send(get(path+"?v="+row.AvatarKey, withCookie(token)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", name, rec.Code)
		}
		h := rec.Header()
		if h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "private, max-age=86400" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: headers = %v", name, h)
		}
		if h.Get("ETag") != `"`+row.AvatarKey+`"` {
			t.Errorf("%s: ETag = %q", name, h.Get("ETag"))
		}
		img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
		if err != nil || img.Bounds() != image.Rect(0, 0, 256, 256) {
			t.Errorf("%s: body is not a 256x256 png: %v %v", name, img, err)
		}
	}
	// A conditional request is answered with 304.
	rec := e.send(get(path, withCookie(bobToken), withHeader("If-None-Match", `"`+row.AvatarKey+`"`)))
	if rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: status = %d, want 304", rec.Code)
	}
	// The pages show it where they show alice: the sidebar and the settings.
	rec = e.send(get("/me/settings", withCookie(e.token)))
	requireBody(t, rec, []string{`<img class="avatar avatar-md" src="/avatars/` + strconv.FormatUint(e.alice.ID, 10) + `?v=` + row.AvatarKey + `"`,
		`<img class="avatar avatar-lg" src="/avatars/`})

	for _, tt := range []struct {
		name  string
		path  string
		token string
		code  int
	}{
		{"no avatar", "/avatars/" + strconv.FormatUint(e.bob.ID, 10), e.token, http.StatusNotFound},
		{"unknown user", "/avatars/99999", e.token, http.StatusNotFound},
		{"not a number", "/avatars/abc", e.token, http.StatusNotFound},
		{"zero", "/avatars/0", e.token, http.StatusNotFound},
		{"too large a number", "/avatars/99999999999999999999", e.token, http.StatusNotFound},
		{"no login", path, "", http.StatusFound},
		{"an api token", path, e.apiToken("alice", "app"), http.StatusFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var opts []reqOpt
			if tt.token != "" {
				opts = append(opts, withCookie(tt.token))
			}
			rec := e.send(get(tt.path, opts...))
			if rec.Code != tt.code {
				t.Errorf("status = %d, want %d", rec.Code, tt.code)
			}
			if tt.code == http.StatusNotFound && strings.Contains(rec.Header().Get("Content-Type"), "image") {
				t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
			}
		})
	}

	// Removing it brings back the initials, and the address stops working.
	rec = e.send(postForm("/me/settings/avatar/remove", url.Values{}, withCookie(e.token), asDatastar()))
	requireSSE(t, rec)
	requireBody(t, rec, []string{`id="profile"`, `<span class="avatar avatar-lg avatar-c`, "Saved"}, "avatar-remove-form")
	if rec = e.send(get(path, withCookie(bobToken))); rec.Code != http.StatusNotFound {
		t.Errorf("after the removal: status = %d, want 404", rec.Code)
	}
	rec = e.send(postForm("/me/settings/avatar/remove", url.Values{}, withCookie(e.token)))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("removing nothing: status = %d, want 303", rec.Code)
	}
}

func TestAvatarsAppearWhereUsersAreShown(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	body, contentType := multipartBody(t, nil, []upload{{name: "avatar", filename: "me.png", data: pngOf(t, 8, 8)}})
	req := httptest.NewRequest(http.MethodPost, "/me/settings/avatar", body)
	req.Header.Set("Content-Type", contentType)
	e.cookies["alice"](req)
	if rec := e.send(req); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d:\n%s", rec.Code, rec.Body)
	}
	row, err := e.st.UserByID(t.Context(), e.alice.ID)
	if err != nil || row.AvatarKey == "" {
		t.Fatalf("avatar not stored: %v %q", err, row.AvatarKey)
	}
	src := `src="/avatars/` + strconv.FormatUint(e.alice.ID, 10) + `?v=` + row.AvatarKey + `"`
	// Bob sees alice's picture on her post, and her initials-less name for bob.
	if _, err := e.posts.Create(t.Context(), e.actor(e.alice), e.general.ID, app.PostInput{Format: app.FormatText, Body: "hello"}); err != nil {
		t.Fatalf("Create: %+v", err)
	}
	rec := e.send(func() *http.Request { r := get("/rooms/" + idText(e.general.ID)); e.cookies["bob"](r); return r }())
	requireBody(t, rec, []string{`<img class="avatar avatar-sm" ` + src, `<span class="post-author">Alice</span>`,
		// Bob has none: his initials on a colour.
		`<span class="avatar avatar-md avatar-c`})
}

func TestSettingsPassword(t *testing.T) {
	t.Parallel()
	const newPassword = "an entirely new passphrase"
	tests := []struct {
		name     string
		form     url.Values
		opts     []reqOpt
		wantCode int
		want     []string
		changed  bool
	}{
		{name: "datastar changes it", form: url.Values{"currentPassword": {testPassword}, "newPassword": {newPassword}, "newPasswordConfirm": {newPassword}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, want: []string{`id="password"`, `<p class="saved" role="status">Saved</p>`}, changed: true},
		{name: "plain redirects", form: url.Values{"currentPassword": {testPassword}, "newPassword": {newPassword}, "newPasswordConfirm": {newPassword}},
			wantCode: http.StatusSeeOther, changed: true},
		{name: "the two differ", form: url.Values{"currentPassword": {testPassword}, "newPassword": {newPassword}, "newPasswordConfirm": {newPassword + "x"}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, want: []string{"The two passwords are not the same.", `id="password-confirm-error"`}},
		{name: "wrong current", form: url.Values{"currentPassword": {"nope nope nope"}, "newPassword": {newPassword}, "newPasswordConfirm": {newPassword}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, want: []string{"This is not your password.", `id="password-current-error"`}},
		{name: "too short", form: url.Values{"currentPassword": {testPassword}, "newPassword": {"short"}, "newPasswordConfirm": {"short"}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, want: []string{"Use at least 12 characters.", `id="password-new-error"`}},
		{name: "the same", form: url.Values{"currentPassword": {testPassword}, "newPassword": {testPassword}, "newPasswordConfirm": {testPassword}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, want: []string{"Choose a password different from the current one."}},
		{name: "plain failure shows the page", form: url.Values{"currentPassword": {"nope nope nope"}, "newPassword": {newPassword}, "newPasswordConfirm": {newPassword}},
			wantCode: http.StatusUnprocessableEntity, want: []string{"<title>Settings · Clowder</title>", "This is not your password."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newSettingsEnv(t)
			other := e.session("alice")
			apiToken := e.apiToken("alice", "app")
			bobToken := e.session("bob")
			rec := e.send(postForm("/me/settings/password", tt.form, append([]reqOpt{withCookie(e.token)}, tt.opts...)...))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d:\n%.600s", rec.Code, tt.wantCode, rec.Body)
			}
			requireBody(t, rec, tt.want, newPassword, testPassword+"x")
			if len(tt.opts) > 0 {
				requireSSE(t, rec)
			}
			_, errOther := e.accounts.SessionActor(t.Context(), other)
			_, errAPI := e.accounts.APISessionActor(t.Context(), apiToken)
			_, errCurrent := e.accounts.SessionActor(t.Context(), e.token)
			_, errBob := e.accounts.SessionActor(t.Context(), bobToken)
			if errCurrent != nil || errBob != nil {
				t.Errorf("the current session or bob's ended: %v, %v", errCurrent, errBob)
			}
			if tt.changed != (errOther != nil) || tt.changed != (errAPI != nil) {
				t.Errorf("other sessions ended = %v (browser), %v (api); want %v", errOther != nil, errAPI != nil, tt.changed)
			}
			_, _, loginErr := e.accounts.Login(t.Context(), "alice", newPassword, "192.0.2.77")
			if tt.changed != (loginErr == nil) {
				t.Errorf("login with the new password: %v, password changed = %v", loginErr, tt.changed)
			}
		})
	}
}

func TestSettingsPasswordCountsInTheLimiter(t *testing.T) {
	t.Parallel()
	e := newSettingsEnv(t)
	wrong := url.Values{"currentPassword": {"nope nope nope"}, "newPassword": {"an entirely new passphrase"}, "newPasswordConfirm": {"an entirely new passphrase"}}
	for range 5 {
		rec := e.send(postForm("/me/settings/password", wrong, withCookie(e.token), asDatastar(), fromAddr("198.51.100.5:1234")))
		requireBody(t, rec, []string{"This is not your password."})
	}
	right := url.Values{"currentPassword": {testPassword}, "newPassword": {"an entirely new passphrase"}, "newPasswordConfirm": {"an entirely new passphrase"}}
	rec := e.send(postForm("/me/settings/password", right, withCookie(e.token), asDatastar(), fromAddr("198.51.100.6:1234")))
	requireBody(t, rec, []string{messageRateLimited})
	rec = e.send(postForm("/me/settings/password", right, withCookie(e.token), fromAddr("198.51.100.6:1234")))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("plain: status = %d, want 429", rec.Code)
	}
}

func TestSettingsAppearance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		theme   string
		wantJS  string
		wantErr string
		attr    string
	}{
		{theme: "dark", wantJS: `document.documentElement.setAttribute(&#39;data-theme&#39;, &#34;dark&#34;)`, attr: `data-theme="dark"`},
		{theme: "light", wantJS: `document.documentElement.setAttribute(&#39;data-theme&#39;, &#34;light&#34;)`, attr: `data-theme="light"`},
		{theme: "system", wantJS: `document.documentElement.removeAttribute(&#39;data-theme&#39;)`},
		{theme: "neon", wantErr: "Choose System, Light or Dark."},
	}
	for _, tt := range tests {
		t.Run(tt.theme, func(t *testing.T) {
			t.Parallel()
			e := newSettingsEnv(t)
			rec := e.send(postForm("/me/settings/appearance", url.Values{"theme": {tt.theme}}, withCookie(e.token), asDatastar()))
			requireSSE(t, rec)
			if tt.wantErr != "" {
				requireBody(t, rec, []string{tt.wantErr, `id="appearance"`}, "Saved")
				if got, _ := e.accounts.Settings(t.Context(), e.actor(e.alice)); got.Theme != app.ThemeSystem {
					t.Errorf("theme = %q after a refused one", got.Theme)
				}
				return
			}
			requireBody(t, rec, []string{`id="appearance"`, "Saved", tt.wantJS, `value="` + tt.theme + `" checked`})
			// The next page of the person has the attribute.
			page := e.send(get("/me/settings", withCookie(e.token)))
			if tt.attr == "" {
				requireBody(t, page, []string{`<html lang="en">`}, "data-theme")
			} else {
				requireBody(t, page, []string{`<html lang="en" ` + tt.attr + `>`})
			}
			// Other people and the login page are not themed.
			requireBody(t, e.send(get("/login")), []string{`<html lang="en">`}, "data-theme")
		})
	}
	// Plain form.
	e := newSettingsEnv(t)
	rec := e.send(postForm("/me/settings/appearance", url.Values{"theme": {"dark"}}, withCookie(e.token)))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/me/settings?saved=appearance#appearance" {
		t.Errorf("plain: status %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = e.send(postForm("/me/settings/appearance", url.Values{"theme": {"neon"}}, withCookie(e.token)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("plain refused: status %d, want 422", rec.Code)
	}
}

func (e *testEnv) actor(u app.User) app.Actor {
	return app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}
}
