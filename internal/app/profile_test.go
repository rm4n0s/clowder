package app_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// profileEnv is an env whose service stores avatars and publishes to a
// Recorder, with a member, an admin and a disabled user signed up.
type profileEnv struct {
	*env
	rec      *live.Recorder
	avatars  *files.Store
	dir      string
	admin    app.User
	alice    app.User
	bob      app.User
	disabled app.User
}

func newProfileEnv(t *testing.T) *profileEnv {
	t.Helper()
	e := newEnv(t)
	rec := &live.Recorder{}
	dir := filepath.Join(t.TempDir(), "avatars")
	avatars, err := files.New(dir)
	if err != nil {
		t.Fatalf("files.New: %+v", err)
	}
	e.svc = app.NewAccounts(e.st, e.clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: lifetime, SessionLifetime: sessionLifetime, Publisher: rec, Avatars: avatars,
	})
	p := &profileEnv{env: e, rec: rec, avatars: avatars, dir: dir}
	p.admin = e.signUp("root", store.RoleAdmin)
	p.alice = e.signUp("alice", store.RoleMember)
	p.bob = e.signUp("bob", store.RoleMember)
	p.disabled = e.signUp("dina", store.RoleMember)
	if err := e.svc.SetDisabled(t.Context(), actor(p.admin), p.disabled.ID, true); err != nil {
		t.Fatalf("SetDisabled: %+v", err)
	}
	rec.Reset()
	return p
}

// who is a caller of the permission tables.
type who struct {
	name string
	user func(p *profileEnv) app.User
	// wantRoute is the route of the error, "" for success.
	wantRoute string
}

// callers are the callers of a method that acts on the account of the caller.
func callers() []who {
	const disabled = "Accounts.%s->Accounts.activeUser->Accounts.activeUserOn.Unauthenticated"
	return []who{
		{name: "admin", user: func(p *profileEnv) app.User { return p.admin }},
		{name: "member", user: func(p *profileEnv) app.User { return p.alice }},
		{name: "disabled user", user: func(p *profileEnv) app.User { return p.disabled }, wantRoute: disabled},
	}
}

func TestUpdateProfile(t *testing.T) {
	t.Parallel()
	for _, c := range callers() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := newProfileEnv(t)
			u := c.user(p)
			got, err := p.svc.UpdateProfile(t.Context(), actor(u), "  New Name ")
			if c.wantRoute != "" {
				_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.UpdateProfile.func1->Accounts.activeUserOn.Unauthenticated")
				return
			}
			if err != nil {
				t.Fatalf("UpdateProfile: %+v", err)
			}
			if got.DisplayName != "New Name" || got.ID != u.ID {
				t.Errorf("user = %+v", got)
			}
			row, _ := p.st.UserByID(t.Context(), u.ID)
			if row.DisplayName != "New Name" {
				t.Errorf("stored display name = %q", row.DisplayName)
			}
			other, _ := p.st.UserByID(t.Context(), p.bob.ID)
			if other.DisplayName != "Bob" {
				t.Errorf("another user's display name = %q, want it unchanged", other.DisplayName)
			}
			ev := p.rec.Events()
			if len(ev) != 1 || ev[0].Kind != live.KindUserUpdated || ev[0].Topic != live.UserTopic(u.ID) || ev[0].UserID != u.ID {
				t.Errorf("events = %+v, want one user.updated on the user's topic", ev)
			}
			p.rec.Reset()
			if _, err := p.svc.UpdateProfile(t.Context(), actor(u), "New Name"); err != nil {
				t.Fatal(err)
			}
			if ev := p.rec.Events(); len(ev) != 0 {
				t.Errorf("an unchanged name published %+v", ev)
			}
		})
	}
}

func TestUpdateProfileInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"one character", "A", true},
		{"64 characters", strings.Repeat("é", 64), true},
		{"65 characters", strings.Repeat("é", 65), false},
		{"empty", "", false},
		{"spaces", "   ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProfileEnv(t)
			_, err := p.svc.UpdateProfile(t.Context(), actor(p.alice), tt.in)
			if tt.ok {
				if err != nil {
					t.Fatalf("UpdateProfile: %+v", err)
				}
				return
			}
			appErr := requireTag(t, err, app.TagInvalidInput, "Accounts.UpdateProfile.InvalidInput")
			if f := fieldsOf(t, appErr); f[app.FieldDisplayName] == "" {
				t.Errorf("fields = %v, want the display name", f)
			}
			if ev := p.rec.Events(); len(ev) != 0 {
				t.Errorf("a failed change published %+v", ev)
			}
		})
	}
}

func TestUpdateProfileFailsOnTheDatabase(t *testing.T) {
	t.Parallel()
	p := newProfileEnv(t)
	_, err := p.svc.UpdateProfile(canceledCtx(t), actor(p.alice), "Alice B")
	_ = requireTag(t, err, "TransactionFailed", "Accounts.UpdateProfile->Store.Tx.TransactionFailed")
	_, err = p.svc.UpdateProfile(t.Context(), app.Actor{UserID: 9999, Username: "ghost", Role: store.RoleMember}, "Ghost")
	_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.UpdateProfile.func1->Accounts.activeUserOn.Unauthenticated")
}

func TestSettings(t *testing.T) {
	t.Parallel()
	for _, c := range callers() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := newProfileEnv(t)
			u := c.user(p)
			got, err := p.svc.Settings(t.Context(), actor(u))
			if c.wantRoute != "" {
				_ = requireTag(t, err, app.TagUnauthenticated, fmtRoute(c.wantRoute, "Settings"))
				return
			}
			if err != nil {
				t.Fatalf("Settings: %+v", err)
			}
			if got != (app.Settings{Theme: app.ThemeSystem, Sound: true}) {
				t.Errorf("defaults = %+v, want system, sound on, browser notifications off", got)
			}
		})
	}
}

// fmtRoute fills the method name into a route pattern with one %s.
func fmtRoute(pattern, method string) string {
	return strings.Replace(pattern, "%s", method, 1)
}

func TestUpdateSettings(t *testing.T) {
	t.Parallel()
	dark, light, bogus := app.ThemeDark, app.ThemeLight, app.Theme("neon")
	yes, no := true, false
	tests := []struct {
		name   string
		update app.SettingsUpdate
		want   app.Settings
		route  string // empty: success
	}{
		{"theme only", app.SettingsUpdate{Theme: &dark}, app.Settings{Theme: app.ThemeDark, Sound: true}, ""},
		{"sound off", app.SettingsUpdate{Sound: &no}, app.Settings{Theme: app.ThemeSystem}, ""},
		{"browser notifications on", app.SettingsUpdate{BrowserNotifications: &yes}, app.Settings{Theme: app.ThemeSystem, Sound: true, BrowserNotifications: true}, ""},
		{"all three", app.SettingsUpdate{Theme: &light, Sound: &no, BrowserNotifications: &yes}, app.Settings{Theme: app.ThemeLight, BrowserNotifications: true}, ""},
		{"nothing", app.SettingsUpdate{}, app.Settings{Theme: app.ThemeSystem, Sound: true}, ""},
		{"unknown theme", app.SettingsUpdate{Theme: &bogus}, app.Settings{}, "Accounts.UpdateSettings.InvalidInput"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProfileEnv(t)
			got, err := p.svc.UpdateSettings(t.Context(), actor(p.alice), tt.update)
			if tt.route != "" {
				appErr := requireTag(t, err, app.TagInvalidInput, tt.route)
				if f := fieldsOf(t, appErr); f[app.FieldTheme] == "" {
					t.Errorf("fields = %v, want the theme", f)
				}
				if n := len(p.rec.Events()); n != 0 {
					t.Errorf("a failed change published %d events", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateSettings: %+v", err)
			}
			if got != tt.want {
				t.Errorf("settings = %+v, want %+v", got, tt.want)
			}
			again, err := p.svc.Settings(t.Context(), actor(p.alice))
			if err != nil || again != tt.want {
				t.Errorf("Settings = %+v, %v; want %+v", again, err, tt.want)
			}
			changed := tt.want != (app.Settings{Theme: app.ThemeSystem, Sound: true})
			ev := p.rec.Events()
			if changed {
				if len(ev) != 1 || ev[0].Kind != live.KindSettingsChanged || ev[0].Topic != live.UserTopic(p.alice.ID) {
					t.Errorf("events = %+v, want one settings.changed on the user's topic", ev)
				}
			} else if len(ev) != 0 {
				t.Errorf("an unchanged setting published %+v", ev)
			}
			// Another user keeps the defaults.
			other, err := p.svc.Settings(t.Context(), actor(p.bob))
			if err != nil || other != (app.Settings{Theme: app.ThemeSystem, Sound: true}) {
				t.Errorf("another user's settings = %+v, %v", other, err)
			}
		})
	}
}

func TestUpdateSettingsPermissions(t *testing.T) {
	t.Parallel()
	dark := app.ThemeDark
	for _, c := range callers() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := newProfileEnv(t)
			_, err := p.svc.UpdateSettings(t.Context(), actor(c.user(p)), app.SettingsUpdate{Theme: &dark})
			if c.wantRoute != "" {
				_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.UpdateSettings.func1->Accounts.activeUserOn.Unauthenticated")
				if n := len(p.rec.Events()); n != 0 {
					t.Errorf("a refused change published %d events", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateSettings: %+v", err)
			}
		})
	}
}

func TestSettingsFailOnTheDatabase(t *testing.T) {
	t.Parallel()
	p := newProfileEnv(t)
	dark := app.ThemeDark
	_, err := p.svc.UpdateSettings(canceledCtx(t), actor(p.alice), app.SettingsUpdate{Theme: &dark})
	_ = requireTag(t, err, "TransactionFailed", "Accounts.UpdateSettings->Store.Tx.TransactionFailed")
	_, err = p.svc.Settings(canceledCtx(t), actor(p.alice))
	_ = requireTag(t, err, store.TagQueryFailed, "Accounts.Settings")
}

// auditUserEvents returns the audit events about accounts.
func (p *profileEnv) auditUserEvents() []store.AuditEvent {
	p.t.Helper()
	rows, err := p.st.ListAuditEvents(p.t.Context(), store.AuditFilter{EntityType: store.EntityUser}, 50, 0)
	if err != nil {
		p.t.Fatalf("ListAuditEvents: %+v", err)
	}
	return rows
}

const newPassword = "a brand new passphrase"

func TestChangePassword(t *testing.T) {
	t.Parallel()
	p := newProfileEnv(t)
	ctx := t.Context()
	browser := p.loginAs("alice", app.SessionBrowser, "Firefox")
	other := p.loginAs("alice", app.SessionBrowser, "Chrome")
	client := p.loginAs("alice", app.SessionAPI, "Desktop")
	bobSession := p.loginAs("bob", app.SessionBrowser, "Bob's")
	keep := p.sessionIDOf(browser.Token, app.SessionBrowser)
	p.rec.Reset()

	err := p.svc.ChangePassword(ctx, actor(p.alice), keep, app.PasswordChange{CurrentPassword: goodPassword, NewPassword: newPassword, ClientIP: loginIP})
	if err != nil {
		t.Fatalf("ChangePassword: %+v", err)
	}

	if _, err := p.svc.SessionActor(ctx, browser.Token); err != nil {
		t.Errorf("the current session ended: %+v", err)
	}
	if _, err := p.svc.SessionActor(ctx, other.Token); err == nil {
		t.Error("another browser session of the user still works")
	}
	if _, err := p.svc.APISessionActor(ctx, client.Token); err == nil {
		t.Error("the API session of the user still works")
	}
	if _, err := p.svc.SessionActor(ctx, bobSession.Token); err != nil {
		t.Errorf("the session of another user ended: %+v", err)
	}
	if _, _, err := p.svc.Login(ctx, "alice", newPassword, loginIP); err != nil {
		t.Errorf("login with the new password: %+v", err)
	}
	if _, _, err := p.svc.Login(ctx, "alice", goodPassword, loginIP); err == nil {
		t.Error("login with the old password still works")
	}

	ev := p.rec.Events()
	if len(ev) == 0 || ev[0].Kind != live.KindUserChanged || ev[0].Topic != live.UserTopic(p.alice.ID) {
		t.Errorf("events = %+v, want user.changed on the user's topic", ev)
	}
	audit := p.auditUserEvents()
	if len(audit) != 1 {
		t.Fatalf("%d audit events, want 1", len(audit))
	}
	if a := audit[0]; a.Action != store.ActionUserPasswordChanged || a.ActorID != p.alice.ID || a.EntityID != p.alice.ID ||
		a.RoomID != store.NoRoom || a.EntityType != store.EntityUser || a.Details != "{}" ||
		a.Summary != "Alice changed their password" || strings.Contains(a.Summary+a.Details, newPassword) {
		t.Errorf("audit event = %+v", a)
	}
}

func TestChangePasswordFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      app.PasswordChange
		route   string
		field   string
		wantTag string
	}{
		{"too short", app.PasswordChange{CurrentPassword: goodPassword, NewPassword: "short"},
			"Accounts.ChangePassword.InvalidInput", app.FieldNewPassword, app.TagInvalidInput},
		{"eleven characters", app.PasswordChange{CurrentPassword: goodPassword, NewPassword: strings.Repeat("a", 11)},
			"Accounts.ChangePassword.InvalidInput", app.FieldNewPassword, app.TagInvalidInput},
		{"too long", app.PasswordChange{CurrentPassword: goodPassword, NewPassword: strings.Repeat("a", 1025)},
			"Accounts.ChangePassword.InvalidInput", app.FieldNewPassword, app.TagInvalidInput},
		{"the same", app.PasswordChange{CurrentPassword: goodPassword, NewPassword: goodPassword},
			"Accounts.ChangePassword.InvalidInput", app.FieldNewPassword, app.TagInvalidInput},
		{"wrong current", app.PasswordChange{CurrentPassword: "not the password", NewPassword: newPassword},
			"Accounts.ChangePassword.InvalidInput", app.FieldCurrentPassword, app.TagInvalidInput},
		{"empty current", app.PasswordChange{CurrentPassword: "", NewPassword: newPassword},
			"Accounts.ChangePassword.InvalidInput", app.FieldCurrentPassword, app.TagInvalidInput},
		{"huge current", app.PasswordChange{CurrentPassword: strings.Repeat("a", 2000), NewPassword: newPassword},
			"Accounts.ChangePassword.InvalidInput", app.FieldCurrentPassword, app.TagInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProfileEnv(t)
			ctx := t.Context()
			session := p.loginAs("alice", app.SessionBrowser, "Firefox")
			elsewhere := p.loginAs("alice", app.SessionAPI, "Desktop")
			p.rec.Reset()
			tt.in.ClientIP = loginIP
			err := p.svc.ChangePassword(ctx, actor(p.alice), p.sessionIDOf(session.Token, app.SessionBrowser), tt.in)
			appErr := requireTag(t, err, tt.wantTag, tt.route)
			if f := fieldsOf(t, appErr); f[tt.field] == "" {
				t.Errorf("fields = %v, want %q", f, tt.field)
			}
			if _, err := p.svc.APISessionActor(ctx, elsewhere.Token); err != nil {
				t.Errorf("a failed change ended another session: %+v", err)
			}
			if _, _, err := p.svc.Login(ctx, "alice", goodPassword, loginIP); err != nil {
				t.Errorf("the old password stopped working: %+v", err)
			}
			if n := len(p.auditUserEvents()); n != 0 {
				t.Errorf("a failed change wrote %d audit events", n)
			}
			for _, ev := range p.rec.Events() {
				if ev.Kind == live.KindUserChanged {
					t.Errorf("a failed change published %+v", ev)
				}
			}
		})
	}
}

func TestChangePasswordCountsInTheLimiter(t *testing.T) {
	t.Parallel()
	p := newProfileEnv(t)
	ctx := t.Context()
	session := p.loginAs("alice", app.SessionBrowser, "Firefox")
	id := p.sessionIDOf(session.Token, app.SessionBrowser)
	wrong := app.PasswordChange{CurrentPassword: "not the password", NewPassword: newPassword, ClientIP: "198.51.100.7"}
	for i := range 5 {
		err := p.svc.ChangePassword(ctx, actor(p.alice), id, wrong)
		_ = requireTag(t, err, app.TagInvalidInput, "Accounts.ChangePassword.InvalidInput")
		if i == 4 {
			break
		}
	}
	// The fifth failure blocks the username: even the right password waits.
	right := app.PasswordChange{CurrentPassword: goodPassword, NewPassword: newPassword, ClientIP: "198.51.100.8"}
	err := p.svc.ChangePassword(ctx, actor(p.alice), id, right)
	appErr := requireTag(t, err, app.TagRateLimited, "Accounts.ChangePassword.RateLimited")
	if appErr.Metadata["username"] != "alice" {
		t.Errorf("metadata = %v, want the username", appErr.Metadata)
	}
	// The login of that username is blocked too: they share the counter.
	_, _, err = p.svc.Login(ctx, "alice", goodPassword, "203.0.113.9")
	_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login->Accounts.LoginClient.RateLimited")
	// Time passes and the change works.
	p.clk.Advance(16 * time.Minute)
	if err := p.svc.ChangePassword(ctx, actor(p.alice), id, right); err != nil {
		t.Errorf("after the window: %+v", err)
	}
}

func TestChangePasswordLimiterCountsLoginFailuresToo(t *testing.T) {
	t.Parallel()
	p := newProfileEnv(t)
	ctx := t.Context()
	for range 5 {
		_, _, _ = p.svc.Login(ctx, "alice", "wrong wrong wrong", "198.51.100.1")
	}
	err := p.svc.ChangePassword(ctx, actor(p.alice), 0, app.PasswordChange{CurrentPassword: goodPassword, NewPassword: newPassword, ClientIP: "198.51.100.2"})
	_ = requireTag(t, err, app.TagRateLimited, "Accounts.ChangePassword.RateLimited")
}

func TestChangePasswordPermissions(t *testing.T) {
	t.Parallel()
	for _, c := range callers() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := newProfileEnv(t)
			err := p.svc.ChangePassword(t.Context(), actor(c.user(p)), 0, app.PasswordChange{CurrentPassword: goodPassword, NewPassword: newPassword, ClientIP: loginIP})
			if c.wantRoute != "" {
				_ = requireTag(t, err, app.TagUnauthenticated, fmtRoute(c.wantRoute, "ChangePassword"))
				return
			}
			if err != nil {
				t.Fatalf("ChangePassword: %+v", err)
			}
			// The password of another user is not touched.
			if _, _, err := p.svc.Login(t.Context(), "bob", goodPassword, loginIP); err != nil && c.user(p).ID != p.bob.ID {
				t.Errorf("bob's login: %+v", err)
			}
		})
	}
}

func TestChangePasswordFailsOnTheDatabase(t *testing.T) {
	t.Parallel()
	p := newProfileEnv(t)
	err := p.svc.ChangePassword(canceledCtx(t), actor(p.alice), 0, app.PasswordChange{CurrentPassword: goodPassword, NewPassword: newPassword, ClientIP: loginIP})
	_ = requireTag(t, err, store.TagQueryFailed, "Accounts.ChangePassword->Accounts.activeUser")
}

// ---- avatars ----

// pngBytes encodes img as a PNG.
func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// jpegBytes encodes img as a JPEG.
func jpegBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// solid is an opaque image of one colour.
func solid(w, h int, c color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	return img
}

// stripes is an image of three vertical (or horizontal) thirds: red, green, blue.
func stripes(w, h int, vertical bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			pos, size := x, w
			if !vertical {
				pos, size = y, h
			}
			var c color.NRGBA
			switch pos * 3 / size {
			case 0:
				c = color.NRGBA{R: 255, A: 255}
			case 1:
				c = color.NRGBA{G: 255, A: 255}
			default:
				c = color.NRGBA{B: 255, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// gifBytes encodes the frames, each a solid square of the colour, as a GIF.
func gifBytes(t *testing.T, size int, colours ...color.Color) []byte {
	t.Helper()
	g := &gif.GIF{}
	for _, c := range colours {
		frame := image.NewPaletted(image.Rect(0, 0, size, size), color.Palette{c})
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngHeaderOnly is the start of a PNG file: the signature and the IHDR chunk of
// an image w by h, and no data. DecodeConfig reads it; a decode fails.
func pngHeaderOnly(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2 // 8 bits, true colour
	chunk := append([]byte("IHDR"), ihdr...)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

// pixelAt returns the colour of the pixel of the stored avatar.
func pixelAt(t *testing.T, img image.Image, x, y int) color.NRGBA {
	t.Helper()
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

// near reports whether the channels of two colours differ by at most d.
func near(a, b color.NRGBA, d int) bool {
	abs := func(x int) int {
		if x < 0 {
			return -x
		}
		return x
	}
	return abs(int(a.R)-int(b.R)) <= d && abs(int(a.G)-int(b.G)) <= d && abs(int(a.B)-int(b.B)) <= d && abs(int(a.A)-int(b.A)) <= d
}

// storedAvatar reads the avatar of the user as OpenAvatar serves it and decodes it.
func (p *profileEnv) storedAvatar(u app.User) (image.Image, string, []byte) {
	p.t.Helper()
	f, key, err := p.svc.OpenAvatar(p.t.Context(), actor(p.bob), u.ID)
	if err != nil {
		p.t.Fatalf("OpenAvatar: %+v", err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	if err != nil {
		p.t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil || format != "png" {
		p.t.Fatalf("the stored avatar is %q, %v; want a PNG", format, err)
	}
	return img, key, raw
}

func TestSetAvatarImages(t *testing.T) {
	t.Parallel()
	red := color.NRGBA{R: 255, A: 255}
	green := color.NRGBA{G: 255, A: 255}
	blue := color.NRGBA{B: 255, A: 255}
	tests := []struct {
		name string
		data func(t *testing.T) []byte
		// wantMessage is part of the message of the field, "" for success.
		wantMessage string
		// check looks at the stored picture.
		check func(t *testing.T, img image.Image)
	}{
		{name: "png", data: func(t *testing.T) []byte { return pngBytes(t, solid(300, 300, red)) },
			check: func(t *testing.T, img image.Image) {
				if c := pixelAt(t, img, 128, 128); !near(c, red, 0) {
					t.Errorf("centre = %v, want red", c)
				}
			}},
		{name: "jpeg", data: func(t *testing.T) []byte { return jpegBytes(t, solid(300, 300, green)) },
			check: func(t *testing.T, img image.Image) {
				if c := pixelAt(t, img, 10, 200); !near(c, green, 4) {
					t.Errorf("pixel = %v, want green", c)
				}
			}},
		{name: "gif", data: func(t *testing.T) []byte { return gifBytes(t, 40, blue) },
			check: func(t *testing.T, img image.Image) {
				if c := pixelAt(t, img, 0, 0); !near(c, blue, 0) {
					t.Errorf("pixel = %v, want blue", c)
				}
			}},
		{name: "animated gif takes the first frame", data: func(t *testing.T) []byte { return gifBytes(t, 40, red, blue, green) },
			check: func(t *testing.T, img image.Image) {
				if c := pixelAt(t, img, 100, 100); !near(c, red, 0) {
					t.Errorf("pixel = %v, want the first frame, red", c)
				}
			}},
		{name: "wide picture is cut to the centre", data: func(t *testing.T) []byte { return pngBytes(t, stripes(600, 100, true)) },
			check: func(t *testing.T, img image.Image) {
				for _, pt := range [][2]int{{0, 0}, {255, 0}, {0, 255}, {255, 255}, {128, 128}} {
					if c := pixelAt(t, img, pt[0], pt[1]); !near(c, green, 1) {
						t.Errorf("pixel %v = %v, want only the green centre", pt, c)
					}
				}
			}},
		{name: "tall picture is cut to the centre", data: func(t *testing.T) []byte { return pngBytes(t, stripes(100, 600, false)) },
			check: func(t *testing.T, img image.Image) {
				for _, pt := range [][2]int{{0, 0}, {255, 0}, {0, 255}, {255, 255}, {128, 128}} {
					if c := pixelAt(t, img, pt[0], pt[1]); !near(c, green, 1) {
						t.Errorf("pixel %v = %v, want only the green centre", pt, c)
					}
				}
			}},
		{name: "small picture is scaled up", data: func(t *testing.T) []byte { return pngBytes(t, solid(3, 3, blue)) },
			check: func(t *testing.T, img image.Image) {
				if c := pixelAt(t, img, 255, 255); !near(c, blue, 0) {
					t.Errorf("pixel = %v, want blue", c)
				}
			}},
		{name: "one pixel", data: func(t *testing.T) []byte { return pngBytes(t, solid(1, 1, red)) }},
		{name: "4096 pixels wide", data: func(t *testing.T) []byte { return pngBytes(t, solid(4096, 1, red)) }},
		{name: "4096 pixels tall", data: func(t *testing.T) []byte { return pngBytes(t, solid(1, 4096, red)) }},
		{name: "transparent stays transparent", data: func(t *testing.T) []byte {
			img := image.NewNRGBA(image.Rect(0, 0, 512, 512))
			for y := range 512 {
				for x := range 256 {
					img.SetNRGBA(x, y, red)
				}
			}
			return pngBytes(t, img)
		}, check: func(t *testing.T, img image.Image) {
			if c := pixelAt(t, img, 10, 10); !near(c, red, 0) {
				t.Errorf("opaque side = %v, want red", c)
			}
			if c := pixelAt(t, img, 250, 10); c.A != 0 {
				t.Errorf("transparent side = %v, want alpha 0", c)
			}
		}},
		{name: "box filter averages", data: func(t *testing.T) []byte {
			img := image.NewNRGBA(image.Rect(0, 0, 512, 512))
			for y := range 512 {
				for x := range 512 {
					if x%2 == 0 {
						img.SetNRGBA(x, y, color.NRGBA{A: 255})
					} else {
						img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
					}
				}
			}
			return pngBytes(t, img)
		}, check: func(t *testing.T, img image.Image) {
			if c := pixelAt(t, img, 100, 100); !near(c, color.NRGBA{R: 128, G: 128, B: 128, A: 255}, 1) {
				t.Errorf("pixel = %v, want the grey average", c)
			}
		}},
		{name: "256 pixels is kept as it is", data: func(t *testing.T) []byte {
			img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
			for y := range 256 {
				for x := range 256 {
					img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 255})
				}
			}
			return pngBytes(t, img)
		}, check: func(t *testing.T, img image.Image) {
			for _, pt := range [][2]int{{0, 0}, {17, 200}, {255, 255}, {128, 3}} {
				want := color.NRGBA{R: uint8(pt[0]), G: uint8(pt[1]), B: uint8(pt[0] ^ pt[1]), A: 255}
				if c := pixelAt(t, img, pt[0], pt[1]); !near(c, want, 0) {
					t.Errorf("pixel %v = %v, want %v", pt, c, want)
				}
			}
		}},
		{name: "too many bytes", data: func(t *testing.T) []byte {
			return append(pngHeaderOnly(10, 10), make([]byte, app.AvatarMaxBytes)...)
		}, wantMessage: "2 MiB"},
		{name: "exactly the byte limit is read", data: func(t *testing.T) []byte {
			// A valid PNG padded with trailing bytes to exactly 2 MiB: the
			// decoder ignores what follows the picture.
			b := pngBytes(t, solid(10, 10, red))
			return append(b, make([]byte, app.AvatarMaxBytes-len(b))...)
		}},
		{name: "too wide, from the header only", data: func(t *testing.T) []byte { return pngHeaderOnly(4097, 10) }, wantMessage: "4096"},
		{name: "too tall, from the header only", data: func(t *testing.T) []byte { return pngHeaderOnly(10, 4097) }, wantMessage: "4096"},
		{name: "huge, from the header only", data: func(t *testing.T) []byte { return pngHeaderOnly(100000, 100000) }, wantMessage: "4096"},
		{name: "too wide jpeg", data: func(t *testing.T) []byte { return jpegBytes(t, solid(4097, 1, red)) }, wantMessage: "4096"},
		{name: "broken data", data: func(t *testing.T) []byte { return []byte("this is not a picture") }, wantMessage: "PNG, JPEG or GIF"},
		{name: "empty", data: func(t *testing.T) []byte { return nil }, wantMessage: "PNG, JPEG or GIF"},
		{name: "truncated png", data: func(t *testing.T) []byte { return pngHeaderOnly(100, 100) }, wantMessage: "PNG, JPEG or GIF"},
		{name: "truncated jpeg", data: func(t *testing.T) []byte { b := jpegBytes(t, solid(50, 50, red)); return b[:len(b)/2] }, wantMessage: "PNG, JPEG or GIF"},
		{name: "webp is not accepted", data: func(t *testing.T) []byte {
			return []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00\x30\x01\x00\x9d\x01\x2a\x10\x00\x10\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
		}, wantMessage: "PNG, JPEG or GIF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProfileEnv(t)
			got, err := p.svc.SetAvatar(t.Context(), actor(p.alice), bytes.NewReader(tt.data(t)))
			if tt.wantMessage != "" {
				appErr := requireTag(t, err, app.TagInvalidInput, "Accounts.SetAvatar->makeAvatar.InvalidInput")
				if msg := fieldsOf(t, appErr)[app.FieldAvatar]; !strings.Contains(msg, tt.wantMessage) {
					t.Errorf("message = %q, want it to mention %q", msg, tt.wantMessage)
				}
				row, _ := p.st.UserByID(t.Context(), p.alice.ID)
				if row.AvatarKey != "" {
					t.Errorf("a refused picture left the key %q", row.AvatarKey)
				}
				if entries, _ := os.ReadDir(p.dir); len(entries) != 1 { // only tmp
					t.Errorf("a refused picture left files: %v", entries)
				}
				if n := len(p.rec.Events()); n != 0 {
					t.Errorf("a refused picture published %d events", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetAvatar: %+v", err)
			}
			if len(got.AvatarKey) != 64 || got.AvatarURL() != "/avatars/"+itoa(p.alice.ID)+"?v="+got.AvatarKey {
				t.Errorf("user = %+v, url %q", got, got.AvatarURL())
			}
			img, key, raw := p.storedAvatar(p.alice)
			if key != got.AvatarKey {
				t.Errorf("served key = %q, want %q", key, got.AvatarKey)
			}
			if b := img.Bounds(); b.Dx() != 256 || b.Dy() != 256 {
				t.Errorf("stored size = %v, want 256x256", b)
			}
			if cfg, format, err := image.DecodeConfig(bytes.NewReader(raw)); err != nil || format != "png" || cfg.Width != 256 || cfg.Height != 256 {
				t.Errorf("stored file = %+v %q %v, want a 256x256 png", cfg, format, err)
			}
			if tt.check != nil {
				tt.check(t, img)
			}
		})
	}
}

func itoa(id uint64) string { return strconv.FormatUint(id, 10) }

func TestSetAvatarReplacesAndRemoves(t *testing.T) {
	t.Parallel()
	p := newProfileEnv(t)
	ctx := t.Context()
	first, err := p.svc.SetAvatar(ctx, actor(p.alice), bytes.NewReader(pngBytes(t, solid(20, 20, color.NRGBA{R: 255, A: 255}))))
	if err != nil {
		t.Fatalf("SetAvatar: %+v", err)
	}
	if ev := p.rec.Events(); len(ev) != 1 || ev[0].Kind != live.KindUserUpdated || ev[0].Topic != live.UserTopic(p.alice.ID) {
		t.Errorf("events = %+v, want one user.updated on the user's topic", ev)
	}
	second, err := p.svc.SetAvatar(ctx, actor(p.alice), bytes.NewReader(pngBytes(t, solid(20, 20, color.NRGBA{B: 255, A: 255}))))
	if err != nil {
		t.Fatal(err)
	}
	if first.AvatarKey == second.AvatarKey {
		t.Fatal("a new avatar kept the old key: its address would not change")
	}
	if _, err := p.avatars.Open(first.AvatarKey); err == nil {
		t.Error("the file of the replaced avatar is still there")
	}
	if _, err := p.avatars.Open(second.AvatarKey); err != nil {
		t.Errorf("the file of the new avatar is gone: %v", err)
	}
	if other, _ := p.st.UserByID(ctx, p.bob.ID); other.AvatarKey != "" {
		t.Errorf("another user got the avatar: %q", other.AvatarKey)
	}

	p.rec.Reset()
	removed, err := p.svc.RemoveAvatar(ctx, actor(p.alice))
	if err != nil || removed.AvatarKey != "" {
		t.Fatalf("RemoveAvatar = %+v, %v", removed, err)
	}
	if _, err := p.avatars.Open(second.AvatarKey); err == nil {
		t.Error("the file of the removed avatar is still there")
	}
	if ev := p.rec.Events(); len(ev) != 1 || ev[0].Kind != live.KindUserUpdated {
		t.Errorf("events = %+v, want one user.updated", ev)
	}
	_, _, err = p.svc.OpenAvatar(ctx, actor(p.bob), p.alice.ID)
	_ = requireTag(t, err, app.TagNotFound, "Accounts.OpenAvatar.NotFound")

	// Removing again is not an error and publishes nothing.
	p.rec.Reset()
	if _, err := p.svc.RemoveAvatar(ctx, actor(p.alice)); err != nil {
		t.Errorf("second RemoveAvatar: %+v", err)
	}
	if n := len(p.rec.Events()); n != 0 {
		t.Errorf("removing nothing published %d events", n)
	}
}

func TestAvatarPermissions(t *testing.T) {
	t.Parallel()
	img := pngBytes(t, solid(8, 8, color.NRGBA{R: 255, A: 255}))
	for _, c := range callers() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := newProfileEnv(t)
			u := c.user(p)
			_, err := p.svc.SetAvatar(t.Context(), actor(u), bytes.NewReader(img))
			if c.wantRoute != "" {
				_ = requireTag(t, err, app.TagUnauthenticated, fmtRoute(c.wantRoute, "SetAvatar"))
				_, err = p.svc.RemoveAvatar(t.Context(), actor(u))
				_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.RemoveAvatar.func1->Accounts.activeUserOn.Unauthenticated")
				_, _, err = p.svc.OpenAvatar(t.Context(), actor(u), p.alice.ID)
				_ = requireTag(t, err, app.TagUnauthenticated, fmtRoute(c.wantRoute, "OpenAvatar"))
				if entries, _ := os.ReadDir(p.dir); len(entries) != 1 {
					t.Errorf("a refused avatar left files: %v", entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetAvatar: %+v", err)
			}
			// Any logged-in user reads any avatar, even of a disabled user; an
			// unknown user is not found.
			f, _, err := p.svc.OpenAvatar(t.Context(), actor(p.bob), u.ID)
			if err != nil {
				t.Fatalf("OpenAvatar by another user: %+v", err)
			}
			_ = f.Close()
			_, _, err = p.svc.OpenAvatar(t.Context(), actor(p.bob), 99999)
			_ = requireTag(t, err, store.TagNotFound, "Accounts.OpenAvatar->Store.UserByID.NotFound")
			// Writing is only for oneself: the other users have none.
			if row, _ := p.st.UserByID(t.Context(), p.bob.ID); row.AvatarKey != "" && u.ID != p.bob.ID {
				t.Errorf("bob got an avatar from %s", c.name)
			}
		})
	}
}

func TestAvatarFailures(t *testing.T) {
	t.Parallel()
	img := pngBytes(t, solid(8, 8, color.NRGBA{R: 255, A: 255}))
	t.Run("no store for avatars", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		alice := e.signUp("alice", store.RoleMember)
		_, err := e.svc.SetAvatar(t.Context(), actor(alice), bytes.NewReader(img))
		_ = requireTag(t, err, "AvatarsUnavailable", "Accounts.SetAvatar.AvatarsUnavailable")
		_, _, err = e.svc.OpenAvatar(t.Context(), actor(alice), alice.ID)
		_ = requireTag(t, err, app.TagNotFound, "Accounts.OpenAvatar.NotFound")
		if _, err := e.svc.RemoveAvatar(t.Context(), actor(alice)); err != nil {
			t.Errorf("RemoveAvatar without a store: %+v", err)
		}
	})
	t.Run("reader fails", func(t *testing.T) {
		t.Parallel()
		p := newProfileEnv(t)
		_, err := p.svc.SetAvatar(t.Context(), actor(p.alice), failingReader{})
		_ = requireTag(t, err, "AvatarReadFailed", "Accounts.SetAvatar->makeAvatar.AvatarReadFailed")
	})
	t.Run("canceled", func(t *testing.T) {
		t.Parallel()
		p := newProfileEnv(t)
		_, err := p.svc.SetAvatar(canceledCtx(t), actor(p.alice), bytes.NewReader(img))
		_ = requireTag(t, err, store.TagQueryFailed, "Accounts.SetAvatar->Accounts.activeUser")
		_, err = p.svc.RemoveAvatar(canceledCtx(t), actor(p.alice))
		_ = requireTag(t, err, "TransactionFailed", "Accounts.RemoveAvatar->Store.Tx.TransactionFailed")
		_, _, err = p.svc.OpenAvatar(canceledCtx(t), actor(p.alice), p.alice.ID)
		_ = requireTag(t, err, store.TagQueryFailed, "Accounts.OpenAvatar->Accounts.activeUser")
	})
	t.Run("the file is gone", func(t *testing.T) {
		t.Parallel()
		p := newProfileEnv(t)
		u, err := p.svc.SetAvatar(t.Context(), actor(p.alice), bytes.NewReader(img))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.avatars.Remove(u.AvatarKey); err != nil {
			t.Fatal(err)
		}
		_, _, err = p.svc.OpenAvatar(t.Context(), actor(p.bob), p.alice.ID)
		_ = requireTag(t, err, app.TagNotFound, "Accounts.OpenAvatar.NotFound")
	})
	t.Run("the key is not a file key", func(t *testing.T) {
		t.Parallel()
		p := newProfileEnv(t)
		if err := p.st.SetUserAvatarKey(t.Context(), p.alice.ID, "../../etc/passwd"); err != nil {
			t.Fatal(err)
		}
		_, _, err := p.svc.OpenAvatar(t.Context(), actor(p.bob), p.alice.ID)
		_ = requireTag(t, err, files.TagBadKey, "Accounts.OpenAvatar")
	})
}

// failingReader fails every read.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

func TestAvatarURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   uint64
		key  string
		want string
	}{
		{"none", 3, "", ""},
		{"with a key", 3, "abc", "/avatars/3?v=abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := app.AvatarPath(tt.id, tt.key); got != tt.want {
				t.Errorf("AvatarPath = %q, want %q", got, tt.want)
			}
			if got := (app.User{ID: tt.id, AvatarKey: tt.key}).AvatarURL(); got != tt.want {
				t.Errorf("User.AvatarURL = %q, want %q", got, tt.want)
			}
			if got := (app.UserRef{ID: tt.id, AvatarKey: tt.key}).AvatarURL(); got != tt.want {
				t.Errorf("UserRef.AvatarURL = %q, want %q", got, tt.want)
			}
			if got := (app.Member{UserID: tt.id, AvatarKey: tt.key}).AvatarURL(); got != tt.want {
				t.Errorf("Member.AvatarURL = %q, want %q", got, tt.want)
			}
		})
	}
}
