//go:build clowder_browser

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/errors"
)

// apiLoginTimeout bounds the login through the API.
const apiLoginTimeout = 30 * time.Second

// apiLogin logs the demo user in through POST /api/v1/auth/login of the server
// at baseURL, as a client named clientName, and leaves the session in place:
// it does not log out, so that the page of the sessions shows it next to the
// browser's. The token is dropped.
func apiLogin(ctx context.Context, baseURL, username, clientName string) error {
	body, err := json.Marshal(map[string]string{
		"username": username, "password": browsertest.DemoPassword, "client_name": clientName,
	})
	if err != nil {
		return errors.NewErr(tagAPILoginFailed, err).SetMetadata("user", username)
	}
	ctx, cancel := context.WithTimeout(ctx, apiLoginTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return errors.NewErr(tagAPILoginFailed, err).SetMetadata("user", username)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.NewErr(tagAPILoginFailed, err).SetMetadata("user", username, "url", baseURL)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New(tagAPILoginFailed, "the API did not accept the login",
			"user", username, "url", baseURL, "status", resp.StatusCode)
	}
	return nil
}

const tagAPILoginFailed = "APILoginFailed"
