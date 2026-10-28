package main

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rm4n0s/errors"
)

// runner is what a scenario works with: the client and who it logs in as.
type runner struct {
	c          *client
	username   string
	password   string
	clientName string
	// otherUsername and otherPassword are the second user of the board
	// scenario.
	otherUsername string
	otherPassword string
	// thirdUsername is the third member of the group of the messages
	// scenario, who only has to exist.
	thirdUsername string
	// adminUsername and adminPassword are the admin of the admin scenario.
	adminUsername string
	adminPassword string
	// otherBaseURL is the address the other user writes through in the watch
	// scenario; empty means the address of the client.
	otherBaseURL string
	// eventWait is how long the watch scenario waits for the event of a write;
	// 0 means defaultEventWait.
	eventWait time.Duration
}

// otherBase returns the address the other user writes through.
func (r *runner) otherBase() string {
	if r.otherBaseURL != "" {
		return r.otherBaseURL
	}
	return r.c.base
}

// scenario is a named list of steps against the API. A later step of the plan
// adds its own to scenarios.
type scenario struct {
	name    string
	summary string
	run     func(ctx context.Context, r *runner) error
}

// scenarios lists the scenarios in the order they run.
func scenarios() []scenario {
	return []scenario{
		{name: "session", summary: "log in, read who I am and my sessions, find users, log out", run: sessionScenario},
		{name: "rooms", summary: "list the rooms, leave and join one, make a room with a member, post, comment, react, edit, read the thread, delete", run: roomsScenario},
		{name: "board", summary: "upload files, post them with a task, work the board, download, search, and read the notifications as the assignee", run: boardScenario},
		{name: "messages", summary: "start a conversation, send a file, reply as the other user, react, edit, delete, read, make a group, rename it and leave", run: messagesScenario},
		{name: settingsScenarioName, summary: "change the display name and the theme, upload an avatar and fetch it, change the password and see the other login end, put everything back", run: settingsScenario},
		{name: prefsScenarioName, summary: "read the notification choices, set a group conversation to mentions, mute a room, read both back and put them back", run: prefsScenario},
		{name: "admin", summary: "as an admin: list the users, make an invitation, list the rooms and read the audit log; as a member: refused", run: adminScenario},
		{name: watchScenarioName, summary: "open the WebSocket, subscribe to a room, its board and a conversation, and see the events of the other user's writes", run: watchScenario},
	}
}

// login logs in as the runner's user and keeps the token for the next
// requests. The token and the password are hidden in everything printed.
func (r *runner) login(ctx context.Context) (answer, error) {
	return r.loginAs(ctx, r.username, r.password)
}

// loginAs logs in as the user and keeps the token for the next requests.
func (r *runner) loginAs(ctx context.Context, username, password string) (answer, error) {
	return r.loginOn(ctx, r.c, username, password)
}

// loginOn logs in on the client, which may be another client than the
// runner's, and keeps the token in it.
func (r *runner) loginOn(ctx context.Context, c *client, username, password string) (answer, error) {
	c.redac.add(password)
	a, err := c.do(ctx, "POST", "/api/v1/auth/login", map[string]string{
		"username": username, "password": password, "client_name": r.clientName,
	}, 200)
	if err != nil {
		return a, err
	}
	token, _ := a.field("token").(string)
	if token == "" {
		return a, errors.New(tagAnswerUnexpected, "the login answer has no token")
	}
	c.token = token
	c.redac.add(token)
	return a, nil
}

// sessionScenario is the scenario of step 16.1: login, /me, the sessions, the
// users, logout, and a check that the token is dead.
func sessionScenario(ctx context.Context, r *runner) error {
	a, err := r.login(ctx)
	if err != nil {
		return err
	}
	if got := a.field("user", "username"); got != r.username {
		return errors.New(tagAnswerUnexpected, "the login answer names another user", "got", got)
	}

	a, err = r.c.do(ctx, "GET", "/api/v1/me", nil, 200)
	if err != nil {
		return err
	}
	if got := a.field("user", "username"); got != r.username {
		return errors.New(tagAnswerUnexpected, "/me names another user", "got", got)
	}

	a, err = r.c.do(ctx, "GET", "/api/v1/me/sessions", nil, 200)
	if err != nil {
		return err
	}
	if !hasCurrentSession(a, r.clientName) {
		return errors.New(tagAnswerUnexpected, "the list of sessions has no current api session of this client",
			"client", r.clientName)
	}

	prefix := r.username
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	a, err = r.c.do(ctx, "GET", "/api/v1/users?q="+prefix, nil, 200)
	if err != nil {
		return err
	}
	if !hasUser(a, r.username) {
		return errors.New(tagAnswerUnexpected, "the users do not include the user", "q", prefix)
	}

	if _, err := r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	// The token is dead now.
	if _, err := r.c.do(ctx, "GET", "/api/v1/me", nil, 401); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}

// number returns the number at the path of keys in the decoded JSON of the
// answer, 0 when there is none.
func (a answer) number(keys ...string) uint64 {
	n, _ := a.field(keys...).(float64)
	return uint64(n)
}

// items returns the array at the path of keys in the decoded JSON of the
// answer, nil when there is none.
func (a answer) items(keys ...string) []any {
	list, _ := a.field(keys...).([]any)
	return list
}

// thumbsUp is the emoji the scenario reacts with.
const thumbsUp = "\U0001F44D"

// roomsScenario is the scenario of step 16.2: it logs in, lists the rooms,
// leaves and joins a visible room (or joins and leaves it, if the user is not
// in it), makes a private
// room, adds a user, writes a markdown post with a tag and a task, reads the
// feed, comments, replies to the comment, reacts and takes the reaction off,
// edits the post, reads the thread, deletes the comment, reads the tags and
// deletes the room, then logs out.
func roomsScenario(ctx context.Context, r *runner) error {
	a, err := r.login(ctx)
	if err != nil {
		return err
	}
	me := a.number("user", "id")

	a, err = r.c.do(ctx, "GET", "/api/v1/rooms", nil, 200)
	if err != nil {
		return err
	}
	// The first visible room: leave it and join it again if the user is in it,
	// join it and leave it again if not, so that the user's rooms are as
	// they were.
	var visibleID uint64
	var inIt bool
	for _, item := range a.items("rooms") {
		room, _ := item.(map[string]any)
		if room["visibility"] == "visible" {
			id, _ := room["id"].(float64)
			visibleID, inIt = uint64(id), room["is_member"] == true
			break
		}
	}
	if visibleID != 0 {
		room := fmt.Sprintf("/api/v1/rooms/%d", visibleID)
		if _, err = r.c.do(ctx, "GET", room, nil, 200); err != nil {
			return err
		}
		steps := []string{"/join", "/leave"}
		if inIt {
			steps = []string{"/leave", "/join"}
		}
		for _, step := range steps {
			if _, err = r.c.do(ctx, "POST", room+step, nil, 204); err != nil {
				return err
			}
		}
	}

	name := fmt.Sprintf("apicheck-%d", time.Now().UnixNano())
	a, err = r.c.do(ctx, "POST", "/api/v1/rooms", map[string]any{
		"name": name, "description": "Made by clowder-apicheck", "visibility": "private",
	}, 201)
	if err != nil {
		return err
	}
	roomID := a.number("id")
	if roomID == 0 || a.field("name") != name || a.field("is_member") != true || a.number("created_by", "id") != me {
		return errors.New(tagAnswerUnexpected, "the new room is not as made", "room", a.field("name"))
	}
	room := fmt.Sprintf("/api/v1/rooms/%d", roomID)

	a, err = r.c.do(ctx, "GET", room+"/candidates", nil, 200)
	if err != nil {
		return err
	}
	var friend uint64
	for _, item := range a.items("users") {
		u, _ := item.(map[string]any)
		if id, _ := u["id"].(float64); uint64(id) != me && id > 0 {
			friend = uint64(id)
			break
		}
	}
	if friend == 0 {
		return errors.New(tagAnswerUnexpected, "there is nobody to add to the room")
	}
	if _, err = r.c.do(ctx, "POST", room+"/members", map[string]any{"user_id": friend}, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", room+"/members", nil, 200)
	if err != nil {
		return err
	}
	if len(a.items("members")) != 2 {
		return errors.New(tagAnswerUnexpected, "the room does not have two members", "members", len(a.items("members")))
	}

	a, err = r.c.do(ctx, "POST", room+"/posts", map[string]any{
		"format": "markdown",
		"body":   "# Plan\n\nThe **first** post of the apicheck room.",
		"tags":   []string{"apicheck", "plan"},
		"tasks":  []map[string]any{{"title": "Read the plan", "assignee_ids": []uint64{friend}, "due_date": "2030-01-31"}},
	}, 201)
	if err != nil {
		return err
	}
	postID := a.number("id")
	if postID == 0 || !strings.Contains(fmt.Sprint(a.field("html")), "<strong>first</strong>") ||
		len(a.items("tags")) != 2 || len(a.items("tasks")) != 1 || a.field("author", "username") != r.username {
		return errors.New(tagAnswerUnexpected, "the new post is not as written", "id", postID)
	}
	post := fmt.Sprintf("/api/v1/posts/%d", postID)

	a, err = r.c.do(ctx, "GET", room+"/posts?limit=10", nil, 200)
	if err != nil {
		return err
	}
	if feed := a.items("posts"); len(feed) != 1 || a.field("next_before") != nil {
		return errors.New(tagAnswerUnexpected, "the feed is not the one post", "posts", len(feed))
	}

	a, err = r.c.do(ctx, "POST", post+"/comments", map[string]any{"body": "A comment on the plan."}, 201)
	if err != nil {
		return err
	}
	commentID := a.number("id")
	if commentID == 0 || a.number("parent_id") != postID {
		return errors.New(tagAnswerUnexpected, "the comment does not answer the post", "id", commentID)
	}
	comment := fmt.Sprintf("/api/v1/posts/%d", commentID)
	a, err = r.c.do(ctx, "POST", comment+"/comments", map[string]any{"format": "markdown", "body": "A *reply* to the comment."}, 201)
	if err != nil {
		return err
	}
	if a.number("parent_id") != commentID || a.number("root_id") != postID {
		return errors.New(tagAnswerUnexpected, "the reply does not answer the comment")
	}

	react := post + "/reactions/" + url.PathEscape(thumbsUp)
	a, err = r.c.do(ctx, "PUT", react, nil, 200)
	if err != nil {
		return err
	}
	if len(a.items("reactions")) != 1 {
		return errors.New(tagAnswerUnexpected, "the reaction is not on the post")
	}
	if a, err = r.c.do(ctx, "DELETE", react, nil, 200); err != nil {
		return err
	}
	if len(a.items("reactions")) != 0 {
		return errors.New(tagAnswerUnexpected, "the reaction is still on the post")
	}

	a, err = r.c.do(ctx, "PATCH", post, map[string]any{"body": "# Plan\n\nThe **edited** post of the apicheck room."}, 200)
	if err != nil {
		return err
	}
	if a.field("edited_at") == nil || !strings.Contains(fmt.Sprint(a.field("html")), "<strong>edited</strong>") ||
		len(a.items("tags")) != 2 || len(a.items("tasks")) != 1 {
		return errors.New(tagAnswerUnexpected, "the edit did not keep the tags and the task or did not show", "id", postID)
	}

	a, err = r.c.do(ctx, "GET", post+"/thread", nil, 200)
	if err != nil {
		return err
	}
	thread := a.items("comments")
	if len(thread) != 2 {
		return errors.New(tagAnswerUnexpected, "the thread does not have two comments", "comments", len(thread))
	}
	var depths []any
	for _, item := range thread {
		node, _ := item.(map[string]any)
		depths = append(depths, node["depth"])
	}
	if got := fmt.Sprint(depths...); got != "1 2" {
		return errors.New(tagAnswerUnexpected, "the depths of the thread are not 1 and 2", "depths", got)
	}

	if _, err = r.c.do(ctx, "DELETE", comment, nil, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", comment, nil, 200)
	if err != nil {
		return err
	}
	if a.field("deleted") != true || a.field("body") != nil {
		return errors.New(tagAnswerUnexpected, "the comment with a reply is not kept as deleted")
	}

	a, err = r.c.do(ctx, "GET", "/api/v1/tags?prefix=apic", nil, 200)
	if err != nil {
		return err
	}
	if !slices.Contains(a.items("tags"), any("apicheck")) {
		return errors.New(tagAnswerUnexpected, "the tag suggestions do not include apicheck", "tags", fmt.Sprint(a.items("tags")))
	}

	// The room goes, as a creator may, so that a run leaves nothing behind.
	if _, err = r.c.do(ctx, "DELETE", room, nil, 204); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "GET", room, nil, 404); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}

// hasCurrentSession reports whether the answer of GET /me/sessions has an api
// session of the client that is the current one.
func hasCurrentSession(a answer, clientName string) bool {
	list, _ := a.field("sessions").([]any)
	for _, item := range list {
		s, _ := item.(map[string]any)
		if s["kind"] == "api" && s["client_name"] == clientName && s["current"] == true {
			return true
		}
	}
	return false
}

// hasUser reports whether the answer of GET /users lists the username.
func hasUser(a answer, username string) bool {
	list, _ := a.field("users").([]any)
	for _, item := range list {
		u, _ := item.(map[string]any)
		if name, _ := u["username"].(string); strings.EqualFold(name, username) {
			return true
		}
	}
	return false
}

const tagAnswerUnexpected = "AnswerUnexpected"
