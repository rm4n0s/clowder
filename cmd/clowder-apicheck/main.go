package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/rm4n0s/errors"
)

// passwordEnv is the environment variable that holds the password when -password
// is not given.
const passwordEnv = "CLOWDER_APICHECK_PASSWORD"

// otherPasswordEnv is the environment variable that holds the password of the
// other user when -other-password is not given.
const otherPasswordEnv = "CLOWDER_APICHECK_OTHER_PASSWORD"

// demoPassword is the password of the users of `clowder demo`, which the other
// user has unless it is told otherwise.
const demoPassword = "demo-password-1234"

// requestTimeout bounds each request.
const requestTimeout = 30 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run runs the scenarios and returns the exit status: 0 when every step got
// the status it expects, 1 when one did not, 2 for wrong flags. env looks up an
// environment variable.
func run(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clowder-apicheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	baseURL := fs.String("base-url", "http://localhost:8080", "address of the Clowder server")
	username := fs.String("username", "", "username to log in as")
	password := fs.String("password", "", "password ("+passwordEnv+" when empty)")
	clientName := fs.String("client-name", "clowder-apicheck", "name of this client in the user's sessions")
	otherUsername := fs.String("other-username", "bob", "the other user, whom the board scenario assigns a task to and who reads the notifications")
	otherPassword := fs.String("other-password", "", "password of the other user ("+otherPasswordEnv+" when empty; the demo password if that is empty too)")
	only := fs.String("only", "", "run only these scenarios, separated by commas")
	list := fs.Bool("list", false, "print the names of the scenarios and stop")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *list {
		for _, s := range scenarios() {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\n", s.name, s.summary)
		}
		return 0
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "clowder-apicheck: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	if *password == "" {
		*password = env(passwordEnv)
	}
	if *username == "" || *password == "" {
		_, _ = fmt.Fprintf(stderr, "clowder-apicheck: -username and -password (or %s) are required\n", passwordEnv)
		return 2
	}
	if *otherPassword == "" {
		*otherPassword = env(otherPasswordEnv)
	}
	if *otherPassword == "" {
		*otherPassword = demoPassword
	}
	chosen, err := choose(scenarios(), *only)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "clowder-apicheck: %s\n", err.Error())
		return 2
	}

	c := &client{
		base:  strings.TrimRight(*baseURL, "/"),
		http:  &http.Client{Timeout: requestTimeout},
		out:   stdout,
		redac: &redactor{},
	}
	r := &runner{c: c, username: *username, password: *password, clientName: *clientName,
		otherUsername: *otherUsername, otherPassword: *otherPassword}
	for _, s := range chosen {
		_, _ = fmt.Fprintf(stdout, "== %s: %s\n\n", s.name, s.summary)
		if err := s.run(ctx, r); err != nil {
			_, _ = fmt.Fprintf(stdout, "FAILED %s: %s\n", s.name, c.redac.text(describe(err)))
			_, _ = fmt.Fprintf(stderr, "clowder-apicheck: %s failed: %s\n", s.name, c.redac.text(describe(err)))
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "ok %s\n", s.name)
	}
	_, _ = fmt.Fprintf(stdout, "\nall scenarios passed (%d requests)\n", c.requests)
	return 0
}

// choose picks the scenarios named in only (all when it is empty), in the
// order they are listed. A name that no scenario has is an error.
func choose(all []scenario, only string) ([]scenario, error) {
	if strings.TrimSpace(only) == "" {
		return all, nil
	}
	want := map[string]bool{}
	for _, name := range strings.Split(only, ",") {
		want[strings.TrimSpace(name)] = true
	}
	var chosen []scenario
	for _, s := range all {
		if want[s.name] {
			chosen = append(chosen, s)
			delete(want, s.name)
		}
	}
	for name := range want {
		return nil, errors.New(tagScenarioUnknown, "no scenario has this name", "name", name)
	}
	return chosen, nil
}

// describe is the text of an error with its tag and metadata, for the line that
// says why a run failed.
func describe(err error) string {
	appErr, ok := errors.FromError(err)
	if !ok {
		return err.Error()
	}
	var b strings.Builder
	b.WriteString(appErr.Tag + ": " + appErr.Message)
	for _, k := range slices.Sorted(maps.Keys(appErr.Metadata)) {
		_, _ = fmt.Fprintf(&b, " %s=%v", k, appErr.Metadata[k])
	}
	if appErr.OriginalErr != nil {
		b.WriteString(" (" + appErr.OriginalErr.Error() + ")")
	}
	return b.String()
}

const tagScenarioUnknown = "ScenarioUnknown"
