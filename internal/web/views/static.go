package views

import (
	"context"
	"net/url"
)

// staticVersionKey is the context key of the version of the static files.
type staticVersionKey struct{}

// WithStaticVersion returns ctx with the version of the embedded static files
// (the hash the server puts in /sw.js too). StaticURL then adds it to the
// address of every static file a page names, so that a page of a new release
// asks for new addresses: a browser or the service worker that kept the old
// files under the old addresses cannot answer them (SPEC D58).
func WithStaticVersion(ctx context.Context, version string) context.Context {
	return context.WithValue(ctx, staticVersionKey{}, version)
}

// StaticURL is the address of a static file for the page rendered in ctx: path
// (such as "/static/app.css") with "?v=" and the version of the static files
// when ctx has one (WithStaticVersion), and path alone otherwise (a component
// rendered in a context that was not made by the server).
func StaticURL(ctx context.Context, path string) string {
	version, _ := ctx.Value(staticVersionKey{}).(string)
	if version == "" {
		return path
	}
	return path + "?v=" + url.QueryEscape(version)
}
