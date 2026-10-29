package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
)

// postJSON sends a JSON body and returns the status, the headers and the body.
func postJSON(t *testing.T, target string, body any) (int, http.Header, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(out)
}

// TestServeLimitsPerUser runs serve and shows that the limits of SPEC D62 are
// wired: one counter of live connections is shared by the API, 30 WebSockets
// of a user are accepted and the 31st is 429, the sign-up with an invitation
// that is not valid is limited by address, and the refusals are in the
// metrics.
func TestServeLimitsPerUser(t *testing.T) {
	dir := t.TempDir()
	var link bytes.Buffer
	if err := runInvite(t.Context(), []string{"-admin", "-data", dir}, mapEnv(nil), &link, io.Discard, auth.FastHasher()); err != nil {
		t.Fatalf("runInvite: %+v", err)
	}
	token := strings.TrimSpace(strings.SplitN(link.String(), "\n", 2)[0])
	token = token[strings.LastIndex(token, "/")+1:]

	webAddr, metricsAddr, stop := serveFor(t, "-data", dir, "-metrics-addr", "127.0.0.1:0")
	defer stop()
	base := "http://" + webAddr

	status, _, body := postJSON(t, base+"/api/v1/auth/signup", map[string]any{
		"invitation_token": token, "username": "founder", "display_name": "Founder",
		"password": "a password of 12+", "client_name": "limits test",
	})
	if status != http.StatusCreated {
		t.Fatalf("sign-up = %d: %s", status, body)
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.Token == "" {
		t.Fatalf("sign-up answer %q: %v", body, err)
	}

	dial := func(ctx context.Context) (*websocket.Conn, *http.Response, error) {
		return websocket.Dial(ctx, "ws://"+webAddr+"/api/v1/ws", &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + created.Token}}})
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for i := range app.MaxConnectionsPerUser {
		conn, resp, err := dial(ctx)
		if err != nil {
			t.Fatalf("WebSocket %d: %v (%v)", i+1, err, resp)
		}
		defer func() { _ = conn.CloseNow() }()
	}
	conn, resp, err := dial(ctx)
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("the 31st WebSocket was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "30" {
		t.Fatalf("the 31st WebSocket: %v, want 429 with Retry-After 30", resp)
	}
	_ = resp.Body.Close()

	// Five sign-ups with an invitation that is not valid block the address.
	for i := range 5 {
		status, _, body := postJSON(t, base+"/api/v1/auth/signup", map[string]any{
			"invitation_token": "no-such-token", "username": "mallory", "display_name": "Mallory",
			"password": "a password of 12+", "client_name": "limits test",
		})
		if status != http.StatusNotFound {
			t.Fatalf("invalid sign-up %d = %d: %s", i+1, status, body)
		}
	}
	status, header, _ := postJSON(t, base+"/api/v1/auth/signup", map[string]any{
		"invitation_token": "no-such-token", "username": "mallory", "display_name": "Mallory",
		"password": "a password of 12+", "client_name": "limits test",
	})
	if status != http.StatusTooManyRequests || header.Get("Retry-After") == "" {
		t.Fatalf("the sign-up past the limit = %d, Retry-After %q, want 429", status, header.Get("Retry-After"))
	}

	_, _, metricsBody := scrapeURL(t, http.MethodGet, "http://"+metricsAddr+"/metrics")
	for _, want := range []string{
		`clowder_limits_refused_total{limit="connections"} 1`,
		`clowder_limits_refused_total{limit="signup"} 1`,
		`clowder_limits_refused_total{limit="staged"} 0`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Errorf("the metrics lack %q:\n%s", want, metricsBody)
		}
	}
}
