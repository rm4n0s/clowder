package app_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/store"
	"golang.org/x/crypto/ssh"
)

// fingerprintOf returns the fingerprint of the key on the line.
func fingerprintOf(t testing.TB, line string) string {
	t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatalf("ParseAuthorizedKey: %v", err)
	}
	return ssh.FingerprintSHA256(pub)
}

// codeAlphabet is Crockford's base32.
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func TestNormalizeCode(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"K7QF9MZP", "K7QF9MZP"},
		{"K7QF-9MZP", "K7QF9MZP"},
		{"k7qf-9mzp", "K7QF9MZP"},
		{" k7qf 9mzp \n", "K7QF9MZP"},
		{"K7-QF-9M-ZP", "K7QF9MZP"},
		{"IL0O", "1100"},
		{"il0o", "1100"},
		{"", ""},
		{"---", ""},
	}
	for _, tt := range tests {
		if got := app.NormalizeCode(tt.in); got != tt.want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got, want := app.SSHCodeHash("k7qf-9mzp"), auth.HashToken("K7QF9MZP"); got != want {
		t.Errorf("SSHCodeHash = %q, want %q", got, want)
	}
}

func TestIssuedSSHCodeFormatted(t *testing.T) {
	t.Parallel()
	if got := (app.IssuedSSHCode{Code: "K7QF9MZP"}).Formatted(); got != "K7QF-9MZP" {
		t.Errorf("Formatted = %q", got)
	}
	if got := (app.IssuedSSHCode{Code: "odd"}).Formatted(); got != "odd" {
		t.Errorf("Formatted of a short code = %q", got)
	}
}

// signInFixture is an sshEnv where alice, bob and the disabled user have a
// key each.
type signInFixture struct {
	*sshEnv
	line        map[string]string
	fingerprint map[string]string
	logs        *syncBuffer
}

// syncBuffer is a bytes.Buffer safe for the logger of concurrent calls.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newSignInFixture(t *testing.T) *signInFixture {
	t.Helper()
	e := newSSHEnv(t)
	f := &signInFixture{sshEnv: e, line: map[string]string{}, fingerprint: map[string]string{}, logs: &syncBuffer{}}
	e.sign = app.NewSignIn(e.st, e.clk, slog.New(slog.NewJSONHandler(f.logs, nil)))
	for _, u := range []app.User{e.alice, e.bob, e.disabled, e.admin} {
		line := newEd25519Line(t)
		f.line[u.Username], f.fingerprint[u.Username] = line, fingerprintOf(t, line)
		row := &store.SSHKey{
			UserID: u.ID, Name: "key", Type: "ssh-ed25519", Fingerprint: f.fingerprint[u.Username], PublicKey: strings.Fields(line)[0] + " " + strings.Fields(line)[1],
		}
		if err := e.st.CreateSSHKey(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// TestKeyAllowed is the table of who may fetch a code: only the key of an
// enabled user, under that user's name. Every refusal looks the same.
func TestKeyAllowed(t *testing.T) {
	t.Parallel()
	f := newSignInFixture(t)
	tests := []struct {
		name     string
		username string
		key      string
		want     bool
	}{
		{"admin with their key", "root", "root", true},
		{"member with their key", "alice", "alice", true},
		{"upper case name", "  ALICE ", "alice", true},
		{"disabled user", "dina", "dina", false},
		{"another user's key", "alice", "bob", false},
		{"an unknown user with a known key", "nobody", "alice", false},
		{"an unknown key", "alice", "", false},
		{"an empty name", "", "alice", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fp := f.fingerprint[tt.key]
			if fp == "" {
				fp = "SHA256:unknown"
			}
			if got := f.sign.KeyAllowed(t.Context(), tt.username, fp); got != tt.want {
				t.Errorf("KeyAllowed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKeyAllowedLogsADatabaseFailure(t *testing.T) {
	t.Parallel()
	f := newSignInFixture(t)
	if f.sign.KeyAllowed(canceledContext(t), "alice", f.fingerprint["alice"]) {
		t.Error("a canceled call allowed the key")
	}
	if !strings.Contains(f.logs.String(), "QueryFailed") {
		t.Errorf("log = %q, want the failure with its tag", f.logs.String())
	}
	f.logs.mu.Lock()
	f.logs.buf.Reset()
	f.logs.mu.Unlock()
	f.sign.KeyAllowed(t.Context(), "alice", f.fingerprint["bob"])
	if f.logs.String() != "" {
		t.Errorf("a refusal was logged: %q", f.logs.String())
	}
}

func TestIssueCode(t *testing.T) {
	t.Parallel()
	f := newSignInFixture(t)
	ctx := t.Context()
	got, err := f.sign.IssueCode(ctx, "Alice", f.fingerprint["alice"], "192.0.2.1:5555")
	if err != nil {
		t.Fatalf("IssueCode: %+v", err)
	}
	if len(got.Code) != 8 || strings.Trim(got.Code, codeAlphabet) != "" {
		t.Errorf("code = %q, want 8 characters of Crockford's base32", got.Code)
	}
	if want := f.clk.Now().Add(60 * time.Second); !got.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want)
	}
	if got.Formatted() != got.Code[:4]+"-"+got.Code[4:] {
		t.Errorf("Formatted = %q", got.Formatted())
	}

	// The key was used and the code is stored as a hash only.
	row, err := f.st.SSHKeyByFingerprint(ctx, f.fingerprint["alice"])
	if err != nil || row.LastUsedAt == nil || !row.LastUsedAt.Equal(f.clk.Now()) {
		t.Errorf("key after the code = %+v, %v", row, err)
	}
	other, _ := f.st.SSHKeyByFingerprint(ctx, f.fingerprint["bob"])
	if other.LastUsedAt != nil {
		t.Errorf("the key of another user was used: %+v", other)
	}
	if n := f.countCodes(); n != 1 {
		t.Errorf("%d code rows, want 1", n)
	}

	// The log says who and from where, never the code; no audit event.
	log := f.logs.String()
	for _, want := range []string{`"user":"alice"`, f.fingerprint["alice"], "192.0.2.1:5555"} {
		if !strings.Contains(log, want) {
			t.Errorf("log %q lacks %q", log, want)
		}
	}
	if strings.Contains(log, got.Code) {
		t.Errorf("the log holds the code: %q", log)
	}
	if n := len(f.auditUserEvents()); n != 0 {
		t.Errorf("issuing a code wrote %d audit events", n)
	}

	// The code is usable once, under any case and dash, until it expires.
	at := f.clk.Now().Add(30 * time.Second)
	used, err := f.st.ConsumeSSHCode(ctx, f.alice.ID, app.SSHCodeHash(strings.ToLower(got.Formatted())), at)
	if err != nil || used.UsedAt == nil || used.UserID != f.alice.ID {
		t.Fatalf("ConsumeSSHCode = %+v, %v", used, err)
	}
	if _, err := f.st.ConsumeSSHCode(ctx, f.alice.ID, app.SSHCodeHash(got.Code), at); err == nil {
		t.Error("a code was used twice")
	}
}

// countCodes counts the rows of ssh_codes.
func (f *signInFixture) countCodes() int {
	f.t.Helper()
	n, err := f.st.CountSSHCodesSince(f.t.Context(), f.alice.ID, time.Time{})
	if err != nil {
		f.t.Fatal(err)
	}
	return int(n)
}

func TestIssueCodeExpiry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		after time.Duration
		valid bool
	}{
		{"at once", 0, true},
		{"59 seconds", 59 * time.Second, true},
		{"just before 60 seconds", 60*time.Second - time.Nanosecond, true},
		{"exactly 60 seconds", 60 * time.Second, false},
		{"a minute later", 2 * time.Minute, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSignInFixture(t)
			got, err := f.sign.IssueCode(t.Context(), "alice", f.fingerprint["alice"], "192.0.2.1:1")
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.st.ConsumeSSHCode(t.Context(), f.alice.ID, app.SSHCodeHash(got.Code), f.clk.Now().Add(tt.after))
			if (err == nil) != tt.valid {
				t.Errorf("after %v: err = %v, want valid %v", tt.after, err, tt.valid)
			}
		})
	}
}

func TestIssueCodeReplacesTheOlderOnes(t *testing.T) {
	t.Parallel()
	f := newSignInFixture(t)
	ctx := t.Context()
	first, err := f.sign.IssueCode(ctx, "alice", f.fingerprint["alice"], "a")
	if err != nil {
		t.Fatal(err)
	}
	bobs, err := f.sign.IssueCode(ctx, "bob", f.fingerprint["bob"], "b")
	if err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(5 * time.Second)
	second, err := f.sign.IssueCode(ctx, "alice", f.fingerprint["alice"], "a")
	if err != nil {
		t.Fatal(err)
	}
	if first.Code == second.Code {
		t.Fatal("two codes are the same")
	}
	now := f.clk.Now()
	if _, err := f.st.ConsumeSSHCode(ctx, f.alice.ID, app.SSHCodeHash(first.Code), now); err == nil {
		t.Error("the replaced code works")
	}
	if _, err := f.st.ConsumeSSHCode(ctx, f.bob.ID, app.SSHCodeHash(bobs.Code), now); err != nil {
		t.Errorf("the code of another user was replaced: %+v", err)
	}
	if _, err := f.st.ConsumeSSHCode(ctx, f.alice.ID, app.SSHCodeHash(second.Code), now); err != nil {
		t.Errorf("the newest code: %+v", err)
	}
}

func TestIssueCodeLimit(t *testing.T) {
	t.Parallel()
	f := newSignInFixture(t)
	ctx := t.Context()
	for i := range app.SSHCodeLimit {
		if _, err := f.sign.IssueCode(ctx, "alice", f.fingerprint["alice"], "a"); err != nil {
			t.Fatalf("code %d: %+v", i+1, err)
		}
		f.clk.Advance(time.Minute)
	}
	// Ten codes within the last ten minutes: the eleventh waits.
	_, err := f.sign.IssueCode(ctx, "alice", f.fingerprint["alice"], "a")
	appErr := requireTag(t, err, app.TagSSHCodeLimit, "SignIn.IssueCode.func1.SSHCodeLimit")
	if appErr.Metadata["userID"] != f.alice.ID {
		t.Errorf("metadata = %v", appErr.Metadata)
	}
	// Another user is not limited.
	if _, err := f.sign.IssueCode(ctx, "bob", f.fingerprint["bob"], "b"); err != nil {
		t.Errorf("another user: %+v", err)
	}
	// The first code was made 10 minutes ago; 5 minutes more and it leaves the
	// window of 15 minutes (the count is of rows made at or after the cut).
	f.clk.Advance(5*time.Minute + time.Second)
	if _, err := f.sign.IssueCode(ctx, "alice", f.fingerprint["alice"], "a"); err != nil {
		t.Errorf("after the first code left the window: %+v", err)
	}
}

func TestIssueCodePrunesOldRows(t *testing.T) {
	t.Parallel()
	f := newSignInFixture(t)
	ctx := t.Context()
	if _, err := f.sign.IssueCode(ctx, "alice", f.fingerprint["alice"], "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sign.IssueCode(ctx, "bob", f.fingerprint["bob"], "b"); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(23 * time.Hour)
	if _, err := f.sign.IssueCode(ctx, "alice", f.fingerprint["alice"], "a"); err != nil {
		t.Fatal(err)
	}
	if n := f.countCodes(); n != 2 {
		t.Errorf("%d rows after 23 hours, want both of alice", n)
	}
	f.clk.Advance(2 * time.Hour)
	if _, err := f.sign.IssueCode(ctx, "alice", f.fingerprint["alice"], "a"); err != nil {
		t.Fatal(err)
	}
	// Both rows of the first moment are older than 24 hours now, as is the one
	// of bob; the row from 2 hours ago and the new one stay.
	if n := f.countCodes(); n != 2 {
		t.Errorf("%d rows of alice after the prune, want 2", n)
	}
	var bobRows int64
	if err := f.st.Tx(ctx, func(tx *store.Store) error {
		var err error
		bobRows, err = tx.CountSSHCodesSince(ctx, f.bob.ID, time.Time{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if bobRows != 0 {
		t.Errorf("%d old rows of bob are left", bobRows)
	}
}

// TestIssueCodePermissions is the table of who gets a code: the same refusal
// for an unknown user, a disabled user and another user's key.
func TestIssueCodePermissions(t *testing.T) {
	t.Parallel()
	f := newSignInFixture(t)
	tests := []struct {
		name     string
		username string
		key      string
		wantErr  bool
	}{
		{"admin", "root", "root", false},
		{"member", "alice", "alice", false},
		{"disabled user", "dina", "dina", true},
		{"another user's key", "alice", "bob", true},
		{"unknown user", "nobody", "alice", true},
		{"unknown key", "alice", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fp := f.fingerprint[tt.key]
			if fp == "" {
				fp = "SHA256:unknown"
			}
			_, err := f.sign.IssueCode(t.Context(), tt.username, fp, "a")
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("IssueCode: %+v", err)
				}
				return
			}
			// All refusals are the same not found, whoever asks.
			if err == nil {
				t.Fatal("a code was issued")
			}
			appErr := requireTag(t, err, app.TagNotFound, "SignIn.IssueCode")
			if appErr.Tag != app.TagNotFound {
				t.Errorf("tag = %s", appErr.Tag)
			}
		})
	}
	// Nothing was stored for the refusals: the codes of the two allowed
	// callers are all that exists.
	for _, u := range []app.User{f.bob, f.disabled} {
		if n, _ := f.st.CountSSHCodesSince(t.Context(), u.ID, time.Time{}); n != 0 {
			t.Errorf("%s has %d codes", u.Username, n)
		}
	}
}

func TestCodesAreDifferent(t *testing.T) {
	t.Parallel()
	f := newSignInFixture(t)
	seen := map[string]bool{}
	for range 5 {
		got, err := f.sign.IssueCode(t.Context(), "bob", f.fingerprint["bob"], "b")
		if err != nil {
			t.Fatal(err)
		}
		if seen[got.Code] {
			t.Errorf("code %q came twice", got.Code)
		}
		seen[got.Code] = true
	}
}
