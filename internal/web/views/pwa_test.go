package views_test

import (
	"context"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/static"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// TestLayoutHead: every page links the manifest, and the viewport covers the
// notch (SPEC D58); the pages with a login and without one both do.
func TestLayoutHead(t *testing.T) {
	sb := views.SidebarData{User: app.User{DisplayName: "Alice"}}
	tests := []struct {
		name string
		html string
	}{
		{"with a login", render(t, views.Layout("T", 0, &sb))},
		{"without a login", render(t, views.Layout("T", 0, nil))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, want := range []string{`<link rel="manifest" href="/manifest.webmanifest">`, `content="width=device-width, initial-scale=1, viewport-fit=cover"`} {
				if !strings.Contains(tt.html, want) {
					t.Errorf("the head lacks %q:\n%s", want, tt.html)
				}
			}
		})
	}
}

// TestThemeColor: the System theme writes one theme-color element for each
// palette, and a chosen theme writes the one of that theme alone.
func TestThemeColor(t *testing.T) {
	tests := []struct {
		name    string
		theme   string
		want    []string
		notWant []string
	}{
		{"system", "", []string{
			`<meta name="theme-color" content="#fbf6ee" media="(prefers-color-scheme: light)">`,
			`<meta name="theme-color" content="#1a1512" media="(prefers-color-scheme: dark)">`,
		}, nil},
		{"system, spelled out", "system", []string{`media="(prefers-color-scheme: light)"`, `media="(prefers-color-scheme: dark)"`}, nil},
		{"light", "light", []string{`<meta name="theme-color" content="#fbf6ee">`}, []string{"#1a1512", "media="}},
		{"dark", "dark", []string{`<meta name="theme-color" content="#1a1512">`}, []string{"#fbf6ee", "media="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			if err := views.ThemeColor().Render(views.WithTheme(context.Background(), tt.theme), &sb); err != nil {
				t.Fatalf("render: %v", err)
			}
			got := sb.String()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("lacks %q:\n%s", want, got)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("has %q:\n%s", notWant, got)
				}
			}
		})
	}
}

// TestThemeColorsAreTheGrounds: the two colours of the theme-color elements are
// the ground of the light and of the dark palette in app.css.
func TestThemeColorsAreTheGrounds(t *testing.T) {
	css, err := static.FS.ReadFile("app.css")
	if err != nil {
		t.Fatal(err)
	}
	// The first --c-ground is the light palette's, the second the dark one's.
	grounds := regexp.MustCompile(`--c-ground:\s*(#[0-9a-fA-F]{6});`).FindAllStringSubmatch(string(css), -1)
	if len(grounds) < 2 {
		t.Fatalf("found %d --c-ground declarations, want at least 2", len(grounds))
	}
	if got := strings.ToLower(grounds[0][1]); got != views.ThemeColorLight {
		t.Errorf("light ground = %s, ThemeColorLight = %s", got, views.ThemeColorLight)
	}
	if got := strings.ToLower(grounds[1][1]); got != views.ThemeColorDark {
		t.Errorf("dark ground = %s, ThemeColorDark = %s", got, views.ThemeColorDark)
	}
}

// TestMenuButtonAndDrawer: the header has the button that opens the sidebar,
// and the sidebar is a popover with its close button, which has autofocus (it is
// what moves the focus into the drawer).
func TestMenuButtonAndDrawer(t *testing.T) {
	sb := views.SidebarData{User: app.User{DisplayName: "Alice"}}
	header := render(t, views.PageHeader(sb))
	for _, want := range []string{`id="menu-button"`, `popovertarget="sidebar"`, `aria-controls="sidebar"`, `>Rooms</span>`, `menu-dot`} {
		if !strings.Contains(header, want) {
			t.Errorf("the header lacks %q:\n%s", want, header)
		}
	}
	aside := render(t, views.PageSidebar(sb))
	for _, want := range []string{`<aside id="sidebar" popover>`, `class="drawer-close"`, `popovertarget="sidebar"`, `popovertargetaction="hide"`, `autofocus`} {
		if !strings.Contains(aside, want) {
			t.Errorf("the sidebar lacks %q:\n%s", want, aside)
		}
	}
}

// TestStaticURL: a static address gets ?v= and the version when the context has
// one, and is left as it is when it has none.
func TestStaticURL(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"no version", "", "/static/app.css"},
		{"a version", "abc123", "/static/app.css?v=abc123"},
		{"a version is escaped", "a b&c", "/static/app.css?v=a+b%26c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.version != "" {
				ctx = views.WithStaticVersion(ctx, tt.version)
			}
			if got := views.StaticURL(ctx, "/static/app.css"); got != tt.want {
				t.Errorf("StaticURL = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHeadNamesStaticFilesWithTheVersion: every static file of the head, the
// favicon in the head and the favicon of a patch, and the scripts of the board
// and of the conversations, carry ?v= and the version of the context.
func TestHeadNamesStaticFilesWithTheVersion(t *testing.T) {
	ctx := views.WithStaticVersion(context.Background(), "v123")
	sb := views.SidebarData{User: app.User{DisplayName: "Alice"}}
	renderIn := func(c interface {
		Render(context.Context, io.Writer) error
	}) string {
		var sb strings.Builder
		if err := c.Render(ctx, &sb); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	page := renderIn(views.Layout("T", 0, &sb))
	for _, want := range []string{
		`href="/static/favicon.svg?v=v123"`, `href="/static/favicon-32.png?v=v123"`, `href="/static/apple-touch-icon.png?v=v123"`,
		`href="/static/app.css?v=v123"`, `href="/static/code.css?v=v123"`,
		`src="/static/datastar.js?v=v123"`, `src="/static/code.js?v=v123"`, `src="/static/notify.js?v=v123"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the head lacks %q:\n%s", want, page)
		}
	}
	// Every /static/ address in the page has the version.
	for _, m := range regexp.MustCompile(`(?:href|src)="(/static/[^"]*)"`).FindAllStringSubmatch(page, -1) {
		if !strings.HasSuffix(m[1], "?v=v123") {
			t.Errorf("the page names %q without the version", m[1])
		}
	}
	// The favicon that a stream patches in, with and without the dot.
	for _, tt := range []struct {
		unread int
		want   string
	}{{0, `href="/static/favicon.svg?v=v123"`}, {3, `href="/static/favicon-dot.svg?v=v123"`}} {
		if got := renderIn(views.Favicon(tt.unread)); !strings.Contains(got, tt.want) {
			t.Errorf("Favicon(%d) = %s, want %q", tt.unread, got, tt.want)
		}
	}
	// A component rendered without a version names the plain address.
	var plain strings.Builder
	if err := views.Favicon(0).Render(context.Background(), &plain); err != nil || !strings.Contains(plain.String(), `href="/static/favicon.svg"`) {
		t.Errorf("Favicon without a version = %s (%v)", plain.String(), err)
	}
}
