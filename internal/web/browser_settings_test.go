//go:build clowder_browser

package web_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
	"golang.org/x/crypto/ssh"
)

// openSettings opens the settings page and waits for it.
func openSettings(t *testing.T, ctx context.Context, srv *browsertest.Server) {
	t.Helper()
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/me/settings"), chromedp.WaitVisible("#profile-form")); err != nil {
		t.Fatalf("open the settings: %v", err)
	}
}

// TestSettingsProfileAndTheme changes the display name and the theme of carol
// on the settings page: each section says "Saved", the name changes in the
// sidebar without a reload, and the theme is on the html element at once and
// after a reload.
func TestSettingsProfileAndTheme(t *testing.T) {
	srv := browsertest.Start(t)
	carol := secondBrowser(t, srv, "carol")
	openSettings(t, carol, srv)
	runJS(t, carol, `window.notReloaded = true; true`)

	err := chromedp.Do(carol,
		browsertest.Fill("#profile-display-name", "Carol Q"),
		chromedp.Click(`#profile-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("save the name: %v", err)
	}
	if err := waitFor(carol, `document.querySelector('#profile .saved') !== null`); err != nil {
		t.Fatalf("the profile section does not say Saved: %v", err)
	}
	if err := waitFor(carol, `document.getElementById('sidebar-user').textContent === 'Carol Q'`); err != nil {
		t.Fatalf("the sidebar keeps the old name: %v (%q)", err, textOf(t, carol, "#sidebar-user"))
	}
	// Carol has no avatar: her initials, on a colour.
	if got := textOf(t, carol, "#profile .avatar-lg"); got != "CQ" {
		t.Errorf("initials = %q, want CQ", got)
	}

	// An empty name is refused in the form.
	err = chromedp.Do(carol, browsertest.Fill("#profile-display-name", " "), chromedp.Click(`#profile-form button[type="submit"]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `document.getElementById('profile-display-name-error') !== null`); err != nil {
		t.Fatalf("no error for an empty name: %v", err)
	}

	// The theme.
	if err := chromedp.Do(carol, chromedp.Click(`#theme-dark`), chromedp.Click(`#appearance-form button[type="submit"]`)); err != nil {
		t.Fatalf("choose the dark theme: %v", err)
	}
	if err := waitFor(carol, `document.documentElement.getAttribute('data-theme') === 'dark'`); err != nil {
		t.Fatalf("the page is not dark: %v", err)
	}
	if err := waitFor(carol, `document.querySelector('#appearance .saved') !== null`); err != nil {
		t.Fatalf("the appearance section does not say Saved: %v", err)
	}
	if notReloaded, err := chromedp.Run(carol, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !notReloaded {
		t.Errorf("the page was reloaded (%v)", err)
	}
	if err := chromedp.Do(carol, chromedp.Reload(), chromedp.WaitVisible("#profile-form")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `document.documentElement.getAttribute('data-theme') === 'dark' && document.getElementById('theme-dark').checked`); err != nil {
		t.Fatalf("the theme is not kept after a reload: %v", err)
	}
	if err := chromedp.Do(carol, chromedp.Click(`#theme-system`), chromedp.Click(`#appearance-form button[type="submit"]`)); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `!document.documentElement.hasAttribute('data-theme')`); err != nil {
		t.Fatalf("the system theme leaves the attribute: %v", err)
	}
}

// TestSettingsAvatar uploads a picture as carol, sees it in the settings and
// in the sidebar, and removes it again.
func TestSettingsAvatar(t *testing.T) {
	srv := browsertest.Start(t)
	carol := secondBrowser(t, srv, "carol")
	openSettings(t, carol, srv)

	img := image.NewNRGBA(image.Rect(0, 0, 80, 60))
	for y := range 60 {
		for x := range 80 {
			img.SetNRGBA(x, y, color.NRGBA{R: 0xc2, G: 0x41, B: 0x0c, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "me.png")
	if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	err := chromedp.Do(carol, chromedp.SetUploadFiles("#avatar-file", []string{file}), chromedp.Click(`#avatar-form button[type="submit"]`))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := waitFor(carol, `document.querySelector('#profile img.avatar-lg') !== null`); err != nil {
		t.Fatalf("the settings do not show the picture: %v", err)
	}
	if err := waitFor(carol, `document.querySelector('#sidebar-profile img.avatar-md') !== null`); err != nil {
		t.Fatalf("the sidebar does not show the picture: %v", err)
	}
	// The picture is really there: the browser loaded it, 256 by 256.
	if err := waitFor(carol, `(() => { const i = document.querySelector('#profile img.avatar-lg'); return i.complete && i.naturalWidth === 256 && i.naturalHeight === 256 })()`); err != nil {
		t.Fatalf("the picture did not load as 256x256: %v", err)
	}
	src, err := chromedp.Run(carol, chromedp.Evaluate[string](`document.querySelector('#profile img.avatar-lg').getAttribute('src')`))
	if err != nil || !strings.HasPrefix(src, "/avatars/") || !strings.Contains(src, "?v=") {
		t.Errorf("src = %q (%v), want /avatars/<id>?v=<key>", src, err)
	}

	// Something that is not a picture is refused in the form.
	bad := filepath.Join(t.TempDir(), "bad.png")
	if err := os.WriteFile(bad, []byte("not a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = chromedp.Do(carol, chromedp.SetUploadFiles("#avatar-file", []string{bad}), chromedp.Click(`#avatar-form button[type="submit"]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `document.getElementById('avatar-file-error') !== null`); err != nil {
		t.Fatalf("no error for a file that is not a picture: %v", err)
	}

	if err := chromedp.Do(carol, chromedp.Click(`#avatar-remove-form button`)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := waitFor(carol, `document.querySelector('#profile img.avatar-lg') === null && document.querySelector('#profile span.avatar-lg') !== null`); err != nil {
		t.Fatalf("the initials do not come back: %v", err)
	}
	if err := waitFor(carol, `document.querySelector('#sidebar-profile img.avatar-md') === null`); err != nil {
		t.Fatalf("the sidebar keeps the picture: %v", err)
	}
}

// TestSettingsPasswordEndsTheOtherBrowser has carol logged in in two browsers:
// the password changed in one logs the other out at once, and the first stays
// logged in.
func TestSettingsPasswordEndsTheOtherBrowser(t *testing.T) {
	srv := browsertest.Start(t)
	first := secondBrowser(t, srv, "carol")
	other := secondBrowser(t, srv, "carol")
	openSettings(t, first, srv)
	if err := chromedp.Do(other, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible("#sidebar-user")); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Do(other, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	err := chromedp.Do(first,
		browsertest.Fill("#password-current", browsertest.DemoPassword),
		browsertest.Fill("#password-new", "a brand new passphrase"),
		browsertest.Fill("#password-confirm", "a different passphrase"),
		chromedp.Click(`#password-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(first, `document.getElementById('password-confirm-error') !== null`); err != nil {
		t.Fatalf("no error for two different passwords: %v", err)
	}
	err = chromedp.Do(first,
		browsertest.Fill("#password-current", browsertest.DemoPassword),
		browsertest.Fill("#password-new", "a brand new passphrase"),
		browsertest.Fill("#password-confirm", "a brand new passphrase"),
		chromedp.Click(`#password-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(first, `document.querySelector('#password .saved') !== null`); err != nil {
		t.Fatalf("the password section does not say Saved: %v", err)
	}
	// The passwords do not stay in the form (the morph alone would keep them).
	if err := waitFor(first, `['password-current', 'password-new', 'password-confirm'].every(id => document.getElementById(id).value === '')`); err != nil {
		t.Errorf("the passwords stay in the form: %v", err)
	}
	// The page navigates, which destroys the context a poll runs in: ask for
	// the location again until it is the login page.
	loggedOut := false
	for range 100 {
		if loc, err := chromedp.Run(other, chromedp.Location()); err == nil && strings.Contains(loc, "/login") {
			loggedOut = true
			break
		}
		if err := chromedp.Do(other, chromedp.Sleep(100*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if !loggedOut {
		t.Fatal("the other browser was not logged out")
	}
	// The first one still works.
	if err := chromedp.Do(first, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible("#sidebar-user")); err != nil {
		t.Fatalf("the browser that changed the password was logged out: %v", err)
	}
}

// TestSettingsSSHKeys adds an SSH key as carol on the Sign-in section, sees it
// in the table without a reload with the command of the server, is refused a
// wrong password and a key of a type that is not accepted, and removes the key
// with her password.
func TestSettingsSSHKeys(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{SSH: true})
	carol := secondBrowser(t, srv, "carol")
	openSettings(t, carol, srv)
	runJS(t, carol, `window.notReloaded = true; true`)

	// The server runs: the section shows the command with the port it got.
	host, port, err := net.SplitHostPort(srv.SSHAddr)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := textOf(t, carol, "#ssh-command"), "ssh -p "+port+" carol@127.0.0.1"; got != want || host != "127.0.0.1" {
		t.Errorf("command = %q, want %q", got, want)
	}
	if got := textOf(t, carol, "#ssh-host-fingerprint"); !strings.HasPrefix(got, "SHA256:") {
		t.Errorf("host fingerprint = %q", got)
	}

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " carol@laptop"
	add := func(name, text, password string) {
		t.Helper()
		err := chromedp.Do(carol,
			browsertest.Fill("#ssh-key-name", name),
			browsertest.Fill("#ssh-key-public", text),
			browsertest.Fill("#ssh-key-password", password),
			chromedp.Click(`#ssh-key-add-form button[type="submit"]`),
		)
		if err != nil {
			t.Fatalf("add the key: %v", err)
		}
	}

	add("laptop", line, "not the password")
	if err := waitFor(carol, `document.getElementById('ssh-key-password-error') !== null`); err != nil {
		t.Fatalf("no error for a wrong password: %v", err)
	}
	if got := valueOf(t, carol, "#ssh-key-public"); got != line {
		t.Errorf("the key was not kept in the form: %q", got)
	}
	add("laptop", "ssh-dss AAAAB3NzaC1kc3MAAAAA nope", browsertest.DemoPassword)
	if err := waitFor(carol, `document.getElementById('ssh-key-public-error') !== null`); err != nil {
		t.Fatalf("no error for a key that is not accepted: %v", err)
	}

	add("laptop", line, browsertest.DemoPassword)
	if err := waitFor(carol, `document.querySelector('#signin .saved') !== null && document.querySelector('#ssh-keys-panel table.ssh-keys tbody tr') !== null`); err != nil {
		t.Fatalf("the key is not in the table: %v", err)
	}
	if got := textOf(t, carol, "#ssh-keys-panel td.name"); got != "laptop" {
		t.Errorf("name = %q", got)
	}
	if got := textOf(t, carol, "#ssh-keys-panel td.fingerprint"); got != ssh.FingerprintSHA256(key) {
		t.Errorf("fingerprint = %q, want %q", got, ssh.FingerprintSHA256(key))
	}
	// The form is empty again.
	if got := valueOf(t, carol, "#ssh-key-name"); got != "" {
		t.Errorf("the name stays in the form: %q", got)
	}
	if got := valueOf(t, carol, `#ssh-key-add-form input[name="currentPassword"]`); got != "" {
		t.Error("the password stays in the form")
	}
	if got := valueOf(t, carol, "#ssh-key-public"); got != "" {
		t.Errorf("the key stays in the form: %q", got)
	}

	// Remove it: the wrong password first.
	err = chromedp.Do(carol,
		browsertest.Fill(`#ssh-keys-panel input[name="currentPassword"]`, "not the password"),
		chromedp.Click(`#ssh-keys-panel form.ssh-key-remove button[type="submit"]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `document.querySelector('#ssh-keys-panel .field-error') !== null`); err != nil {
		t.Fatalf("no error for a wrong password on Remove: %v", err)
	}
	err = chromedp.Do(carol,
		browsertest.Fill(`#ssh-keys-panel input[name="currentPassword"]`, browsertest.DemoPassword),
		chromedp.Click(`#ssh-keys-panel form.ssh-key-remove button[type="submit"]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `document.getElementById('ssh-keys-empty') !== null`); err != nil {
		t.Fatalf("the key is still listed: %v", err)
	}
	if notReloaded, err := chromedp.Run(carol, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !notReloaded {
		t.Errorf("the page was reloaded (%v)", err)
	}
}
