//go:build clowder_browser

package web_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
	"golang.org/x/crypto/ssh"
)

// fetchSSHCode connects to the SSH server of srv as the user with the key and
// returns the code the server prints (the command json).
func fetchSSHCode(t *testing.T, srv *browsertest.Server, username string, signer ssh.Signer) string {
	t.Helper()
	client, err := ssh.Dial("tcp", srv.SSHAddr, &ssh.ClientConfig{
		User:            username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { return nil },
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh %s: %v", username, err)
	}
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("ssh session: %v", err)
	}
	defer func() { _ = session.Close() }()
	out, err := session.Output("json")
	if err != nil {
		t.Fatalf("ssh json: %v", err)
	}
	var issued struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(out, &issued); err != nil || issued.Code == "" {
		t.Fatalf("ssh answered %q (%v)", out, err)
	}
	return issued.Code
}

// TestLoginWithTheSSHCode is the two-form login: carol adds a key, fetches a
// code over SSH (which proves the key), turns "Ask for an SSH code when I log
// in" on in the settings, logs out, logs in with her password, is asked for the
// code in a second form, is refused a wrong one, and lands on the page she was
// going to with a fresh code.
func TestLoginWithTheSSHCode(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{SSH: true})
	carol := secondBrowser(t, srv, "carol")
	openSettings(t, carol, srv)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " carol@laptop"
	err = chromedp.Do(carol,
		browsertest.Fill("#ssh-key-name", "laptop"),
		browsertest.Fill("#ssh-key-public", line),
		browsertest.Fill("#ssh-key-password", browsertest.DemoPassword),
		chromedp.Click(`#ssh-key-add-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("add the key: %v", err)
	}
	if err := waitFor(carol, `document.querySelector('#ssh-keys-panel table.ssh-keys tbody tr') !== null`); err != nil {
		t.Fatalf("the key is not in the table: %v", err)
	}
	// The switch cannot be turned on before a key has fetched a code.
	if err := waitFor(carol, `document.getElementById('ssh-code-unavailable') !== null && document.querySelector('#ssh-code-form button[type="submit"]').disabled`); err != nil {
		t.Fatalf("the switch is not disabled before a code was fetched: %v", err)
	}

	// The key fetches a code; the page is opened again and the switch works.
	fetchSSHCode(t, srv, "carol", signer)
	openSettings(t, carol, srv)
	err = chromedp.Do(carol,
		browsertest.Fill("#ssh-code-password", "not the password"),
		chromedp.Click(`#ssh-code-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `document.getElementById('ssh-code-password-error') !== null`); err != nil {
		t.Fatalf("no error for a wrong password on the switch: %v", err)
	}
	err = chromedp.Do(carol,
		browsertest.Fill("#ssh-code-password", browsertest.DemoPassword),
		chromedp.Click(`#ssh-code-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `document.querySelector('#ssh-code-state strong') !== null && document.querySelector('#ssh-code-state strong').textContent === 'on'`); err != nil {
		t.Fatalf("the switch is not on: %v", err)
	}
	if got := valueOf(t, carol, "#ssh-code-password"); got != "" {
		t.Error("the password stays in the form of the switch")
	}

	// Log out and log in again: the password, then the code.
	if err := chromedp.Do(carol, chromedp.Click(`#logout-form button[type="submit"]`), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatalf("log out: %v", err)
	}
	err = chromedp.Do(carol,
		chromedp.Navigate(srv.URL+"/login?next=%2Frooms"),
		chromedp.WaitVisible("#login-form"),
		browsertest.Fill("#login-username", "carol"),
		browsertest.Fill("#login-password", browsertest.DemoPassword),
		chromedp.Click(`#login-form button[type="submit"]`),
		chromedp.WaitVisible("#login-code-form"),
	)
	if err != nil {
		t.Fatalf("the second form does not appear: %v", err)
	}
	if loc, err := chromedp.Run(carol, chromedp.Location()); err != nil || !strings.HasSuffix(loc, "/login/code") {
		t.Fatalf("location = %q (%v), want /login/code", loc, err)
	}
	_, port, _ := net.SplitHostPort(srv.SSHAddr)
	if got, want := textOf(t, carol, "#login-code-command"), "ssh -p "+port+" carol@127.0.0.1"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	// The form is made for a code: the browser hints.
	for attr, want := range map[string]string{"autocomplete": "one-time-code", "autocapitalize": "characters", "spellcheck": "false", "maxlength": "9"} {
		got, err := chromedp.Run(carol, chromedp.Evaluate[string](`document.getElementById('login-code').getAttribute('`+attr+`')`))
		if err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", attr, got, err, want)
		}
	}

	// A wrong code is refused in the form; nothing else changes.
	err = chromedp.Do(carol,
		browsertest.Fill("#login-code", "0000-0000"),
		chromedp.Click(`#login-code-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `document.getElementById('login-code-error') !== null`); err != nil {
		t.Fatalf("no error for a wrong code: %v", err)
	}
	if got := textOf(t, carol, "#login-code-error"); got != "That code is not right." {
		t.Errorf("error = %q", got)
	}

	// The right code, fetched now, lands on the page she was going to.
	code := fetchSSHCode(t, srv, "carol", signer)
	err = chromedp.Do(carol,
		browsertest.Fill("#login-code", strings.ToLower(code[:4]+"-"+code[4:])),
		chromedp.Click(`#login-code-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	landed := false
	for range 100 {
		if loc, err := chromedp.Run(carol, chromedp.Location()); err == nil && strings.HasSuffix(loc, "/rooms") {
			landed = true
			break
		}
		if err := chromedp.Do(carol, chromedp.Sleep(100*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if !landed {
		loc, _ := chromedp.Run(carol, chromedp.Location())
		t.Fatalf("the login did not land on /rooms (at %q)", loc)
	}
	if err := chromedp.Do(carol, chromedp.WaitVisible("#sidebar-user")); err != nil {
		t.Fatalf("carol is not logged in: %v", err)
	}
}
