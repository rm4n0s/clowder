//go:build clowder_browser

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/errors"
)

// TestResolveRooms finds the demo room "secret" in alice's sidebar, and fails
// for a room she is not in.
func TestResolveRooms(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, time.Minute)
	defer cancel()
	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}

	got, err := resolveRooms(ctx, "{room:secret}?x=1")
	if err != nil {
		t.Fatalf("resolveRooms: %+v", err)
	}
	if !strings.HasPrefix(got, "/rooms/") || strings.Contains(got, "{") || !strings.HasSuffix(got, "?x=1") {
		t.Errorf("page = %q, want /rooms/<id>?x=1", got)
	}
	if got, err := resolveRooms(ctx, "/plain"); err != nil || got != "/plain" {
		t.Errorf("resolveRooms(/plain) = %q, %v", got, err)
	}

	_, err = resolveRooms(ctx, "/rooms/{room:nope}")
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagRoomNotFound || !appErr.HasRoute("resolveRooms->roomHref.RoomNotFound") {
		t.Errorf("err = %+v, want RoomNotFound from roomHref", err)
	}
}
