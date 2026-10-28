package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPagesWithoutALoginHaveTheLogoAndACard: the login page and the error
// pages are a card on the ground with the logo above it, and have no header,
// no sidebar and no user menu (step 21.1).
func TestPagesWithoutALoginHaveTheLogoAndACard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		path   string
		status int
		want   []string
	}{
		{"login", "/login", http.StatusOK, []string{`<div class="auth-card">`, `<h1>Log in</h1>`}},
		{"not found", "/no-such-page", http.StatusNotFound, []string{`<div class="auth-card error-card">`, `<h1>404</h1>`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newTestServer(t)
			rec := do(s.Handler(), httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			body := rec.Body.String()
			for _, want := range append(tt.want, `<div class="plain-shell">`, `<p class="plain-logo">`, `<a class="logo" href="/">`, `<span class="logo-word">clowder</span>`, `href="/static/app.css?v=`) {
				if !strings.Contains(body, want) {
					t.Errorf("body lacks %q:\n%s", want, body)
				}
			}
			for _, notWant := range []string{"<header", "<aside", "user-menu", "sidebar", "notify.js"} {
				if strings.Contains(body, notWant) {
					t.Errorf("body has %q:\n%s", notWant, body)
				}
			}
			// The logo comes before the card.
			if logo, card := strings.Index(body, `<a class="logo"`), strings.Index(body, `auth-card`); logo < 0 || card < 0 || logo > card {
				t.Errorf("logo at %d, card at %d, want the logo first", logo, card)
			}
		})
	}
}
