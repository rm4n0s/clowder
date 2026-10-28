package views_test

import (
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestLogo(t *testing.T) {
	t.Parallel()
	html := render(t, views.Logo())
	for _, want := range []string{
		`<a class="logo" href="/">`,
		`<svg class="logo-mark" viewBox="0 0 32 32"`, `aria-hidden="true"`,
		`d="M5 27V8l7 6h8l7-6v19a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2z"`,
		`<span class="logo-word">clowder</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the logo lacks %q:\n%s", want, html)
		}
	}
	if got := strings.Count(html, `class="logo-eye"`); got != 2 {
		t.Errorf("the cat has %d eyes, want 2", got)
	}
	if strings.Contains(html, "style=") {
		t.Errorf("the logo has an inline style (the CSP refuses it):\n%s", html)
	}
}

func TestIcons(t *testing.T) {
	t.Parallel()
	icons := map[string]templ.Component{
		"bell": views.IconBell(), "message": views.IconMessage(), "search": views.IconSearch(), "lock": views.IconLock(),
		"muted": views.IconMuted(), "paw": views.IconPaw(), "heart": views.IconHeart(), "attach": views.IconAttach(),
		"more": views.IconMore(), "settings": views.IconSettings(), "logout": views.IconLogout(), "sun": views.IconSun(),
		"moon": views.IconMoon(), "monitor": views.IconMonitor(), "plus": views.IconPlus(), "check": views.IconCheck(),
		"close": views.IconClose(), "menu": views.IconMenu(), "board": views.IconBoard(), "feed": views.IconFeed(),
		"tag": views.IconTag(), "calendar": views.IconCalendar(), "file": views.IconFile(), "task": views.IconTask(), "task-done": views.IconTaskDone(),
	}
	for name, c := range icons {
		t.Run(name, func(t *testing.T) {
			html := render(t, c)
			for _, want := range []string{`<svg class="icon" viewBox="0 0 24 24"`, `aria-hidden="true"`, `focusable="false"`, "currentColor"} {
				if !strings.Contains(html, want) {
					t.Errorf("the %s icon lacks %q:\n%s", name, want, html)
				}
			}
			for _, notWant := range []string{"style=", "<script", "<image", "href=", "<text"} {
				if strings.Contains(html, notWant) {
					t.Errorf("the %s icon has %q:\n%s", name, notWant, html)
				}
			}
			if strings.Count(html, "<svg") != 1 || !strings.Contains(html, "<path") && !strings.Contains(html, "<circle") && !strings.Contains(html, "<rect") && !strings.Contains(html, "<ellipse") {
				t.Errorf("the %s icon draws nothing:\n%s", name, html)
			}
		})
	}
}

func TestThemeMenu(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		theme   app.Theme
		pressed string
	}{
		{"unset is System", "", "system"},
		{"system", app.ThemeSystem, "system"},
		{"light", app.ThemeLight, "light"},
		{"dark", app.ThemeDark, "dark"},
		{"anything else is System", "sepia", "system"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := render(t, views.ThemeMenu(tt.theme))
			for _, want := range []string{
				`<form id="theme-menu" class="theme-menu" method="post" action="/me/settings/appearance?from=menu"`,
				`name="theme" value="system"`, `name="theme" value="light"`, `name="theme" value="dark"`,
			} {
				if !strings.Contains(html, want) {
					t.Errorf("the theme menu lacks %q:\n%s", want, html)
				}
			}
			if got := strings.Count(html, `aria-pressed="true"`); got != 1 {
				t.Fatalf("%d choices are marked, want 1:\n%s", got, html)
			}
			if got := strings.Count(html, `theme-option current`); got != 1 {
				t.Errorf("%d choices have the class current, want 1", got)
			}
			// The marked button is the one of the theme.
			marked := html[strings.Index(html, `aria-pressed="true"`)-120 : strings.Index(html, `aria-pressed="true"`)]
			if !strings.Contains(marked, `value="`+tt.pressed+`"`) {
				t.Errorf("the marked choice is not %s: %s", tt.pressed, marked)
			}
		})
	}
}

func TestUserMenu(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		user    app.User
		theme   app.Theme
		want    []string
		notWant []string
	}{
		{
			name: "member",
			user: app.User{ID: 4, DisplayName: "Bob <b>", Role: app.RoleMember},
			want: []string{
				`<details class="user-menu" id="user-menu" data-preserve-attr="open">`,
				`<summary class="user-menu-button" aria-label="Account menu">`,
				`<span id="header-avatar">`, `id="sidebar-user">Bob &lt;b&gt;</p>`,
				`href="/me/settings"`, `href="/me/settings#sessions"`, `id="logout-form"`, `id="theme-menu"`,
			},
			notWant: []string{"/admin/users", "<b>"},
		},
		{
			name: "admin",
			user: app.User{ID: 1, DisplayName: "Ada", Role: app.RoleAdmin},
			want: []string{`href="/admin/users"`, "Admin</a>"},
		},
		{
			name:  "the theme of the user is marked",
			user:  app.User{ID: 2, DisplayName: "Dee", Role: app.RoleMember},
			theme: app.ThemeDark,
			want:  []string{`value="dark" class="theme-option current" aria-pressed="true"`},
		},
		{
			name: "picture",
			user: app.User{ID: 3, DisplayName: "Pat", Role: app.RoleMember, AvatarKey: "k1"},
			want: []string{`<img class="avatar avatar-md" src="/avatars/3?v=k1"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := render(t, views.UserMenu(views.SidebarData{User: tt.user, Theme: tt.theme}))
			for _, w := range tt.want {
				if !strings.Contains(html, w) {
					t.Errorf("the user menu lacks %q:\n%s", w, html)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(html, w) {
					t.Errorf("the user menu has %q:\n%s", w, html)
				}
			}
			if strings.Contains(html, `type="hidden"`) {
				t.Errorf("the user menu has a hidden input, which a page that checks for its own would trip over:\n%s", html)
			}
		})
	}
}

// TestHeaderIDs checks the ids that the streams patch: the counts, which stay
// in the header, the title and the avatar's holder.
func TestHeaderIDs(t *testing.T) {
	t.Parallel()
	html := render(t, views.Header(views.SidebarData{User: app.User{DisplayName: "Eli"}, Unread: 3, UnreadMessages: 2}))
	for _, want := range []string{
		`<span id="notification-count" class="count-badge" title="3 unread notifications">3</span>`,
		`<span id="message-count" class="count-badge" title="2 conversations with unread messages">2</span>`,
		`id="header-avatar"`, `id="sidebar-user"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the header lacks %q:\n%s", want, html)
		}
	}
	// Each id appears once.
	for _, id := range []string{"notification-count", "message-count", "header-avatar", "sidebar-user", "user-menu", "logout-form", "theme-menu"} {
		if n := strings.Count(html, `id="`+id+`"`); n != 1 {
			t.Errorf("id %s appears %d times, want 1", id, n)
		}
	}
}

func TestFeedNewLineHasThePaw(t *testing.T) {
	t.Parallel()
	html := render(t, views.FeedNewLine())
	for _, want := range []string{`id="feed-new-line"`, `class="feed-new-line"`, `<svg class="icon"`, "<ellipse", "New since your last visit"} {
		if !strings.Contains(html, want) {
			t.Errorf("the line lacks %q:\n%s", want, html)
		}
	}
}
