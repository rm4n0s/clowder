package web

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"golang.org/x/crypto/ssh"
)

// newKeyLine makes a new ed25519 key and returns the line of its .pub file.
func newKeyLine(t testing.TB) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " alice@laptop"
}

// dsaKeyLine is the line of an ssh-dss key, a type Clowder does not accept.
const dsaKeyLine = "ssh-dss AAAAB3NzaC1kc3MAAACBALHTz0WfOvQDhAV/rIMDI/fgfPPdXeiXkyncZ/NsILm5P9ZSiockiR4satpB3dLxqyymbO7jUdEXLd41hrnVTpNTQmK4m6RnScbcKn9AF8xuNX99/PvLpIFwTIqPIbsADuPU4OJ6pV6pQy+aI13a51DbXoWbQZB1DCNg9uQkkXApAAAAFQCPvp5V2waOI4f0b0dICn0wXyl4TwAAAIBEhgS2eJQ7cQyoal+VOzGKXB4l6LHx2mltXbNVjFDAvqpSyodJNAReHXK/1dc59+Ua4o0N4PTSVt0QcJtXXSm5nDiSDeb7l77b8JCrruwpjw9iIcu13ONv1WKaecO/04G33mghHY7M5OpmKkkgPsbsjJfR/hCS0uihGpa66vd45AAAAIEAj8kG1d4zQ4d86XfheSiEsn8EBDWqUZhKHCPRbzuB2XZQj0HHpKloGLi/0QLRbVlPc8MpXoWylQerwyoTdWOI3Y7pKCozilYxcUyUbO1WWMzy3dS6IeoX93kFP5Jeg7u4p1HVTL8m5/CqY6TH3QMPlLx2BHKklaIdteIzkaAjqzI="

// sshSettingsEnv is a settingsEnv whose server shows the SSH server.
func newSSHSettingsEnv(t *testing.T, on bool) *settingsEnv {
	t.Helper()
	var mutate func(*config.Config)
	if on {
		mutate = func(c *config.Config) {
			c.SSHAddr = ":2222"
			c.SSHPublicHost = "chat.example.org"
		}
	}
	e := newTestEnv(t, mutate)
	s := &settingsEnv{testEnv: e}
	s.alice = e.addUser("alice", "Alice", store.RoleMember)
	s.bob = e.addUser("bob", "Bob", store.RoleMember)
	s.token = e.session("alice")
	return s
}

// addKey adds a key for the user through the service.
func (e *settingsEnv) addKey(u app.User, name string) app.SSHKey {
	e.t.Helper()
	k, err := e.sshKeys.Add(e.t.Context(), app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}, name, newKeyLine(e.t), testPassword, "192.0.2.1")
	if err != nil {
		e.t.Fatalf("Add: %+v", err)
	}
	return k
}

func TestSettingsSignInSection(t *testing.T) {
	t.Parallel()
	t.Run("server on, no keys", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		rec := e.send(get("/me/settings", withCookie(e.token)))
		requireBody(t, rec, []string{
			`<a href="#signin">Sign-in</a>`, `<section id="signin" class="settings-section">`,
			"You have no SSH keys.", `<form id="ssh-key-add-form" method="post" action="/me/settings/ssh-keys"`,
			`name="name"`, `name="publicKey"`, `name="currentPassword"`,
			`<code id="ssh-command" class="ssh-command">ssh -p 2222 alice@chat.example.org</code>`,
			`<code id="ssh-host-fingerprint">` + testHostFingerprint + `</code>`,
		}, "The SSH server is not running", "TODO")
	})
	t.Run("server off", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, false)
		rec := e.send(get("/me/settings", withCookie(e.token)))
		requireBody(t, rec, []string{
			`<section id="signin" class="settings-section">`, "The SSH server is not running on this Clowder.",
			`<form id="ssh-key-add-form"`,
		}, `id="ssh-command"`, "ssh -p", testHostFingerprint)
	})
	t.Run("the keys of the user only", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		mine := e.addKey(e.alice, "my laptop")
		theirs := e.addKey(e.bob, "bob's phone")
		rec := e.send(get("/me/settings", withCookie(e.token)))
		requireBody(t, rec, []string{
			`<tr id="ssh-key-` + itoa(mine.ID) + `">`, "my laptop", "ssh-ed25519", mine.Fingerprint, "Never",
			`action="/me/settings/ssh-keys/` + itoa(mine.ID) + `/remove"`, `placeholder="Current password"`,
		}, "bob's phone", theirs.Fingerprint, "You have no SSH keys.")
		// The key line is never on the page.
		if strings.Contains(rec.Body.String(), "AAAA") {
			t.Error("the page holds a key line")
		}
	})
	t.Run("requires login", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		for _, req := range []*http.Request{
			postForm("/me/settings/ssh-keys", url.Values{"name": {"x"}}),
			postForm("/me/settings/ssh-keys/1/remove", url.Values{}),
		} {
			if rec := e.send(req); rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
				t.Errorf("%s without a session: status %d", req.URL.Path, rec.Code)
			}
		}
	})
}

// itoa formats an ID.
func itoa(id uint64) string { return strconv.FormatUint(id, 10) }

func TestSettingsSSHKeyAdd(t *testing.T) {
	t.Parallel()
	line := newKeyLine(t)
	tests := []struct {
		name     string
		form     url.Values
		opts     []reqOpt
		wantCode int
		want     []string
		notWant  []string
		wantKeys int
	}{
		{
			name: "datastar adds", form: url.Values{"name": {"laptop"}, "publicKey": {line}, "currentPassword": {testPassword}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, wantKeys: 1,
			want:    []string{`id="signin"`, `<p class="saved" role="status">Saved</p>`, "laptop", "SHA256:"},
			notWant: []string{testPassword, "AAAA"},
		},
		{
			name: "plain redirects back", form: url.Values{"name": {"laptop"}, "publicKey": {line}, "currentPassword": {testPassword}},
			wantCode: http.StatusSeeOther, wantKeys: 1,
		},
		{
			name: "wrong password", form: url.Values{"name": {"laptop"}, "publicKey": {line}, "currentPassword": {"wrong password!"}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want:    []string{`id="signin"`, "This is not your password.", `value="laptop"`, line},
			notWant: []string{"wrong password!", `Saved</p>`},
		},
		{
			name: "a name that is empty", form: url.Values{"name": {" "}, "publicKey": {line}, "currentPassword": {testPassword}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{"Use 1 to 40 characters.", line},
		},
		{
			name: "text that is not a key", form: url.Values{"name": {"k"}, "publicKey": {"hello"}, "currentPassword": {testPassword}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{"Paste the public key: the line of a .pub file."},
		},
		{
			name: "a dsa key", form: url.Values{"name": {"k"}, "publicKey": {dsaKeyLine}, "currentPassword": {testPassword}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{"This type of key is not accepted."},
		},
		{
			name: "plain: a dsa key shows the page with 422", form: url.Values{"name": {"k"}, "publicKey": {dsaKeyLine}, "currentPassword": {testPassword}},
			wantCode: http.StatusUnprocessableEntity,
			want:     []string{"<title>Settings · Clowder</title>", "This type of key is not accepted.", `id="sessions"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newSSHSettingsEnv(t, true)
			rec := e.send(postForm("/me/settings/ssh-keys", tt.form, append([]reqOpt{withCookie(e.token)}, tt.opts...)...))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d:\n%s", rec.Code, tt.wantCode, rec.Body)
			}
			requireBody(t, rec, tt.want, tt.notWant...)
			if tt.wantCode == http.StatusSeeOther && rec.Header().Get("Location") != "/me/settings?saved=signin#signin" {
				t.Errorf("Location = %q", rec.Header().Get("Location"))
			}
			if len(tt.opts) > 0 {
				requireSSE(t, rec)
			}
			keys, _ := e.sshKeys.List(t.Context(), app.Actor{UserID: e.alice.ID, Username: "alice", Role: store.RoleMember})
			if len(keys) != tt.wantKeys {
				t.Errorf("%d keys stored, want %d", len(keys), tt.wantKeys)
			}
		})
	}
}

func TestSettingsSSHKeyAddClashes(t *testing.T) {
	t.Parallel()
	t.Run("a key that is registered", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		line := newKeyLine(t)
		if _, err := e.sshKeys.Add(t.Context(), app.Actor{UserID: e.bob.ID, Username: "bob", Role: store.RoleMember}, "bobs", line, testPassword, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
		form := url.Values{"name": {"mine"}, "publicKey": {line}, "currentPassword": {testPassword}}
		rec := e.send(postForm("/me/settings/ssh-keys", form, withCookie(e.token), asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{"This key is already registered."}, "bob", "That clashes")
		rec = e.send(postForm("/me/settings/ssh-keys", form, withCookie(e.token)))
		if rec.Code != http.StatusConflict {
			t.Errorf("plain status = %d, want 409", rec.Code)
		}
		requireBody(t, rec, []string{"This key is already registered."})
	})
	t.Run("ten keys", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		for i := range app.SSHKeyMaxPerUser {
			e.addKey(e.alice, "key "+strconv.Itoa(i))
		}
		form := url.Values{"name": {"eleventh"}, "publicKey": {newKeyLine(t)}, "currentPassword": {testPassword}}
		rec := e.send(postForm("/me/settings/ssh-keys", form, withCookie(e.token), asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{"You have 10 keys already. Remove one first."})
	})
}

func TestSettingsSSHKeyRemove(t *testing.T) {
	t.Parallel()
	t.Run("removes", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		k := e.addKey(e.alice, "workstation")
		path := "/me/settings/ssh-keys/" + itoa(k.ID) + "/remove"
		rec := e.send(postForm(path, url.Values{"currentPassword": {testPassword}}, withCookie(e.token), asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="signin"`, "You have no SSH keys.", `<p class="saved" role="status">Saved</p>`}, "workstation")
		if keys, _ := e.sshKeys.List(t.Context(), app.Actor{UserID: e.alice.ID, Username: "alice"}); len(keys) != 0 {
			t.Errorf("keys = %+v", keys)
		}
		// A plain post is sent back to the section.
		k2 := e.addKey(e.alice, "phone")
		rec = e.send(postForm("/me/settings/ssh-keys/"+itoa(k2.ID)+"/remove", url.Values{"currentPassword": {testPassword}}, withCookie(e.token)))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/me/settings?saved=signin#signin" {
			t.Errorf("plain: %d %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		k := e.addKey(e.alice, "workstation")
		path := "/me/settings/ssh-keys/" + itoa(k.ID) + "/remove"
		rec := e.send(postForm(path, url.Values{"currentPassword": {"wrong password!"}}, withCookie(e.token), asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{"This is not your password.", "workstation"}, "wrong password!", "Saved</p>")
		if keys, _ := e.sshKeys.List(t.Context(), app.Actor{UserID: e.alice.ID, Username: "alice"}); len(keys) != 1 {
			t.Errorf("keys = %+v, want the key kept", keys)
		}
	})
	t.Run("another user's key", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		theirs := e.addKey(e.bob, "bob's")
		path := "/me/settings/ssh-keys/" + itoa(theirs.ID) + "/remove"
		rec := e.send(postForm(path, url.Values{"currentPassword": {testPassword}}, withCookie(e.token), asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{"You have no SSH keys."}, "Saved</p>", "bob's")
		if keys, _ := e.sshKeys.List(t.Context(), app.Actor{UserID: e.bob.ID, Username: "bob"}); len(keys) != 1 {
			t.Errorf("the key of bob was removed")
		}
	})
	t.Run("a path that is not an ID", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		for _, id := range []string{"0", "abc", "-1"} {
			rec := e.send(postForm("/me/settings/ssh-keys/"+id+"/remove", url.Values{"currentPassword": {testPassword}}, withCookie(e.token)))
			if rec.Code != http.StatusNotFound {
				t.Errorf("id %q: status %d, want 404", id, rec.Code)
			}
		}
	})
}
