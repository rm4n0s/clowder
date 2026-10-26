package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// runInvite makes a one-time invitation, with no user as its maker, and prints
// its link and its expiry to stdout. The data directory and the database are
// made if they are missing, so the first admin can be invited before the first
// run of the server. Exactly one of -admin and -member says the role of the
// new user.
func runInvite(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, hasher auth.Hasher) error {
	invitationLifetime, err := envDuration(env, "CLOWDER_INVITATION_LIFETIME", config.DefaultInvitationLifetime)
	if err != nil {
		return err
	}
	cfg := config.Default()
	var admin, member bool
	flags := flag.NewFlagSet("invite", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.DataDir, "data", envString(env, "CLOWDER_DATA", config.DefaultDataDir),
		"data directory (CLOWDER_DATA)")
	flags.StringVar(&cfg.BaseURL, "base-url", envString(env, "CLOWDER_BASE_URL", config.DefaultBaseURL),
		"base URL of invitation links (CLOWDER_BASE_URL)")
	flags.DurationVar(&cfg.InvitationLifetime, "invitation-lifetime", invitationLifetime,
		"how long the invitation is valid (CLOWDER_INVITATION_LIFETIME)")
	flags.BoolVar(&admin, "admin", false, "invite an admin")
	flags.BoolVar(&member, "member", false, "invite a member")
	if err := flags.Parse(args); err != nil {
		return errors.NewErr(tagFlagsInvalid, err)
	}
	if flags.NArg() > 0 {
		return errors.New(tagFlagsInvalid, "unexpected arguments", "args", flags.Args())
	}
	if admin == member {
		return errors.New(tagInviteRoleInvalid, "give exactly one of -admin and -member",
			"admin", admin, "member", member)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	role := store.RoleMember
	if admin {
		role = store.RoleAdmin
	}

	logger := newLogger(cfg.LogLevel, stderr)
	if err := os.MkdirAll(cfg.DataDir, dataDirMode); err != nil {
		return errors.NewErr(tagDataDirCreateFailed, err).SetMetadata("dir", cfg.DataDir)
	}
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, databaseFile), clock.Real(), logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			logger.Error("closing the store failed", "err", closeErr)
		}
	}()

	accounts := app.NewAccounts(st, clock.Real(), hasher, cfg.InvitationLifetime)
	inv, err := accounts.CreateSystemInvitation(ctx, role)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "%s/invite/%s\n", strings.TrimRight(cfg.BaseURL, "/"), inv.Token)
	_, _ = fmt.Fprintf(stdout, "expires %s\n", inv.ExpiresAt.Format(time.RFC3339))
	return nil
}

// tagInviteRoleInvalid marks an invite command with none or both of -admin and
// -member.
const tagInviteRoleInvalid = "InviteRoleInvalid"
