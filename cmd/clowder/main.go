package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/rm4n0s/errors"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "clowder:", err)
		os.Exit(1)
	}
}

// run runs the subcommand named by args[0]. env looks up an environment
// variable ("" when unset). Everything the command prints goes to stdout and
// stderr, so tests can dispatch without a process.
func run(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return errors.New(tagUnknownCommand, "no command given", "command", "")
	}
	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], env, stderr)
	case "demo":
		return runDemo(args[1:], env, stdout, stderr)
	case "invite":
		// TODO(step 2.5): invite.
		return errors.New(tagNotImplemented, "command is not implemented yet", "command", args[0])
	default:
		usage(stderr)
		return errors.New(tagUnknownCommand, "unknown command", "command", args[0])
	}
}

// usage prints the list of subcommands.
func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: clowder <command> [flags]

commands:
  serve    run the server
  invite   print a one-time invitation link
  demo     fill an empty database with demo data

Run "clowder serve -h" for the flags of serve.
`)
}

const (
	tagUnknownCommand = "UnknownCommand"
	tagNotImplemented = "NotImplemented"
)
