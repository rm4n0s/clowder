package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// demoPassword is the password of every demo user.
const demoPassword = "demo-password-1234"

// demoUsers are the users of the demo, in the order they are made.
var demoUsers = []struct {
	username    string
	displayName string
	role        store.Role
}{
	{"admin", "Admin", store.RoleAdmin},
	{"alice", "Alice", store.RoleMember},
	{"bob", "Bob", store.RoleMember},
	{"carol", "Carol", store.RoleMember},
}

// runDemo prepares the data directory of the demo. The directory is made if it
// is missing and must otherwise be empty, so that demo never touches the data
// of a real instance. It then makes the database and the demo users in it,
// through the accounts service (an invitation and a sign-up for each), and
// prints the directory and the users with their password to stdout.
// Later steps add their own demo data here.
func runDemo(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, hasher auth.Hasher) error {
	var dataDir string
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&dataDir, "data", envString(env, "CLOWDER_DATA", config.DefaultDataDir),
		"data directory, made if missing and empty (CLOWDER_DATA)")
	if err := flags.Parse(args); err != nil {
		return errors.NewErr(tagFlagsInvalid, err)
	}
	if flags.NArg() > 0 {
		return errors.New(tagFlagsInvalid, "unexpected arguments", "args", flags.Args())
	}
	if dataDir == "" {
		return errors.New(tagFlagsInvalid, "data directory is empty", "flag", "data")
	}

	if err := os.MkdirAll(dataDir, dataDirMode); err != nil {
		return errors.NewErr(tagDataDirCreateFailed, err).SetMetadata("dir", dataDir)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return errors.NewErr(tagDataDirReadFailed, err).SetMetadata("dir", dataDir)
	}
	if len(entries) > 0 {
		return errors.New(tagDataDirNotEmpty, "the demo needs an empty data directory",
			"dir", dataDir, "entries", len(entries))
	}
	_, _ = fmt.Fprintf(stdout, "demo data directory is ready: %s\n", dataDir)

	logger := newLogger(config.DefaultLogLevel, stderr)
	st, err := store.Open(ctx, filepath.Join(dataDir, databaseFile), clock.Real(), logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			logger.Error("closing the store failed", "err", closeErr)
		}
	}()
	accounts := app.NewAccounts(st, clock.Real(), hasher, config.DefaultInvitationLifetime)
	_, _ = fmt.Fprintf(stdout, "demo users (password %s):\n", demoPassword)
	for _, d := range demoUsers {
		inv, err := accounts.CreateSystemInvitation(ctx, d.role)
		if err != nil {
			return err
		}
		u, err := accounts.SignUp(ctx, inv.Token, d.username, d.displayName, demoPassword)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "  %-6s %-6s %s\n", u.Username, u.Role, u.DisplayName)
	}
	return nil
}

const (
	tagDataDirNotEmpty   = "DataDirNotEmpty"
	tagDataDirReadFailed = "DataDirReadFailed"
)
