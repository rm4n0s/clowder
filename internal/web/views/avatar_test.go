package views_test

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestInitials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"two words", "Alice Smith", "AS"},
		{"three words", "Anna Maria Lopez", "AL"},
		{"one word", "alice", "A"},
		{"lower case", "bob dylan", "BD"},
		{"extra spaces", "  carol   ", "C"},
		{"punctuation", "O'Neil, Dan", "OD"},
		{"non-latin", "élan ünal", "ÉÜ"},
		{"greek", "αλέξης", "Α"},
		{"emoji only", "🐱", "?"},
		{"empty", "", "?"},
		{"digits", "42", "4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := views.Initials(tt.in); got != tt.want {
				t.Errorf("Initials(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAvatarColourIndex(t *testing.T) {
	t.Parallel()
	seen := map[int]bool{}
	for id := uint64(1); id <= 64; id++ {
		i := views.AvatarColourIndex(id)
		if i < 0 || i > 7 {
			t.Fatalf("index of %d = %d, want 0 to 7", id, i)
		}
		if i != views.AvatarColourIndex(id) || i != views.AvatarColourIndex(id+8) {
			t.Errorf("index of %d is not stable", id)
		}
		seen[i] = true
	}
	if len(seen) != 8 {
		t.Errorf("%d colours used, want all 8", len(seen))
	}
}

func TestAvatar(t *testing.T) {
	t.Parallel()
	picture := renderHTML(t, views.Avatar(7, "Ada Lovelace", "/avatars/7?v=abc"))
	requireHTML(t, picture, []string{`<img class="avatar avatar-sm" src="/avatars/7?v=abc" alt="" width="24" height="24" loading="lazy" decoding="async">`}, []string{"avatar-c", "AL"})
	initials := renderHTML(t, views.Avatar(7, "Ada Lovelace", ""))
	requireHTML(t, initials, []string{`<span class="avatar avatar-sm avatar-c7" aria-hidden="true">AL</span>`}, []string{"<img"})
	large := renderHTML(t, views.AvatarSized(9, "bob", "", "lg"))
	requireHTML(t, large, []string{`class="avatar avatar-lg avatar-c1"`, ">B<"}, nil)
	medium := renderHTML(t, views.AvatarSized(9, "bob", "/avatars/9?v=k", "md"))
	requireHTML(t, medium, []string{`width="36" height="36"`}, nil)
	// A name with markup is text.
	html := renderHTML(t, views.Avatar(1, "<b>x</b> y", ""))
	requireHTML(t, html, nil, []string{"<b>"})
}

func TestLayoutTheme(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		theme string
		want  string
	}{
		{"none", "", `<html lang="en">`},
		{"system", "system", `<html lang="en">`},
		{"unknown", "neon", `<html lang="en">`},
		{"light", "light", `<html lang="en" data-theme="light">`},
		{"dark", "dark", `<html lang="en" data-theme="dark">`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			ctx := views.WithTheme(context.Background(), tt.theme)
			if err := views.Home(views.SidebarData{User: app.User{DisplayName: "Alice"}}).Render(ctx, &buf); err != nil {
				t.Fatal(err)
			}
			requireHTML(t, buf.String(), []string{tt.want}, nil)
		})
	}
	var buf bytes.Buffer
	if err := views.Home(views.SidebarData{}).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	requireHTML(t, buf.String(), []string{`<html lang="en">`}, []string{"data-theme"})
}

func TestSettingsPageSections(t *testing.T) {
	t.Parallel()
	user := app.User{ID: 4, Username: "ada", DisplayName: "Ada L", AvatarKey: "k1"}
	d := views.SettingsData{
		Sidebar:    views.SidebarData{User: user},
		Profile:    views.ProfileData{User: user, DisplayName: "typed", Errors: views.FormErrors{Fields: map[string]string{"displayName": "Bad name.", "avatar": "Bad picture."}}},
		Password:   views.PasswordData{Errors: views.FormErrors{Form: "Too many.", Fields: map[string]string{"currentPassword": "Wrong."}}, Saved: true},
		Appearance: views.AppearanceData{Theme: app.ThemeDark, Errors: views.FormErrors{Fields: map[string]string{"theme": "No such theme."}}},
	}
	html := renderHTML(t, views.SettingsPage(d))
	requireHTML(t, html, []string{
		`value="typed"`, `id="profile-display-name-error"`, "Bad name.", `id="avatar-file-error"`, "Bad picture.",
		`<img class="avatar avatar-lg" src="/avatars/4?v=k1"`, `id="avatar-remove-form"`,
		`<p class="form-error" role="alert">Too many.</p>`, `id="password-current-error"`, "Wrong.", `<p class="saved" role="status">Saved</p>`,
		`value="dark" checked`, "No such theme.",
	}, []string{`value="system" checked`, "A PNG, JPEG or GIF of up to 2 MiB"})
	for _, f := range []string{`name="currentPassword" type="password"`, `name="newPassword" type="password"`} {
		if !strings.Contains(html, f) {
			t.Errorf("no input %s", f)
		}
	}
	// A password is never put back, whatever the form had.
	if regexp.MustCompile(`type="password"[^>]*value=`).MatchString(html) || regexp.MustCompile(`value="[^"]*"[^>]*type="password"`).MatchString(html) {
		t.Error("a password input has a value")
	}
	if got := strconv.Itoa(strings.Count(html, "<section id=")); got != "6" {
		t.Errorf("%s sections, want 6", got)
	}
}

// TestAvatarPalette checks that the stylesheet has the eight classes of the
// palette and that each takes its text and its ground from the pair of tokens
// of its number. That each pair reaches 4.5:1 in the light and in both dark
// palettes is checked by TestContrast of internal/web/static.
func TestAvatarPalette(t *testing.T) {
	t.Parallel()
	css, err := os.ReadFile("../static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\.avatar-c(\d) \{ color: var\(--c-avatar-(\d)-text\); background: var\(--c-avatar-(\d)-bg\); \}`)
	found := re.FindAllStringSubmatch(string(css), -1)
	if len(found) != 8 {
		t.Fatalf("%d avatar classes in the stylesheet, want 8", len(found))
	}
	for _, m := range found {
		if m[1] != m[2] || m[1] != m[3] {
			t.Errorf("avatar-c%s uses the tokens %s and %s", m[1], m[2], m[3])
		}
	}
	if got := views.AvatarColourIndex(11); got != 3 {
		t.Errorf("AvatarColourIndex(11) = %d, want 3", got)
	}
}
