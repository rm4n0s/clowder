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
