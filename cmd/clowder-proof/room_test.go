//go:build clowder_browser

package main

import (
	"context"
	"regexp"
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
	// {board:name} is the path of the board of the room.
	board, err := resolveRooms(ctx, "{board:secret}")
	if err != nil {
		t.Fatalf("resolveRooms board: %+v", err)
	}
	if want := strings.TrimSuffix(got, "?x=1") + "/board"; board != want {
		t.Errorf("board page = %q, want %q", board, want)
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

// TestResolvePosts finds the demo post "Quick question" in the feeds of alice's
// rooms, and fails for a text that no post has.
func TestResolvePosts(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, time.Minute)
	defer cancel()
	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}

	got, err := resolvePosts(ctx, srv.URL, "{post:Quick question}?x=1")
	if err != nil {
		t.Fatalf("resolvePosts: %+v", err)
	}
	if !regexp.MustCompile(`^/posts/\d+\?x=1$`).MatchString(got) {
		t.Errorf("page = %q, want /posts/<id>?x=1 with no fragment", got)
	}
	if got, err := resolvePosts(ctx, srv.URL, "/plain"); err != nil || got != "/plain" {
		t.Errorf("resolvePosts(/plain) = %q, %v", got, err)
	}

	_, err = resolvePosts(ctx, srv.URL, "/posts/{post:no post says this}")
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagPostNotFound || !appErr.HasRoute("resolvePosts->postHref.PostNotFound") {
		t.Errorf("err = %+v, want PostNotFound from postHref", err)
	}
}

// TestResolveConversations finds the demo conversation of alice and bob, and
// the group, in bob's list of conversations, in any order of the usernames, and
// fails for members that no conversation has.
func TestResolveConversations(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, time.Minute)
	defer cancel()
	if err := browsertest.LogIn(ctx, srv, "bob"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}

	direct, err := resolveConversations(ctx, srv.URL, "{conversation:alice,bob}?x=1")
	if err != nil {
		t.Fatalf("resolveConversations: %+v", err)
	}
	if !regexp.MustCompile(`^/messages/\d+\?x=1$`).MatchString(direct) {
		t.Errorf("page = %q, want /messages/<id>?x=1", direct)
	}
	again, err := resolveConversations(ctx, srv.URL, "{conversation: Bob , alice}?x=1")
	if err != nil || again != direct {
		t.Errorf("same members in another order = %q, %v; want %q", again, err, direct)
	}
	group, err := resolveConversations(ctx, srv.URL, "{conversation:carol,bob,alice}")
	if err != nil || group == direct || !strings.HasPrefix(group, "/messages/") {
		t.Errorf("group = %q, %v", group, err)
	}
	if got, err := resolveConversations(ctx, srv.URL, "/plain"); err != nil || got != "/plain" {
		t.Errorf("resolveConversations(/plain) = %q, %v", got, err)
	}

	_, err = resolveConversations(ctx, srv.URL, "{conversation:bob,carol}")
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagConversationNotFound || !appErr.HasRoute("resolveConversations->conversationHref.ConversationNotFound") {
		t.Errorf("err = %+v, want ConversationNotFound from conversationHref", err)
	}
}
