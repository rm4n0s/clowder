package views

import "context"

// themeKey is the context key of the theme of the page.
type themeKey struct{}

// WithTheme returns ctx with the theme of the person who gets the page, which
// Layout writes as data-theme on the html element. Only "light" and "dark"
// are written; anything else, such as "system", leaves the attribute out and
// the browser's own setting decides.
func WithTheme(ctx context.Context, theme string) context.Context {
	return context.WithValue(ctx, themeKey{}, theme)
}

// themeOf returns the value of data-theme for the page, or "" for none.
func themeOf(ctx context.Context) string {
	theme, _ := ctx.Value(themeKey{}).(string)
	if theme == "light" || theme == "dark" {
		return theme
	}
	return ""
}

// The two colours of the theme-color elements: the ground of the light and of
// the dark palette (--c-ground in app.css; a test keeps them equal), which the
// browser draws behind its own bars on a phone.
const (
	// ThemeColorLight is the ground of the light palette.
	ThemeColorLight = "#fbf6ee"
	// ThemeColorDark is the ground of the dark palette.
	ThemeColorDark = "#1a1512"
)
