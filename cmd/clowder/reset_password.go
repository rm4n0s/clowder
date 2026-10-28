package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/errors"
)

// runResetPassword makes a one-time password reset link for the user named by
// -user, with no admin as its maker, and prints its link and its expiry to
// stdout (SPEC §7.1, D51). It is how the last admin gets back in. It opens an
// existing database: an unknown or disabled user is refused with a message,
// and the link replaces the unused links of the user. The audit event of the
// link names the user as its actor, because the command line has no user (see
// app.Accounts.CreateSystemResetLink).
func runResetPassword(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, hasher auth.Hasher) error {
	cfg := config.Default()
	var username string
	flags := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.DataDir, "data", envString(env, "CLOWDER_DATA", config.DefaultDataDir),
		"data directory (CLOWDER_DATA)")
	databaseFromEnv := urlFlag(flags, &cfg.DatabaseURL, "database-url", env, "CLOWDER_DATABASE_URL", cfg.DatabaseURL,
		"PostgreSQL URL, postgres://...; empty: SQLite in the data directory (CLOWDER_DATABASE_URL)")
	flags.StringVar(&cfg.BaseURL, "base-url", envString(env, "CLOWDER_BASE_URL", config.DefaultBaseURL),
		"base URL of reset links (CLOWDER_BASE_URL)")
	flags.StringVar(&username, "user", "", "username of the user who forgot the password")
	if err := flags.Parse(args); err != nil {
		return errors.NewErr(tagFlagsInvalid, err)
	}
	if flags.NArg() > 0 {
		return errors.New(tagFlagsInvalid, "unexpected arguments", "args", flags.Args())
	}
	databaseFromEnv()
	if strings.TrimSpace(username) == "" {
		return errors.New(tagResetUserMissing, "give the username with -user")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel, stderr)
	st, err := openStore(ctx, cfg.DataDir, cfg.DatabaseURL, clock.Real(), logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			logger.Error("closing the store failed", "err", closeErr)
		}
	}()

	accounts := app.NewAccounts(st, clock.Real(), hasher, app.AccountsConfig{SessionLifetime: cfg.SessionLifetime})
	link, err := accounts.CreateSystemResetLink(ctx, username)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "%s/reset/%s\n", strings.TrimRight(cfg.BaseURL, "/"), link.Token)
	_, _ = fmt.Fprintf(stdout, "expires %s\n", link.ExpiresAt.Format(time.RFC3339))
	return nil
}

// tagResetUserMissing marks a reset-password command without -user.
const tagResetUserMissing = "ResetUserMissing"
