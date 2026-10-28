package views_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// render renders the component to a string.
func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func TestAuthViews(t *testing.T) {
	tests := []struct {
		name    string
		c       templ.Component
		want    []string
		notWant []string
	}{
		{
			name: "login form without problems",
			c:    views.LoginForm(views.LoginData{}),
			want: []string{
				`<form id="login-form" method="post" action="/login"`,
				`data-on:submit="@post(&#39;/login&#39;, {contentType: &#39;form&#39;})"`,
				`<label for="login-username">Username</label>`, `id="login-username" name="username" type="text"`,
				`id="login-password" name="password" type="password"`, `autocomplete="current-password"`,
			},
			notWant: []string{"form-error", "field-error", "aria-invalid", `name="next"`},
		},
		{
			name: "login form with a form problem keeps the username and no password",
			c: views.LoginForm(views.LoginData{
				Username: "ali ce", Errors: views.FormErrors{Form: "Wrong username or password."},
			}),
			want: []string{
				`<p class="form-error" role="alert">Wrong username or password.</p>`, `value="ali ce"`,
			},
			notWant: []string{`type="password" value`, `type="password" autocomplete="current-password" value`},
		},
		{
			name: "login form with next",
			c:    views.LoginForm(views.LoginData{Next: "/rooms/1?a=b&c=d"}),
			want: []string{`<input type="hidden" name="next" value="/rooms/1?a=b&amp;c=d">`},
		},
		{
			name: "field error next to its input",
			c: views.LoginForm(views.LoginData{Errors: views.FormErrors{
				Fields: map[string]string{"username": "Taken <b>", "password": "Too short"},
			}}),
			want: []string{
				`aria-invalid="true" aria-describedby="login-username-error"`,
				`<p class="field-error" id="login-username-error" role="alert">Taken &lt;b&gt;</p>`,
				`<p class="field-error" id="login-password-error" role="alert">Too short</p>`,
			},
			notWant: []string{"<b>"},
		},
		{
			name: "login page",
			c:    views.LoginPage(views.LoginData{}),
			want: []string{"<title id=\"page-title\">Log in · Clowder</title>", `<div class="auth-card">`, "<h1>Log in</h1>", `id="login-form"`},
			// No sidebar for a person who is not logged in.
			notWant: []string{"<aside"},
		},
		{
			name: "sign-up form",
			c:    views.SignUpForm(views.SignUpData{Token: "abc_-123"}),
			want: []string{
				`<form id="signup-form" method="post" action="/invite/abc_-123"`,
				`data-on:submit="@post(&#39;/invite/abc_-123&#39;, {contentType: &#39;form&#39;})"`,
				`name="username"`, `name="displayName"`, `name="password"`, `autocomplete="new-password"`,
				"Create account</button>",
				`<p class="hint">3 to 32 characters: a to z, digits, _ and -.</p>`, `<p class="hint">At least 12 characters.</p>`,
			},
			notWant: []string{"field-error", "form-error"},
		},
		{
			name: "sign-up form escapes the token in the action",
			c:    views.SignUpForm(views.SignUpData{Token: "a'b\\c/d"}),
			want: []string{`action="/invite/a%27b%5Cc%2Fd"`, `@post(&#39;/invite/a%27b%5Cc%2Fd&#39;`},
			// The token cannot end the quoted string of the expression.
			notWant: []string{`a'b`},
		},
		{
			name: "sign-up form with problems keeps the input but not the password",
			c: views.SignUpForm(views.SignUpData{
				Token: "t", Username: "ab", DisplayName: "Dana",
				Errors: views.FormErrors{Fields: map[string]string{
					"username": "Use 3 to 32 characters.", "password": "Use at least 12 characters.",
				}},
			}),
			want: []string{
				`value="ab"`, `value="Dana"`,
				`id="signup-username-error"`, "Use 3 to 32 characters.",
				`id="signup-password-error"`, "Use at least 12 characters.",
			},
			// A hint gives way to the error of its field.
			notWant: []string{"signup-display-name-error", `class="hint"`},
		},
		{
			name: "sign-up page",
			c:    views.SignUpPage(views.SignUpData{Token: "t"}),
			want: []string{"<title id=\"page-title\">Sign up · Clowder</title>", "<h1>Join Clowder</h1>", `id="signup-form"`},
		},
		{
			name: "header of a member",
			c:    views.Header(views.SidebarData{User: app.User{DisplayName: "Alice", Role: app.RoleMember}}),
			want: []string{
				`<a class="logo" href="/">`, `<span class="logo-word">clowder</span>`,
				`<a class="header-link header-notifications" href="/notifications">`, `<a class="header-link header-messages" href="/messages">`,
				`id="sidebar-user">Alice</p>`, `<a class="user-menu-item" href="/me/settings">`, `href="/me/settings#sessions"`,
				`<form id="logout-form" class="user-menu-logout" method="post" action="/logout"`, "Log out</button>",
			},
			notWant: []string{"Admin", "/admin/users"},
		},
		{
			name: "header of an admin",
			c:    views.Header(views.SidebarData{User: app.User{DisplayName: "Ada", Role: app.RoleAdmin}}),
			want: []string{`id="sidebar-user">Ada</p>`, `<a class="user-menu-item" href="/admin/users">`, "Admin</a>", "Log out</button>"},
		},
		{
			name: "sidebar of a member",
			c:    views.Sidebar(views.SidebarData{User: app.User{DisplayName: "Alice", Role: app.RoleMember}}),
			want: []string{`<h2 class="sidebar-heading">Your rooms</h2>`, `<a href="/rooms">Browse rooms</a>`, `<a href="/rooms/new">New room</a>`},
			// What the old sidebar held moved to the header.
			notWant: []string{"Admin", "/admin/users", "logout-form", "sidebar-user", "Messages", "Notifications"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := render(t, tt.c)
			for _, w := range tt.want {
				if !strings.Contains(html, w) {
					t.Errorf("does not contain %q:\n%s", w, html)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(html, w) {
					t.Errorf("contains %q:\n%s", w, html)
				}
			}
		})
	}
}

func TestFormPost(t *testing.T) {
	tests := []struct{ path, want string }{
		{"/login", `@post('/login', {contentType: 'form'})`},
		{"/invite/abc", `@post('/invite/abc', {contentType: 'form'})`},
		{"/a'b", `@post('/a%27b', {contentType: 'form'})`},
		{`/a\b`, `@post('/a%5Cb', {contentType: 'form'})`},
	}
	for _, tt := range tests {
		if got := views.FormPost(tt.path); got != tt.want {
			t.Errorf("FormPost(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestLayoutShell(t *testing.T) {
	sb := views.SidebarData{User: app.User{DisplayName: "Alice"}}
	with := render(t, views.Layout("T", 0, &sb))
	for _, want := range []string{`<header id="site-header" class="site-header">`, `<aside id="sidebar">`, `<nav aria-label="Rooms" id="sidebar-rooms">`, `<main>`, `src="/static/notify.js"`} {
		if !strings.Contains(with, want) {
			t.Errorf("a page with a sidebar lacks %q:\n%s", want, with)
		}
	}
	// The header comes before the sidebar, and the sidebar before the main column.
	if h, a, m := strings.Index(with, "<header"), strings.Index(with, "<aside"), strings.Index(with, "<main>"); h >= a || a >= m {
		t.Errorf("order header %d, aside %d, main %d, want header < aside < main", h, a, m)
	}
	without := render(t, views.Layout("T", 0, nil))
	for _, want := range []string{`<div class="plain-shell">`, `<p class="plain-logo">`, `<a class="logo" href="/">`, `<main>`} {
		if !strings.Contains(without, want) {
			t.Errorf("a page without a sidebar lacks %q:\n%s", want, without)
		}
	}
	for _, notWant := range []string{"<aside", "<header", "notify.js", "user-menu"} {
		if strings.Contains(without, notWant) {
			t.Errorf("a page without a sidebar has %q:\n%s", notWant, without)
		}
	}
}
