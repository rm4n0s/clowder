package main

import (
	"context"
	"strings"

	"github.com/rm4n0s/errors"
)

// runner is what a scenario works with: the client and who it logs in as.
type runner struct {
	c          *client
	username   string
	password   string
	clientName string
}

// scenario is a named list of steps against the API. A later step of the plan
// adds its own to scenarios.
type scenario struct {
	name    string
	summary string
	run     func(ctx context.Context, r *runner) error
}

// scenarios lists the scenarios in the order they run.
func scenarios() []scenario {
	return []scenario{
		{name: "session", summary: "log in, read who I am and my sessions, find users, log out", run: sessionScenario},
	}
}

// login logs in as the runner's user and keeps the token for the next
// requests. The token and the password are hidden in everything printed.
func (r *runner) login(ctx context.Context) (answer, error) {
	r.c.redac.add(r.password)
	a, err := r.c.do(ctx, "POST", "/api/v1/auth/login", map[string]string{
		"username": r.username, "password": r.password, "client_name": r.clientName,
	}, 200)
	if err != nil {
		return a, err
	}
	token, _ := a.field("token").(string)
	if token == "" {
		return a, errors.New(tagAnswerUnexpected, "the login answer has no token")
	}
	r.c.token = token
	r.c.redac.add(token)
	return a, nil
}

// sessionScenario is the scenario of step 16.1: login, /me, the sessions, the
// users, logout, and a check that the token is dead.
func sessionScenario(ctx context.Context, r *runner) error {
	a, err := r.login(ctx)
	if err != nil {
		return err
	}
	if got := a.field("user", "username"); got != r.username {
		return errors.New(tagAnswerUnexpected, "the login answer names another user", "got", got)
	}

	a, err = r.c.do(ctx, "GET", "/api/v1/me", nil, 200)
	if err != nil {
		return err
	}
	if got := a.field("user", "username"); got != r.username {
		return errors.New(tagAnswerUnexpected, "/me names another user", "got", got)
	}

	a, err = r.c.do(ctx, "GET", "/api/v1/me/sessions", nil, 200)
	if err != nil {
		return err
	}
	if !hasCurrentSession(a, r.clientName) {
		return errors.New(tagAnswerUnexpected, "the list of sessions has no current api session of this client",
			"client", r.clientName)
	}

	prefix := r.username
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	a, err = r.c.do(ctx, "GET", "/api/v1/users?q="+prefix, nil, 200)
	if err != nil {
		return err
	}
	if !hasUser(a, r.username) {
		return errors.New(tagAnswerUnexpected, "the users do not include the user", "q", prefix)
	}

	if _, err := r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	// The token is dead now.
	if _, err := r.c.do(ctx, "GET", "/api/v1/me", nil, 401); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}

// hasCurrentSession reports whether the answer of GET /me/sessions has an api
// session of the client that is the current one.
func hasCurrentSession(a answer, clientName string) bool {
	list, _ := a.field("sessions").([]any)
	for _, item := range list {
		s, _ := item.(map[string]any)
		if s["kind"] == "api" && s["client_name"] == clientName && s["current"] == true {
			return true
		}
	}
	return false
}

// hasUser reports whether the answer of GET /users lists the username.
func hasUser(a answer, username string) bool {
	list, _ := a.field("users").([]any)
	for _, item := range list {
		u, _ := item.(map[string]any)
		if name, _ := u["username"].(string); strings.EqualFold(name, username) {
			return true
		}
	}
	return false
}

const tagAnswerUnexpected = "AnswerUnexpected"
