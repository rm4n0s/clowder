package views_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestHome(t *testing.T) {
	var buf bytes.Buffer
	if err := views.Home(views.SidebarData{User: app.User{DisplayName: "Alice", Username: "alice", Role: app.RoleMember}}).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render Home: %v", err)
	}
	html := buf.String()

	tests := []struct {
		name string
		want string
	}{
		{"doctype", "<!doctype html>"},
		{"charset", `<meta charset="utf-8">`},
		{"viewport", `<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">`},
		{"title", "<title id=\"page-title\">Home · Clowder</title>"},
		{"css link", `<link rel="stylesheet" href="/static/app.css">`},
		{"code css link", `<link rel="stylesheet" href="/static/code.css">`},
		{"code script", `<script src="/static/code.js" defer></script>`},
		{"datastar module script", `<script type="module" src="/static/datastar.js"></script>`},
		{"sidebar", `<aside id="sidebar" popover>`},
		{"main", "<main>"},
		{"heading", "<h1>Clowder</h1>"},
		{"signed in as", `<p id="signed-in-as">Signed in as Alice</p>`},
		{"logout button", `<form id="logout-form" class="user-menu-logout" method="post" action="/logout"`},
		{"header", `<header id="site-header" class="site-header">`},
		{"logo", `<a class="logo" href="/">`},
		{"datastar check element", `id="datastar-check"`},
		{"datastar check signal", `data-signals="{status: 'Datastar is running'}"`},
		{"datastar check text", `data-text="$status"`},
		{"datastar check fallback", "Datastar is not running"},
	}
	lower := strings.ToLower(html)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(lower, strings.ToLower(tt.want)) {
				t.Errorf("rendered page does not contain %q:\n%s", tt.want, html)
			}
		})
	}
}
