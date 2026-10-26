package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// openAccounts opens the database that a command made in dir, with the
// accounts service on it. Both are closed when the test ends.
func openAccounts(t *testing.T, dir string) (*app.Accounts, *store.Store) {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(dir, databaseFile), clock.Real(), nil)
	if err != nil {
		t.Fatalf("open the database: %+v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return app.NewAccounts(st, clock.Real(), auth.FastHasher(), config.DefaultInvitationLifetime), st
}

func TestRunInvite(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		wantBase string
		wantRole store.Role
		wantLife time.Duration
	}{
		{
			name: "admin with the default base URL", args: []string{"-admin"},
			wantBase: config.DefaultBaseURL, wantRole: store.RoleAdmin, wantLife: config.DefaultInvitationLifetime,
		},
		{
			name: "member", args: []string{"-member"},
			wantBase: config.DefaultBaseURL, wantRole: store.RoleMember, wantLife: config.DefaultInvitationLifetime,
		},
		{
			name: "base URL flag", args: []string{"-admin", "-base-url", "https://chat.example.com"},
			wantBase: "https://chat.example.com", wantRole: store.RoleAdmin, wantLife: config.DefaultInvitationLifetime,
		},
		{
			name: "base URL with a trailing slash", args: []string{"-member", "-base-url", "https://chat.example.com/team/"},
			wantBase: "https://chat.example.com/team", wantRole: store.RoleMember, wantLife: config.DefaultInvitationLifetime,
		},
		{
			name: "base URL from the environment", args: []string{"-admin"},
			env:      map[string]string{"CLOWDER_BASE_URL": "http://env.test:9000"},
			wantBase: "http://env.test:9000", wantRole: store.RoleAdmin, wantLife: config.DefaultInvitationLifetime,
		},
		{
			name: "the flag beats the environment", args: []string{"-admin", "-base-url", "http://flag.test"},
			env:      map[string]string{"CLOWDER_BASE_URL": "http://env.test"},
			wantBase: "http://flag.test", wantRole: store.RoleAdmin, wantLife: config.DefaultInvitationLifetime,
		},
		{
			name: "lifetime from the environment", args: []string{"-member"},
			env:      map[string]string{"CLOWDER_INVITATION_LIFETIME": "24h"},
			wantBase: config.DefaultBaseURL, wantRole: store.RoleMember, wantLife: 24 * time.Hour,
		},
		{
			name: "lifetime flag", args: []string{"-member", "-invitation-lifetime", "90m"},
			wantBase: config.DefaultBaseURL, wantRole: store.RoleMember, wantLife: 90 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data") // made by the command
			args := append([]string{"invite", "-data", dir}, tt.args...)
			var stdout, stderr bytes.Buffer
			before := time.Now()
			err := run(t.Context(), args, mapEnv(tt.env), &stdout, &stderr, auth.FastHasher())
			if err != nil {
				t.Fatalf("invite: %+v", err)
			}
			after := time.Now()

			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("stdout = %q, want the link and the expiry", stdout.String())
			}
			prefix := tt.wantBase + "/invite/"
			if !strings.HasPrefix(lines[0], prefix) {
				t.Fatalf("link = %q, want prefix %q", lines[0], prefix)
			}
			token := strings.TrimPrefix(lines[0], prefix)
			if token == "" || strings.ContainsAny(token, "/?# ") {
				t.Errorf("token = %q", token)
			}
			expiry, ok := strings.CutPrefix(lines[1], "expires ")
			if !ok {
				t.Fatalf("second line = %q, want the expiry", lines[1])
			}
			expires, err := time.Parse(time.RFC3339, expiry)
			if err != nil {
				t.Fatalf("expiry %q: %v", expiry, err)
			}
			// The expiry is printed to the second.
			if expires.Before(before.Add(tt.wantLife).Truncate(time.Second)) ||
				expires.After(after.Add(tt.wantLife)) {
				t.Errorf("expires %v, want %v after now", expires, tt.wantLife)
			}

			// The token in the link works, and makes a user of the role.
			accounts, st := openAccounts(t, dir)
			if err := accounts.InvitationValid(t.Context(), token); err != nil {
				t.Fatalf("InvitationValid: %+v", err)
			}
			u, err := accounts.SignUp(t.Context(), token, "newcomer", "New Comer", "a password of 12+")
			if err != nil {
				t.Fatalf("SignUp: %+v", err)
			}
			if u.Role != tt.wantRole {
				t.Errorf("role = %q, want %q", u.Role, tt.wantRole)
			}
			row, err := st.InvitationByTokenHash(t.Context(), auth.HashToken(token))
			if err != nil {
				t.Fatal(err)
			}
			if row.CreatedBy != nil {
				t.Errorf("CreatedBy = %v, want nil: no user made it", *row.CreatedBy)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestRunInviteTwiceMakesTwoLinks(t *testing.T) {
	dir := t.TempDir()
	var links []string
	for range 2 {
		var stdout, stderr bytes.Buffer
		args := []string{"invite", "-data", dir, "-member"}
		if err := run(t.Context(), args, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
			t.Fatalf("invite: %+v", err)
		}
		links = append(links, strings.SplitN(stdout.String(), "\n", 2)[0])
	}
	if links[0] == links[1] {
		t.Errorf("the same link twice: %s", links[0])
	}
}

func TestRunInviteErrors(t *testing.T) {
	// A file, so that the data directory cannot be made under it.
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory where the database file should be.
	badDB := t.TempDir()
	if err := os.Mkdir(filepath.Join(badDB, databaseFile), 0o750); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		args      []string
		env       map[string]string
		wantTag   string
		wantRoute string
	}{
		{
			name: "no role flag", args: []string{"invite"},
			wantTag: tagInviteRoleInvalid, wantRoute: "run->runInvite.InviteRoleInvalid",
		},
		{
			name: "both role flags", args: []string{"invite", "-admin", "-member"},
			wantTag: tagInviteRoleInvalid, wantRoute: "run->runInvite.InviteRoleInvalid",
		},
		{
			name: "unknown flag", args: []string{"invite", "-nope"},
			wantTag: tagFlagsInvalid, wantRoute: "run->runInvite.FlagsInvalid",
		},
		{
			name: "unexpected argument", args: []string{"invite", "-admin", "extra"},
			wantTag: tagFlagsInvalid, wantRoute: "run->runInvite.FlagsInvalid",
		},
		{
			name: "malformed lifetime in the environment", args: []string{"invite", "-admin"},
			env:     map[string]string{"CLOWDER_INVITATION_LIFETIME": "soon"},
			wantTag: tagEnvValueInvalid, wantRoute: "run->runInvite->envDuration.EnvValueInvalid",
		},
		{
			name: "lifetime that is not positive", args: []string{"invite", "-admin", "-invitation-lifetime", "0s"},
			wantTag: config.TagInvitationLifetimeInvalid, wantRoute: "run->runInvite->Config.Validate.InvitationLifetimeInvalid",
		},
		{
			name: "base URL that is not absolute", args: []string{"invite", "-admin", "-base-url", "chat.example.com"},
			wantTag: config.TagBaseURLInvalid, wantRoute: "run->runInvite->Config.Validate.BaseURLInvalid",
		},
		{
			name: "data directory under a file", args: []string{"invite", "-admin", "-data", filepath.Join(notADir, "sub")},
			wantTag: tagDataDirCreateFailed, wantRoute: "run->runInvite.DataDirCreateFailed",
		},
		{
			name: "database cannot be opened", args: []string{"invite", "-admin", "-data", badDB},
			wantTag: "StoreOpenFailed", wantRoute: "run->runInvite->Open->open.StoreOpenFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), tt.args, mapEnv(tt.env), &stdout, &stderr, auth.FastHasher())
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", appErr.Tag, tt.wantTag)
			}
			if !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("unexpected failure path: %s, want %s", appErr.Route(), tt.wantRoute)
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected stdout: %q", stdout.String())
			}
		})
	}
}
