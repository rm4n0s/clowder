package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/errors"
)

// watchScenarioName is the name of the watch scenario, which -watch runs.
const watchScenarioName = "watch"

// defaultEventWait is how long the watch scenario waits for the event of each
// write (SPEC §7.13: the events come at once; 5 seconds is a generous bound).
const defaultEventWait = 5 * time.Second

// wantMessage is a message the watch scenario waits for after a write: an
// event of the kind on the topic, or an error message about the topic.
type wantMessage struct {
	typ, topic, kind string
}

// String is how a missing message is named in the error.
func (w wantMessage) String() string {
	if w.typ == "error" {
		return "error " + w.topic
	}
	return "event " + w.topic + " " + w.kind
}

// feed is the WebSocket of the watch scenario with the goroutine that reads it.
// The scenario owns the goroutine: close stops it and waits for it.
type feed struct {
	conn   *websocket.Conn
	out    chan map[string]any
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// err is the error that ended the reading, once out is closed.
	err error
	// c prints what the feed sends and receives.
	c *client
}

// connectFeed opens the WebSocket of the client with its bearer token and
// prints the handshake.
func connectFeed(ctx context.Context, c *client) (*feed, error) {
	wsURL := "ws" + strings.TrimPrefix(c.base, "http") + "/api/v1/ws"
	c.requests++
	_, _ = fmt.Fprintln(c.out, "> GET /api/v1/ws")
	_, _ = fmt.Fprintln(c.out, "> Upgrade: websocket")
	_, _ = fmt.Fprintln(c.out, "> Authorization: Bearer <token>")
	dialCtx, cancelDial := context.WithTimeout(ctx, requestTimeout)
	defer cancelDial()
	conn, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.token}},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
			_, _ = fmt.Fprintf(c.out, "< %d %s\n\n", status, http.StatusText(status))
		}
		return nil, errors.NewErr(tagRequestFailed, err).SetMetadata("method", "GET", "path", "/api/v1/ws", "status", status)
	}
	_, _ = fmt.Fprintln(c.out, "< 101 Switching Protocols")
	_, _ = fmt.Fprintln(c.out)
	conn.SetReadLimit(maxAnswerBytes)
	readCtx, cancel := context.WithCancel(ctx)
	f := &feed{conn: conn, out: make(chan map[string]any, 256), cancel: cancel, c: c}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		defer close(f.out)
		for {
			_, raw, err := conn.Read(readCtx)
			if err != nil {
				f.err = err
				return
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				f.err = errors.NewErr(tagAnswerUnexpected, err).SetMetadata("what", "a message of the WebSocket that is not JSON")
				return
			}
			select {
			case f.out <- m:
			case <-readCtx.Done():
				f.err = readCtx.Err()
				return
			}
		}
	}()
	return f, nil
}

// close stops the reader and drops the connection. It returns when the reader
// has ended.
func (f *feed) close() {
	f.cancel()
	_ = f.conn.CloseNow()
	f.wg.Wait()
}

// send writes a message of the client and prints it.
func (f *feed) send(ctx context.Context, msg map[string]any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return errors.NewErr(tagRequestEncodeFailed, err).SetMetadata("path", "/api/v1/ws")
	}
	_, _ = fmt.Fprintf(f.c.out, "> ws %s\n", raw)
	wctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := f.conn.Write(wctx, websocket.MessageText, raw); err != nil {
		return errors.NewErr(tagRequestFailed, err).SetMetadata("method", "WS", "path", "/api/v1/ws")
	}
	return nil
}

// await prints the messages that come until every wanted one has, or the wait
// is over. A message that was not asked for is printed too and does no harm.
// It fails with tagEventMissed, naming what did not come, when time runs out
// or the connection ends first.
func (f *feed) await(ctx context.Context, wait time.Duration, wants ...wantMessage) error {
	missing := append([]wantMessage(nil), wants...)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for len(missing) > 0 {
		select {
		case <-ctx.Done():
			return errors.NewErr(tagEventMissed, ctx.Err()).SetMetadata("missing", describeWants(missing))
		case <-timer.C:
			return errors.New(tagEventMissed, "an event did not come in time",
				"missing", describeWants(missing), "wait", wait.String())
		case m, ok := <-f.out:
			if !ok {
				return errors.New(tagEventMissed, "the WebSocket ended before the event came",
					"missing", describeWants(missing), "ended", f.endText())
			}
			_, _ = fmt.Fprintf(f.c.out, "< ws %s\n", summarizeMessage(m))
			for i, w := range missing {
				if matches(m, w) {
					missing = append(missing[:i], missing[i+1:]...)
					break
				}
			}
		}
	}
	_, _ = fmt.Fprintln(f.c.out)
	return nil
}

// awaitClose prints the messages that come until the connection is closed by
// the server with the code, within the wait.
func (f *feed) awaitClose(wait time.Duration, code websocket.StatusCode) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return errors.New(tagEventMissed, "the connection was not closed in time",
				"missing", fmt.Sprintf("close %d", code), "wait", wait.String())
		case m, ok := <-f.out:
			if !ok {
				if got := websocket.CloseStatus(f.err); got != code {
					return errors.New(tagEventMissed, "the connection was closed with another code",
						"missing", fmt.Sprintf("close %d", code), "ended", f.endText())
				}
				_, _ = fmt.Fprintf(f.c.out, "< ws closed by the server: %s\n\n", f.endText())
				return nil
			}
			_, _ = fmt.Fprintf(f.c.out, "< ws %s\n", summarizeMessage(m))
		}
	}
}

// endText says how the reading ended.
func (f *feed) endText() string {
	var ce websocket.CloseError
	if errors.As(f.err, &ce) {
		return fmt.Sprintf("code %d %q", int(ce.Code), ce.Reason)
	}
	if f.err != nil {
		return f.err.Error()
	}
	return "no error"
}

// matches reports whether the message is the wanted one.
func matches(m map[string]any, w wantMessage) bool {
	if m["type"] != w.typ || m["topic"] != w.topic {
		return false
	}
	return w.typ != "event" || m["kind"] == w.kind
}

// describeWants lists the wanted messages for an error.
func describeWants(ws []wantMessage) string {
	names := make([]string, len(ws))
	for i, w := range ws {
		names[i] = w.String()
	}
	return strings.Join(names, ", ")
}

// summarizeMessage is the line the scenario prints for a message of the
// server: its type, the topic, the kind and the IDs of an event, and a short
// summary of its data.
func summarizeMessage(m map[string]any) string {
	switch m["type"] {
	case "event":
		var b strings.Builder
		fmt.Fprintf(&b, "event %s %s", m["topic"], m["kind"])
		for _, key := range []string{"room_id", "post_id", "root_id", "user_id", "conversation_id", "message_id"} {
			if n, ok := m[key].(float64); ok {
				fmt.Fprintf(&b, " %s=%d", key, uint64(n))
			}
		}
		fmt.Fprintf(&b, "  %s", summarizeData(fmt.Sprint(m["kind"]), m["data"]))
		return b.String()
	case "error":
		e, _ := m["error"].(map[string]any)
		text := fmt.Sprintf("error %v", e["code"])
		if topic, ok := m["topic"].(string); ok {
			text += " topic=" + topic
		}
		if id, ok := m["id"].(float64); ok {
			text += fmt.Sprintf(" id=%d", uint64(id))
		}
		return text
	default:
		raw, err := json.Marshal(m)
		if err != nil {
			return fmt.Sprint(m)
		}
		return string(raw)
	}
}

// summarizeData is a short description of the data of an event.
func summarizeData(kind string, data any) string {
	if data == nil {
		return "(no data)"
	}
	m, _ := data.(map[string]any)
	str := func(v any) string { s, _ := v.(string); return s }
	num := func(v any) uint64 { n, _ := v.(float64); return uint64(n) }
	author := func(v any) string { a, _ := v.(map[string]any); return str(a["username"]) }
	quote := func(v any) string {
		s := strings.Join(strings.Fields(str(v)), " ")
		if r := []rune(s); len(r) > 40 {
			s = string(r[:40]) + "..."
		}
		return fmt.Sprintf("%q", s)
	}
	switch {
	case len(m) == 1 && m["reactions"] != nil:
		reactions, _ := m["reactions"].([]any)
		var parts []string
		for _, item := range reactions {
			r, _ := item.(map[string]any)
			part := fmt.Sprintf("%s x%d", str(r["emoji"]), num(r["count"]))
			if r["mine"] == true {
				part += " (mine)"
			}
			parts = append(parts, part)
		}
		if len(parts) == 0 {
			return "reactions: none"
		}
		return "reactions: " + strings.Join(parts, ", ")
	case m["columns"] != nil:
		columns, _ := m["columns"].([]any)
		var parts []string
		for _, item := range columns {
			col, _ := item.(map[string]any)
			tasks, _ := col["tasks"].([]any)
			parts = append(parts, fmt.Sprintf("%s(%d)", str(col["name"]), len(tasks)))
		}
		return fmt.Sprintf("board of room %d: %s", num(m["room_id"]), strings.Join(parts, " "))
	case m["unread_notifications"] != nil:
		return fmt.Sprintf("unread: %d notifications, %d conversations", num(m["unread_notifications"]), num(m["unread_conversations"]))
	case m["up_to_id"] != nil:
		return fmt.Sprintf("conversation %d read by user %d up to message %d", num(m["conversation_id"]), num(m["user_id"]), num(m["up_to_id"]))
	case m["conversation_id"] != nil && m["author"] != nil:
		return fmt.Sprintf("message %d by %s: %s", num(m["id"]), author(m["author"]), quote(m["body"]))
	case m["kind"] != nil && m["display_name"] != nil:
		return fmt.Sprintf("conversation %d %s %q unread=%v", num(m["id"]), str(m["kind"]), str(m["display_name"]), m["unread"])
	case m["kind"] != nil && m["read"] != nil:
		return fmt.Sprintf("notification %d: %s", num(m["id"]), str(m["kind"]))
	case m["room_id"] != nil && m["root_id"] != nil:
		return fmt.Sprintf("%s %d by %s: %s (%d comments)", postOrComment(m), num(m["id"]), author(m["author"]), quote(m["body"]), num(m["comment_count"]))
	case m["visibility"] != nil:
		return fmt.Sprintf("room %d %q, %d members", num(m["id"]), str(m["name"]), num(m["member_count"]))
	case m["user"] != nil && m["joined_at"] != nil:
		return "member " + author(m["user"])
	}
	return kind
}

// postOrComment says what a post answer is.
func postOrComment(m map[string]any) string {
	if m["parent_id"] != nil {
		return "comment"
	}
	return "post"
}

// watchScenario is the scenario of step 16.5. The runner's user makes a room
// and the direct conversation with the other user, opens the WebSocket and
// subscribes to the room, its board and the conversation. Then the other user
// (on -other-base-url, when it is another process of the same installation)
// writes a post with a task, a comment, a reaction, moves the card and sends a
// message through REST, and after each write the scenario waits for the events
// it must cause and prints each message that arrives. Last the room is
// deleted, which drops the topics of the room, and the user logs out, which
// closes the WebSocket with 4001.
func watchScenario(ctx context.Context, r *runner) error {
	// Only what matters is printed of the REST requests: the events are the
	// point of this scenario.
	r.c.brief = true
	defer func() { r.c.brief = false }()
	a, err := r.login(ctx)
	if err != nil {
		return err
	}
	me := a.number("user", "id")
	if me == 0 {
		return errors.New(tagAnswerUnexpected, "the login answer has no user ID")
	}
	own := fmt.Sprintf("user:%d", me)

	// The other user writes through the base URL that was given for it.
	bob := &client{base: r.otherBase(), http: r.c.http, out: r.c.out, redac: r.c.redac, brief: true}
	defer func() { r.c.requests += bob.requests }()
	if bob.base != r.c.base {
		_, _ = fmt.Fprintf(r.c.out, "# %s writes through %s\n\n", r.otherUsername, bob.base)
	}
	note(r.c, "%s logs in, to write while %s watches", r.otherUsername, r.username)
	if _, err := r.loginOn(ctx, bob, r.otherUsername, r.otherPassword); err != nil {
		return err
	}
	a, err = bob.do(ctx, "GET", "/api/v1/me", nil, 200)
	if err != nil {
		return err
	}
	bobID := a.number("user", "id")
	if bobID == 0 || bobID == me {
		return errors.New(tagAnswerUnexpected, "the other user is the user or has no ID", "id", bobID)
	}

	// The room and the conversation, made before the connection opens.
	note(r.c, "%s makes a room, adds %s to it and opens the conversation with %s", r.username, r.otherUsername, r.otherUsername)
	name := fmt.Sprintf("watch-%d", time.Now().UnixNano())
	a, err = r.c.do(ctx, "POST", "/api/v1/rooms", map[string]any{
		"name": name, "description": "Made by clowder-apicheck -watch", "visibility": "private",
	}, 201)
	if err != nil {
		return err
	}
	roomID := a.number("id")
	if roomID == 0 {
		return errors.New(tagAnswerUnexpected, "the new room has no ID")
	}
	room := fmt.Sprintf("/api/v1/rooms/%d", roomID)
	if _, err = r.c.do(ctx, "POST", room+"/members", map[string]any{"user_id": bobID}, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "POST", "/api/v1/conversations", map[string]any{"usernames": []string{r.otherUsername}}, 201, 200)
	if err != nil {
		return err
	}
	convID := a.number("id")
	if convID == 0 || a.field("kind") != "direct" {
		return errors.New(tagAnswerUnexpected, "the direct conversation is not as asked", "id", convID)
	}
	roomTopic, boardTopic := fmt.Sprintf("room:%d", roomID), fmt.Sprintf("board:%d", roomID)
	convTopic := fmt.Sprintf("conversation:%d", convID)

	// The WebSocket: subscribe first, then the writes.
	note(r.c, "%s opens the WebSocket and subscribes to the room, its board and the conversation", r.username)
	f, err := connectFeed(ctx, r.c)
	if err != nil {
		return err
	}
	defer f.close()
	if err := r.subscribe(ctx, f, 1, roomTopic, boardTopic, convTopic); err != nil {
		return err
	}
	wait := r.eventWait
	if wait <= 0 {
		wait = defaultEventWait
	}

	// A post with a task: the room, and the board.
	note(r.c, "%s writes a post with a task", r.otherUsername)
	a, err = bob.do(ctx, "POST", room+"/posts", map[string]any{
		"body":  "Hello from " + r.otherUsername + ", written while you watch.",
		"tasks": []map[string]any{{"title": "Watch this card"}},
	}, 201)
	if err != nil {
		return err
	}
	postID := a.number("id")
	if postID == 0 {
		return errors.New(tagAnswerUnexpected, "the new post has no ID")
	}
	if err := f.await(ctx, wait,
		wantMessage{"event", roomTopic, "post.created"}, wantMessage{"event", boardTopic, "board.changed"}); err != nil {
		return err
	}

	// The thread of the post, from now on.
	note(r.c, "%s subscribes to the thread of the post too", r.username)
	threadTopic := fmt.Sprintf("post:%d", postID)
	if err := r.subscribe(ctx, f, 2, threadTopic); err != nil {
		return err
	}
	post := fmt.Sprintf("/api/v1/posts/%d", postID)

	// A comment: the thread, and the post on the room (its comment count).
	note(r.c, "%s comments on the post", r.otherUsername)
	if _, err = bob.do(ctx, "POST", post+"/comments", map[string]any{"body": "A comment, in the thread you watch."}, 201); err != nil {
		return err
	}
	if err := f.await(ctx, wait,
		wantMessage{"event", threadTopic, "comment.created"}, wantMessage{"event", roomTopic, "post.updated"}); err != nil {
		return err
	}

	// A reaction.
	note(r.c, "%s reacts to the post", r.otherUsername)
	if _, err = bob.do(ctx, "PUT", post+"/reactions/"+url.PathEscape(thumbsUp), nil, 200); err != nil {
		return err
	}
	if err := f.await(ctx, wait,
		wantMessage{"event", roomTopic, "reaction.changed"}, wantMessage{"event", threadTopic, "reaction.changed"}); err != nil {
		return err
	}

	// The card moves to the next column.
	note(r.c, "%s moves the card to the next column", r.otherUsername)
	a, err = bob.do(ctx, "GET", room+"/board", nil, 200)
	if err != nil {
		return err
	}
	task, target := taskAndNextColumn(a)
	if task == 0 || target == 0 {
		return errors.New(tagAnswerUnexpected, "the board has no card or no second column", "task", task, "column", target)
	}
	if _, err = bob.do(ctx, "POST", fmt.Sprintf("/api/v1/tasks/%d/move", task), map[string]any{"column_id": target, "position": 0}, 204); err != nil {
		return err
	}
	// The card is part of the post: the post is announced as changed too.
	if err := f.await(ctx, wait,
		wantMessage{"event", boardTopic, "board.changed"},
		wantMessage{"event", roomTopic, "post.updated"}, wantMessage{"event", threadTopic, "post.updated"}); err != nil {
		return err
	}

	// A message: the conversation, and the list of conversations of the user.
	note(r.c, "%s sends a message to %s", r.otherUsername, r.username)
	if _, err = bob.do(ctx, "POST", fmt.Sprintf("/api/v1/conversations/%d/messages", convID),
		map[string]any{"body": "Hello " + r.username + ", this message is for you."}, 201); err != nil {
		return err
	}
	if err := f.await(ctx, wait,
		wantMessage{"event", convTopic, "message.created"}, wantMessage{"event", own, "conversation.changed"}); err != nil {
		return err
	}

	// The room goes: it is announced, and the topics of the room are dropped.
	note(r.c, "%s deletes the room", r.username)
	if _, err = r.c.do(ctx, "DELETE", room, nil, 204); err != nil {
		return err
	}
	if err := f.await(ctx, wait,
		wantMessage{"event", roomTopic, "room.deleted"}, wantMessage{"event", own, "room.deleted"},
		wantMessage{"error", roomTopic, ""}, wantMessage{"error", boardTopic, ""}, wantMessage{"error", threadTopic, ""}); err != nil {
		return err
	}

	// A logout ends the connection of the session with 4001.
	note(r.c, "%s logs out, which ends the session of the WebSocket", r.username)
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	r.c.token = ""
	if err := f.awaitClose(wait, 4001); err != nil {
		return err
	}
	// The other user's session goes too.
	_, err = bob.do(ctx, "POST", "/api/v1/auth/logout", nil, 204)
	return err
}

// note prints a line that says what the next requests are for.
func note(c *client, format string, args ...any) {
	_, _ = fmt.Fprintf(c.out, "# "+format+"\n\n", args...)
}

// subscribe sends a subscribe message and waits for its ack.
func (r *runner) subscribe(ctx context.Context, f *feed, id int, topics ...string) error {
	if err := f.send(ctx, map[string]any{"type": "subscribe", "id": id, "topics": topics}); err != nil {
		return err
	}
	timer := time.NewTimer(requestTimeout)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return errors.New(tagAnswerUnexpected, "no answer to subscribe", "id", id)
		case m, ok := <-f.out:
			if !ok {
				return errors.New(tagAnswerUnexpected, "the WebSocket ended before the ack", "ended", f.endText())
			}
			_, _ = fmt.Fprintf(r.c.out, "< ws %s\n", summarizeMessage(m))
			if m["id"] != float64(id) {
				continue
			}
			_, _ = fmt.Fprintln(r.c.out)
			if m["type"] != "ack" {
				return errors.New(tagAnswerUnexpected, "subscribe was not acked", "id", id, "got", summarizeMessage(m))
			}
			return nil
		}
	}
}

// taskAndNextColumn returns the ID of the first card of the board answer and
// the ID of the column after the one it is in, 0 for none.
func taskAndNextColumn(a answer) (task, next uint64) {
	columns := a.items("columns")
	for i, item := range columns {
		col, _ := item.(map[string]any)
		tasks, _ := col["tasks"].([]any)
		if len(tasks) == 0 || i+1 >= len(columns) {
			continue
		}
		t, _ := tasks[0].(map[string]any)
		n, _ := t["id"].(float64)
		after, _ := columns[i+1].(map[string]any)
		id, _ := after["id"].(float64)
		return uint64(n), uint64(id)
	}
	return 0, 0
}

const tagEventMissed = "EventMissed"
