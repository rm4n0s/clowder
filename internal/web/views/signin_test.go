package views_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestSSHKeyRemovePath(t *testing.T) {
	t.Parallel()
	if got := views.SSHKeyRemovePath(7); got != "/me/settings/ssh-keys/7/remove" {
		t.Errorf("SSHKeyRemovePath(7) = %q", got)
	}
}

func TestSignInSection(t *testing.T) {
	t.Parallel()
	added := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	used := added.Add(24 * time.Hour)
	keys := []app.SSHKey{
		{ID: 3, Name: "laptop <b>", Type: "ssh-ed25519", Fingerprint: "SHA256:abc", CreatedAt: added},
		{ID: 5, Name: "phone", Type: "ssh-rsa", Fingerprint: "SHA256:def", CreatedAt: added, LastUsedAt: &used},
	}
	server := app.SSHServer{Enabled: true, HostFingerprint: "SHA256:hostprint", Command: "ssh -p 2222 ada@chat.example.org"}
	tests := []struct {
		name    string
		data    views.SignInData
		want    []string
		notWant []string
	}{
		{
			name: "keys and the server",
			data: views.SignInData{Keys: keys, Server: server},
			want: []string{
				`<section id="signin" class="settings-section">`, `<li id="ssh-key-3" class="ssh-key">`, `<li id="ssh-key-5" class="ssh-key">`,
				"laptop &lt;b&gt;", "SHA256:abc", "SHA256:def", "Never",
				`action="/me/settings/ssh-keys/3/remove"`, `action="/me/settings/ssh-keys/5/remove"`,
				`<form id="ssh-key-add-form" method="post" action="/me/settings/ssh-keys"`,
				`<code id="ssh-command" class="ssh-command">ssh -p 2222 ada@chat.example.org</code>`,
				`<code id="ssh-host-fingerprint">SHA256:hostprint</code>`,
			},
			notWant: []string{"laptop <b>", "You have no SSH keys.", "The SSH server is not running", "Saved", "TODO"},
		},
		{
			name:    "no keys, server off",
			data:    views.SignInData{},
			want:    []string{"You have no SSH keys.", "The SSH server is not running on this Clowder."},
			notWant: []string{`id="ssh-command"`, "<table", "Saved"},
		},
		{
			name: "problems of the add form keep the typed values",
			data: views.SignInData{Add: views.SSHKeyAddData{
				Name: "typed name", PublicKey: "ssh-ed25519 AAAA typed",
				Errors: views.FormErrors{Form: "Too many.", Fields: map[string]string{"name": "Bad name.", "publicKey": "Bad key.", "currentPassword": "Wrong."}},
			}},
			want: []string{
				`value="typed name"`, ">ssh-ed25519 AAAA typed</textarea>", `<p class="form-error" role="alert">Too many.</p>`,
				`id="ssh-key-name-error"`, "Bad name.", `id="ssh-key-public-error"`, "Bad key.", `id="ssh-key-password-error"`, "Wrong.",
			},
		},
		{
			name: "problems of one remove form",
			data: views.SignInData{Keys: keys, RemoveKeyID: 5, RemoveErrors: views.FormErrors{Fields: map[string]string{"currentPassword": "This is not your password."}}},
			want: []string{"This is not your password."},
		},
		{
			name: "saved",
			data: views.SignInData{Saved: true},
			want: []string{`<p class="saved" role="status">Saved</p>`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			html := renderHTML(t, views.SignInSection(tt.data))
			requireHTML(t, html, tt.want, tt.notWant)
			// A password input never holds a value.
			if tt.name == "problems of one remove form" {
				if n := strings.Count(html, "This is not your password."); n != 1 {
					t.Errorf("the problem is shown %d times, want once (under the form of key 5)", n)
				}
			}
		})
	}
}
