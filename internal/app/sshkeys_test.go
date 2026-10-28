package app_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
	"golang.org/x/crypto/ssh"
)

// Public keys of the types that cannot be made with the standard library, as
// the lines of a .pub file. They are fixed so that the test does not depend on
// ssh-keygen. dsaLine is a 1024-bit ssh-dss key.
const (
	dsaLine       = "ssh-dss AAAAB3NzaC1kc3MAAACBALHTz0WfOvQDhAV/rIMDI/fgfPPdXeiXkyncZ/NsILm5P9ZSiockiR4satpB3dLxqyymbO7jUdEXLd41hrnVTpNTQmK4m6RnScbcKn9AF8xuNX99/PvLpIFwTIqPIbsADuPU4OJ6pV6pQy+aI13a51DbXoWbQZB1DCNg9uQkkXApAAAAFQCPvp5V2waOI4f0b0dICn0wXyl4TwAAAIBEhgS2eJQ7cQyoal+VOzGKXB4l6LHx2mltXbNVjFDAvqpSyodJNAReHXK/1dc59+Ua4o0N4PTSVt0QcJtXXSm5nDiSDeb7l77b8JCrruwpjw9iIcu13ONv1WKaecO/04G33mghHY7M5OpmKkkgPsbsjJfR/hCS0uihGpa66vd45AAAAIEAj8kG1d4zQ4d86XfheSiEsn8EBDWqUZhKHCPRbzuB2XZQj0HHpKloGLi/0QLRbVlPc8MpXoWylQerwyoTdWOI3Y7pKCozilYxcUyUbO1WWMzy3dS6IeoX93kFP5Jeg7u4p1HVTL8m5/CqY6TH3QMPlLx2BHKklaIdteIzkaAjqzI= old@host"
	skEd25519Line = "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIH5pvqGW/eu0bmPl9E6Wlx6daTyICiTW/Hd/06O8Un3XAAAABHNzaDo= yubikey"
	skECDSALine   = "sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItbmlzdHAyNTZAb3BlbnNzaC5jb20AAAAIbmlzdHAyNTYAAABBBEccm3SIVXNepOBIx76z/TTOnkGqBfuOLlaTaFvvqgEvoQ1fhhM0/0IGPZ5lqyvxIssPVZ/18jSjF9miS0Bqs8oAAAAEc3NoOg== yubikey"
)

// rsaKeys makes the RSA keys of the tests once: generating them is slow.
var rsaKeys = sync.OnceValue(func() map[int]*rsa.PrivateKey {
	out := map[int]*rsa.PrivateKey{}
	for _, bits := range []int{2048, 3072} {
		k, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			panic(err)
		}
		out[bits] = k
	}
	return out
})

// lineOf returns the authorized-keys line of a public key with a comment.
func lineOf(t testing.TB, pub any, comment string) string {
	t.Helper()
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " " + comment
}

// newEd25519Line makes a new ed25519 key and returns its line.
func newEd25519Line(t testing.TB) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return lineOf(t, pub, "laptop")
}

// ecdsaLine makes a new ECDSA key on the curve and returns its line.
func ecdsaLine(t testing.TB, curve elliptic.Curve) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return lineOf(t, &k.PublicKey, "ecdsa")
}

// sshEnv is a profileEnv with the SSH keys service on the same accounts.
type sshEnv struct {
	*profileEnv
	keys *app.SSHKeys
	sign *app.SignIn
}

func newSSHEnv(t *testing.T) *sshEnv {
	t.Helper()
	p := newProfileEnv(t)
	return &sshEnv{
		profileEnv: p,
		keys: app.NewSSHKeys(p.st, p.clk, p.svc, app.SSHServerInfo{
			Addr: ":2222", PublicHost: "chat.example.org", HostFingerprint: "SHA256:host",
		}),
		sign: app.NewSignIn(p.st, p.clk, nil),
	}
}

// add adds a key as the user and fails the test on an error.
func (e *sshEnv) add(u app.User, name, line string) app.SSHKey {
	e.t.Helper()
	k, err := e.keys.Add(e.t.Context(), actor(u), name, line, goodPassword, loginIP)
	if err != nil {
		e.t.Fatalf("Add %s: %+v", name, err)
	}
	return k
}

func TestSSHKeysAddTypes(t *testing.T) {
	t.Parallel()
	rsa2048 := lineOf(t, &rsaKeys()[2048].PublicKey, "small")
	rsa3072 := lineOf(t, &rsaKeys()[3072].PublicKey, "big")
	fields := strings.Fields(newEd25519Line(t))
	withoutComment := fields[0] + " " + fields[1]
	tests := []struct {
		name     string
		line     string
		wantType string
		wantTag  string
		route    string
	}{
		{"ed25519", newEd25519Line(t), "ssh-ed25519", "", ""},
		{"ecdsa p256", ecdsaLine(t, elliptic.P256()), "ecdsa-sha2-nistp256", "", ""},
		{"ecdsa p384", ecdsaLine(t, elliptic.P384()), "ecdsa-sha2-nistp384", "", ""},
		{"ecdsa p521", ecdsaLine(t, elliptic.P521()), "ecdsa-sha2-nistp521", "", ""},
		{"security key ed25519", skEd25519Line, "sk-ssh-ed25519@openssh.com", "", ""},
		{"security key ecdsa", skECDSALine, "sk-ecdsa-sha2-nistp256@openssh.com", "", ""},
		{"rsa 3072", rsa3072, "ssh-rsa", "", ""},
		{"without a comment", withoutComment, "ssh-ed25519", "", ""},
		{"with a newline", newEd25519Line(t) + "\n", "ssh-ed25519", "", ""},
		{"rsa 2048", rsa2048, "", app.TagSSHKeyTooShort, "SSHKeys.Add.SSHKeyTooShort"},
		{"dsa", dsaLine, "", app.TagSSHKeyTypeRefused, "SSHKeys.Add.SSHKeyTypeRefused"},
		{"empty", "", "", app.TagInvalidInput, "SSHKeys.Add.InvalidInput"},
		{"garbage", "this is not a key", "", app.TagInvalidInput, "SSHKeys.Add.InvalidInput"},
		{"truncated", strings.Fields(newEd25519Line(t))[0] + " AAAA", "", app.TagInvalidInput, "SSHKeys.Add.InvalidInput"},
		{"options before the key", `command="ls" ` + newEd25519Line(t), "", app.TagInvalidInput, "SSHKeys.Add.InvalidInput"},
		{"two keys", newEd25519Line(t) + "\n" + newEd25519Line(t), "", app.TagInvalidInput, "SSHKeys.Add.InvalidInput"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newSSHEnv(t)
			got, err := e.keys.Add(t.Context(), actor(e.alice), "key", tt.line, goodPassword, loginIP)
			if tt.wantTag != "" {
				appErr := requireTag(t, err, tt.wantTag, tt.route)
				if f := fieldsOf(t, appErr); f[app.FieldPublicKey] == "" {
					t.Errorf("fields = %v, want a message for publicKey", f)
				}
				if n := len(e.auditUserEvents()); n != 0 {
					t.Errorf("a refused key wrote %d audit events", n)
				}
				keys, _ := e.keys.List(t.Context(), actor(e.alice))
				if len(keys) != 0 {
					t.Errorf("a refused key was stored: %+v", keys)
				}
				return
			}
			if err != nil {
				t.Fatalf("Add: %+v", err)
			}
			if got.Type != tt.wantType || got.Name != "key" || got.ID == 0 || got.LastUsedAt != nil || !strings.HasPrefix(got.Fingerprint, "SHA256:") {
				t.Errorf("key = %+v", got)
			}
		})
	}
}

func TestSSHKeysAddStoresTheNormalisedLine(t *testing.T) {
	t.Parallel()
	e := newSSHEnv(t)
	line := newEd25519Line(t)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	k := e.add(e.alice, "  laptop  ", "  "+line+"  ")
	row, err := e.st.SSHKeyByID(t.Context(), k.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if row.PublicKey != want || strings.Contains(row.PublicKey, "laptop") || row.Name != "laptop" ||
		row.Fingerprint != ssh.FingerprintSHA256(pub) || row.UserID != e.alice.ID {
		t.Errorf("row = %+v, want line %q", row, want)
	}
}

func TestSSHKeysAddNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"one character", "a", true},
		{"40 characters", strings.Repeat("é", 40), true},
		{"41 characters", strings.Repeat("é", 41), false},
		{"empty", "", false},
		{"spaces", "   ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newSSHEnv(t)
			_, err := e.keys.Add(t.Context(), actor(e.alice), tt.in, newEd25519Line(t), goodPassword, loginIP)
			if tt.ok {
				if err != nil {
					t.Fatalf("Add: %+v", err)
				}
				return
			}
			appErr := requireTag(t, err, app.TagInvalidInput, "SSHKeys.Add.InvalidInput")
			if f := fieldsOf(t, appErr); f[app.FieldName] == "" {
				t.Errorf("fields = %v, want a message for name", f)
			}
		})
	}
}

func TestSSHKeysAddPassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
	}{
		{"wrong", "not the password"},
		{"empty", ""},
		{"huge", strings.Repeat("a", 2000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newSSHEnv(t)
			_, err := e.keys.Add(t.Context(), actor(e.alice), "k", newEd25519Line(t), tt.password, loginIP)
			appErr := requireTag(t, err, app.TagInvalidInput, "SSHKeys.Add.InvalidInput")
			if f := fieldsOf(t, appErr); f[app.FieldCurrentPassword] == "" {
				t.Errorf("fields = %v, want a message for currentPassword", f)
			}
			if keys, _ := e.keys.List(t.Context(), actor(e.alice)); len(keys) != 0 {
				t.Errorf("a key was stored: %+v", keys)
			}
			if n := len(e.auditUserEvents()); n != 0 {
				t.Errorf("a failed add wrote %d audit events", n)
			}
		})
	}
}

func TestSSHKeysCountInTheLimiter(t *testing.T) {
	t.Parallel()
	e := newSSHEnv(t)
	ctx := t.Context()
	const ip = "198.51.100.7"
	for range 5 {
		_, err := e.keys.Add(ctx, actor(e.alice), "k", newEd25519Line(t), "wrong password", ip)
		_ = requireTag(t, err, app.TagInvalidInput, "SSHKeys.Add.InvalidInput")
	}
	// The fifth failure blocks the username: the right password waits too, for
	// adding and for removing, and a login is blocked as well.
	_, err := e.keys.Add(ctx, actor(e.alice), "k", newEd25519Line(t), goodPassword, "198.51.100.8")
	_ = requireTag(t, err, app.TagRateLimited, "SSHKeys.Add.RateLimited")
	err = e.keys.Remove(ctx, actor(e.alice), 1, goodPassword, "198.51.100.8")
	_ = requireTag(t, err, app.TagRateLimited, "SSHKeys.Remove.RateLimited")
	_, _, err = e.svc.Login(ctx, "alice", goodPassword, "203.0.113.9")
	_ = requireTag(t, err, app.TagRateLimited, "Accounts.LoginClient.RateLimited")
	// Another user is not affected.
	if _, err := e.keys.Add(ctx, actor(e.bob), "k", newEd25519Line(t), goodPassword, "203.0.113.20"); err != nil {
		t.Errorf("another user: %+v", err)
	}
	e.clk.Advance(16 * time.Minute)
	if _, err := e.keys.Add(ctx, actor(e.alice), "k", newEd25519Line(t), goodPassword, "198.51.100.8"); err != nil {
		t.Errorf("after the window: %+v", err)
	}
}

func TestSSHKeysAddDuplicate(t *testing.T) {
	t.Parallel()
	line := newEd25519Line(t)
	tests := []struct {
		name string
		by   func(e *sshEnv) app.User
	}{
		{"the same user", func(e *sshEnv) app.User { return e.alice }},
		{"another user", func(e *sshEnv) app.User { return e.bob }},
	}
	var messages []string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSSHEnv(t)
			e.add(e.alice, "first", line)
			before := len(e.auditUserEvents())
			_, err := e.keys.Add(t.Context(), actor(tt.by(e)), "again", line, goodPassword, loginIP)
			appErr := requireTag(t, err, app.TagConflict, "SSHKeys.Add.func1.Conflict")
			f := fieldsOf(t, appErr)
			messages = append(messages, f[app.FieldPublicKey])
			if f[app.FieldPublicKey] == "" || strings.Contains(strings.ToLower(f[app.FieldPublicKey]), "alice") {
				t.Errorf("fields = %v", f)
			}
			if n := len(e.auditUserEvents()); n != before {
				t.Errorf("a refused duplicate wrote an audit event")
			}
		})
	}
	if len(messages) == 2 && messages[0] != messages[1] {
		t.Errorf("the message depends on who owns the key: %q, %q", messages[0], messages[1])
	}
}

func TestSSHKeysAddLimit(t *testing.T) {
	t.Parallel()
	e := newSSHEnv(t)
	for i := range app.SSHKeyMaxPerUser {
		e.add(e.alice, "key "+strconv.Itoa(i), newEd25519Line(t))
	}
	_, err := e.keys.Add(t.Context(), actor(e.alice), "eleventh", newEd25519Line(t), goodPassword, loginIP)
	appErr := requireTag(t, err, app.TagConflict, "SSHKeys.Add.func1.Conflict")
	if f := fieldsOf(t, appErr); f[app.FieldPublicKey] == "" {
		t.Errorf("fields = %v", f)
	}
	// Another user has their own ten, and a removed key makes room.
	e.add(e.bob, "bob's", newEd25519Line(t))
	keys, _ := e.keys.List(t.Context(), actor(e.alice))
	if len(keys) != 10 {
		t.Fatalf("%d keys, want 10", len(keys))
	}
	if err := e.keys.Remove(t.Context(), actor(e.alice), keys[0].ID, goodPassword, loginIP); err != nil {
		t.Fatalf("Remove: %+v", err)
	}
	e.add(e.alice, "eleventh", newEd25519Line(t))
}

func TestSSHKeysAudit(t *testing.T) {
	t.Parallel()
	e := newSSHEnv(t)
	line := newEd25519Line(t)
	k := e.add(e.alice, "work laptop", line)
	if err := e.keys.Remove(t.Context(), actor(e.alice), k.ID, goodPassword, loginIP); err != nil {
		t.Fatalf("Remove: %+v", err)
	}
	events := e.auditUserEvents()
	if len(events) != 2 {
		t.Fatalf("%d audit events, want 2", len(events))
	}
	removed, added := events[0], events[1]
	if added.Action != store.ActionUserSSHKeyAdded || removed.Action != store.ActionUserSSHKeyRemoved {
		t.Errorf("actions = %s, %s", added.Action, removed.Action)
	}
	for _, ev := range events {
		body := strings.Fields(line)[1]
		if ev.ActorID != e.alice.ID || ev.EntityID != e.alice.ID || ev.RoomID != store.NoRoom || ev.EntityType != store.EntityUser ||
			!strings.Contains(ev.Summary, "work laptop") || !strings.Contains(ev.Summary, k.Fingerprint) ||
			!strings.Contains(ev.Details, k.Fingerprint) || strings.Contains(ev.Summary+ev.Details, body) {
			t.Errorf("event = %+v", ev)
		}
	}
	if !strings.HasPrefix(added.Summary, "Alice added the SSH key") || !strings.HasPrefix(removed.Summary, "Alice removed the SSH key") {
		t.Errorf("summaries = %q, %q", added.Summary, removed.Summary)
	}
}

func TestSSHKeysRemove(t *testing.T) {
	t.Parallel()
	e := newSSHEnv(t)
	mine := e.add(e.alice, "mine", newEd25519Line(t))
	theirs := e.add(e.bob, "theirs", newEd25519Line(t))
	ctx := t.Context()
	before := len(e.auditUserEvents())

	tests := []struct {
		name     string
		id       uint64
		password string
		tag      string
		route    string
	}{
		{"another user's key", theirs.ID, goodPassword, app.TagNotFound, "SSHKeys.Remove.func1.NotFound"},
		{"unknown key", 9999, goodPassword, app.TagNotFound, "SSHKeys.Remove.func1->Store.SSHKeyByID.NotFound"},
		{"wrong password", mine.ID, "nope", app.TagInvalidInput, "SSHKeys.Remove.InvalidInput"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := e.keys.Remove(ctx, actor(e.alice), tt.id, tt.password, loginIP)
			_ = requireTag(t, err, tt.tag, tt.route)
		})
	}
	if n := len(e.auditUserEvents()); n != before {
		t.Errorf("failed removals wrote %d events", n-before)
	}
	if keys, _ := e.keys.List(ctx, actor(e.bob)); len(keys) != 1 {
		t.Errorf("the key of bob was touched: %+v", keys)
	}
	if err := e.keys.Remove(ctx, actor(e.alice), mine.ID, goodPassword, loginIP); err != nil {
		t.Fatalf("Remove: %+v", err)
	}
	if keys, _ := e.keys.List(ctx, actor(e.alice)); len(keys) != 0 {
		t.Errorf("keys after removal: %+v", keys)
	}
	// Removing it again: gone.
	err := e.keys.Remove(ctx, actor(e.alice), mine.ID, goodPassword, loginIP)
	_ = requireTag(t, err, app.TagNotFound, "SSHKeys.Remove")
}

func TestSSHKeysList(t *testing.T) {
	t.Parallel()
	e := newSSHEnv(t)
	first := e.add(e.alice, "first", newEd25519Line(t))
	second := e.add(e.alice, "second", newEd25519Line(t))
	e.add(e.bob, "bob's", newEd25519Line(t))
	keys, err := e.keys.List(t.Context(), actor(e.alice))
	if err != nil || len(keys) != 2 || keys[0].ID != first.ID || keys[1].ID != second.ID {
		t.Fatalf("List = %+v, %v", keys, err)
	}
	if keys[0].CreatedAt.IsZero() || keys[0].LastUsedAt != nil {
		t.Errorf("key = %+v", keys[0])
	}
	none, err := e.keys.List(t.Context(), actor(e.admin))
	if err != nil || len(none) != 0 {
		t.Errorf("the admin sees keys of others: %+v, %v", none, err)
	}
}

// TestSSHKeysPermissions is the table of who may call Add, Remove and List.
// Everyone with an enabled account manages their own keys; an admin has no
// power over those of others; a disabled user is not logged in.
func TestSSHKeysPermissions(t *testing.T) {
	t.Parallel()
	const disabled = "SSHKeys.%s->Accounts.activeUser->Accounts.activeUserOn.Unauthenticated"
	for _, c := range callers() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newSSHEnv(t)
			u := c.user(e.profileEnv)
			ctx := t.Context()
			// The user of every caller has a key made through the store, so
			// that the disabled user has one to remove.
			row := &store.SSHKey{UserID: u.ID, Name: "own", Type: "ssh-ed25519", Fingerprint: "SHA256:own" + u.Username, PublicKey: "ssh-ed25519 AAAA"}
			if err := e.st.CreateSSHKey(ctx, row); err != nil {
				t.Fatal(err)
			}
			_, listErr := e.keys.List(ctx, actor(u))
			_, addErr := e.keys.Add(ctx, actor(u), "new", newEd25519Line(t), goodPassword, loginIP)
			removeErr := e.keys.Remove(ctx, actor(u), row.ID, goodPassword, loginIP)
			if c.wantRoute != "" {
				_ = requireTag(t, listErr, app.TagUnauthenticated, fmtRoute(disabled, "List"))
				_ = requireTag(t, addErr, app.TagUnauthenticated, fmtRoute(disabled, "Add"))
				_ = requireTag(t, removeErr, app.TagUnauthenticated, fmtRoute(disabled, "Remove"))
				return
			}
			for name, err := range map[string]error{"List": listErr, "Add": addErr, "Remove": removeErr} {
				if err != nil {
					t.Errorf("%s: %+v", name, err)
				}
			}
		})
	}
}

func TestSSHKeysServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		info app.SSHServerInfo
		want app.SSHServer
	}{
		{"off", app.SSHServerInfo{}, app.SSHServer{}},
		{"a port", app.SSHServerInfo{Addr: ":2222", PublicHost: "chat.example.org", HostFingerprint: "SHA256:x"},
			app.SSHServer{Enabled: true, HostFingerprint: "SHA256:x", Command: "ssh -p 2222 alice@chat.example.org"}},
		{"port 22", app.SSHServerInfo{Addr: "0.0.0.0:22", PublicHost: "chat.example.org", HostFingerprint: "SHA256:x"},
			app.SSHServer{Enabled: true, HostFingerprint: "SHA256:x", Command: "ssh alice@chat.example.org"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			got := app.NewSSHKeys(e.st, e.clk, e.svc, tt.info).Server("alice")
			if got != tt.want {
				t.Errorf("Server = %+v, want %+v", got, tt.want)
			}
		})
	}
}
