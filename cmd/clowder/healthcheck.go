package main

import (
	"context"
	"flag"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/errors"
)

// healthcheckTimeout is how long the check waits for the answer of the
// server.
const healthcheckTimeout = 3 * time.Second

// runHealthcheck asks GET /healthz of the server that listens on -addr
// (CLOWDER_ADDR, the same setting as serve) and returns nil when it answers
// 200. The image of Docker runs it as its HEALTHCHECK, because the image has
// no shell and no curl (SPEC D40). Any other outcome is an error, which main
// prints as one line and turns into the exit status 1.
func runHealthcheck(ctx context.Context, args []string, env func(string) string, stderr io.Writer) error {
	var addr string
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&addr, "addr", envString(env, "CLOWDER_ADDR", config.DefaultListenAddr),
		"address the server listens on, as host:port (CLOWDER_ADDR)")
	if err := fs.Parse(args); err != nil {
		return errors.NewErr(tagFlagsInvalid, err)
	}
	if fs.NArg() > 0 {
		return errors.New(tagFlagsInvalid, "unexpected arguments", "args", fs.Args())
	}
	target, err := healthURL(addr)
	if err != nil {
		return err
	}
	return checkHealth(ctx, target, healthcheckTimeout)
}

// healthURL is the address of the health check of the server that listens on
// addr. An address with no host, or with the unspecified address (0.0.0.0,
// ::), means this machine, so the check goes to 127.0.0.1; any other host is
// used as it is.
func healthURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", errors.New(tagHealthcheckAddrInvalid, "healthcheck: the address must be host:port, for example :8080",
			"addr", addr, "reason", err.Error())
	}
	if n, convErr := strconv.Atoi(port); convErr != nil || n < 1 || n > 65535 {
		return "", errors.New(tagHealthcheckAddrInvalid, "healthcheck: the port of the address must be a number from 1 to 65535",
			"addr", addr)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

// checkHealth asks the health check at target and returns nil when the answer
// is 200. It gives up after timeout and does not follow redirects.
func checkHealth(ctx context.Context, target string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return errors.NewErr(tagHealthcheckRequestInvalid, err).SetMetadata("url", target)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.NewErr(tagHealthcheckRequestFailed, err).SetMetadata("url", target).
			SetMessage("healthcheck: " + err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	// The body is small and not needed; drain a little so the connection ends cleanly.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return errors.New(tagHealthcheckUnhealthy, "healthcheck: the server answered "+strconv.Itoa(resp.StatusCode)+", not 200",
			"url", target, "status", resp.StatusCode)
	}
	return nil
}

const (
	tagHealthcheckAddrInvalid    = "HealthcheckAddrInvalid"
	tagHealthcheckRequestInvalid = "HealthcheckRequestInvalid"
	tagHealthcheckRequestFailed  = "HealthcheckRequestFailed"
	tagHealthcheckUnhealthy      = "HealthcheckUnhealthy"
)
