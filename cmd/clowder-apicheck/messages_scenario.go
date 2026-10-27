package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rm4n0s/errors"
)

// messageNote is the content of the text file the messages scenario sends.
const messageNote = "Message check: a small text file made by clowder-apicheck.\n"

// conversationFinds returns the conversation with the ID in the answer of
// GET /conversations, nil when it is not there.
func conversationFinds(a answer, id uint64) map[string]any {
	for _, item := range a.items("conversations") {
		c, _ := item.(map[string]any)
		if n, _ := c["id"].(float64); uint64(n) == id {
			return c
		}
	}
	return nil
}

// messagesScenario is the scenario of step 16.4 for the messages. As the
// runner's user it starts the direct conversation with the other user (or
// opens the one there is), uploads a text file and sends it in a message,
// downloads it, and reads the list. Then, as the other user, it reads the
// unread conversation, replies to the message, downloads the file and reads
// the conversation. Back as the first user it sees the reply, reacts to it,
// edits and deletes its own messages, reads the history and marks the
// conversation read, which the other user's "Seen" shows. Last it makes a
// group with the other user and the third one, renames it, writes in it and
// leaves it.
func messagesScenario(ctx context.Context, r *runner) error {
	if _, err := r.login(ctx); err != nil {
		return err
	}
	me := r.username

	// The direct conversation: new (201) or the one that was there (200).
	a, err := r.c.do(ctx, "POST", "/api/v1/conversations", map[string]any{"usernames": []string{r.otherUsername}}, 201, 200)
	if err != nil {
		return err
	}
	conv := a.number("id")
	if conv == 0 || a.field("kind") != "direct" || a.field("can_send") != true || len(a.items("members")) != 2 {
		return errors.New(tagAnswerUnexpected, "the direct conversation is not as asked", "id", conv)
	}
	convPath := fmt.Sprintf("/api/v1/conversations/%d", conv)

	// A message with a file.
	a, err = r.c.upload(ctx, "/api/v1/uploads", "note.txt", []byte(messageNote), 201)
	if err != nil {
		return err
	}
	upload, _ := a.field("id").(string)
	if upload == "" {
		return errors.New(tagAnswerUnexpected, "the upload has no ID")
	}
	a, err = r.c.do(ctx, "POST", convPath+"/messages", map[string]any{
		"body": "Hello <" + r.otherUsername + ">, a note is attached.", "upload_ids": []string{upload},
	}, 201)
	if err != nil {
		return err
	}
	first := a.number("id")
	attachments := a.items("attachments")
	if first == 0 || len(attachments) != 1 || a.field("author", "username") != me || a.field("can_edit") != true ||
		!strings.Contains(fmt.Sprint(a.field("html")), "&lt;"+r.otherUsername+"&gt;") {
		return errors.New(tagAnswerUnexpected, "the new message is not as sent", "id", first)
	}
	att, _ := attachments[0].(map[string]any)
	fileURL, _ := att["url"].(string)
	if !strings.HasPrefix(fileURL, "/api/v1/messages/attachments/") {
		return errors.New(tagAnswerUnexpected, "the file of the message is not under /messages/attachments", "url", fileURL)
	}
	a, err = r.c.download(ctx, fileURL, 200)
	if err != nil {
		return err
	}
	if len(a.body) != len(messageNote) || a.header.Get("Content-Disposition") != `attachment; filename="note.txt"` {
		return errors.New(tagAnswerUnexpected, "the download is not as uploaded", "bytes", len(a.body))
	}
	a, err = r.c.do(ctx, "GET", "/api/v1/conversations", nil, 200)
	if err != nil {
		return err
	}
	c := conversationFinds(a, conv)
	if c == nil || a.field("conversations") == nil {
		return errors.New(tagAnswerUnexpected, "the conversation is not in the list", "id", conv)
	}
	if last, _ := c["last_message"].(map[string]any); last == nil || last["id"] != float64(first) {
		return errors.New(tagAnswerUnexpected, "the list does not show the message as the last", "id", first)
	}

	// The other user: unread, reads the file, replies, marks it read.
	alice := r.c.token
	r.c.token = ""
	if _, err = r.loginAs(ctx, r.otherUsername, r.otherPassword); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", "/api/v1/conversations", nil, 200)
	if err != nil {
		return err
	}
	if c = conversationFinds(a, conv); c == nil || c["unread"] != true {
		return errors.New(tagAnswerUnexpected, "the other user does not see the conversation as unread", "id", conv)
	}
	a, err = r.c.do(ctx, "GET", convPath+"/messages?limit=20", nil, 200)
	if err != nil {
		return err
	}
	if len(a.items("messages")) == 0 {
		return errors.New(tagAnswerUnexpected, "the other user does not read the history")
	}
	if _, err = r.c.download(ctx, fileURL, 200); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "POST", convPath+"/messages", map[string]any{
		"body": "Thanks, got the file.", "reply_to_id": first,
	}, 201)
	if err != nil {
		return err
	}
	reply := a.number("id")
	if reply == 0 || a.number("reply_to", "id") != first || a.field("author", "username") != r.otherUsername || a.field("can_edit") != true {
		return errors.New(tagAnswerUnexpected, "the reply does not quote the message", "id", reply)
	}
	// The sending marked the conversation read for the other user already;
	// reading says so explicitly.
	if _, err = r.c.do(ctx, "POST", convPath+"/read", map[string]any{"up_to_id": reply}, 204); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}

	// Back as the first user: the reply is unread.
	r.c.token = alice
	a, err = r.c.do(ctx, "GET", "/api/v1/me", nil, 200)
	if err != nil {
		return err
	}
	if a.number("unread_conversations") == 0 {
		return errors.New(tagAnswerUnexpected, "the reply is not counted as unread")
	}
	react := fmt.Sprintf("/api/v1/messages/%d/reactions/%s", reply, url.PathEscape(thumbsUp))
	a, err = r.c.do(ctx, "PUT", react, nil, 200)
	if err != nil {
		return err
	}
	if rs := a.items("reactions"); len(rs) != 1 {
		return errors.New(tagAnswerUnexpected, "the reaction is not on the message")
	}
	if a, err = r.c.do(ctx, "PUT", react, nil, 200); err != nil {
		return err
	}
	if rs := a.items("reactions"); len(rs) != 1 {
		return errors.New(tagAnswerUnexpected, "putting the reaction again changed it")
	}
	a, err = r.c.do(ctx, "PATCH", fmt.Sprintf("/api/v1/messages/%d", first), map[string]any{
		"body": "Hello again, the note is attached.",
	}, 200)
	if err != nil {
		return err
	}
	if a.field("edited_at") == nil || a.field("body") != "Hello again, the note is attached." || len(a.items("attachments")) != 1 {
		return errors.New(tagAnswerUnexpected, "the edit is not shown", "id", first)
	}
	// Someone else's message is not changed: 403.
	if _, err = r.c.do(ctx, "PATCH", fmt.Sprintf("/api/v1/messages/%d", reply), map[string]any{"body": "mine now"}, 403); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "POST", convPath+"/messages", map[string]any{"body": "This one is going away."}, 201)
	if err != nil {
		return err
	}
	gone := a.number("id")
	if _, err = r.c.do(ctx, "DELETE", fmt.Sprintf("/api/v1/messages/%d", gone), nil, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", convPath+"/messages", nil, 200)
	if err != nil {
		return err
	}
	var shown []uint64
	var deleted bool
	for _, item := range a.items("messages") {
		m, _ := item.(map[string]any)
		id, _ := m["id"].(float64)
		shown = append(shown, uint64(id))
		if uint64(id) == gone {
			deleted = m["deleted"] == true && m["body"] == nil && m["html"] == nil
		}
	}
	if !deleted || len(shown) < 3 || shown[len(shown)-1] != gone {
		return errors.New(tagAnswerUnexpected, "the history does not show the deleted message in its place", "ids", fmt.Sprint(shown))
	}
	// Read up to the newest; the other user's reply is read, and "Seen" is
	// what the other side has read of the messages written here.
	if _, err = r.c.do(ctx, "POST", convPath+"/read", map[string]any{"up_to_id": gone}, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", convPath, nil, 200)
	if err != nil {
		return err
	}
	if a.field("unread") != false || a.number("seen_up_to_id") < reply {
		return errors.New(tagAnswerUnexpected, "the conversation is still unread or not seen", "unread", a.field("unread"))
	}

	// A group: made, renamed, written in, left.
	name := fmt.Sprintf("apicheck-group-%d", time.Now().UnixNano())
	a, err = r.c.do(ctx, "POST", "/api/v1/conversations", map[string]any{
		"usernames": []string{r.otherUsername, r.thirdUsername}, "name": name,
	}, 201)
	if err != nil {
		return err
	}
	group := a.number("id")
	if group == 0 || a.field("kind") != "group" || a.field("name") != name || len(a.items("members")) != 3 {
		return errors.New(tagAnswerUnexpected, "the group is not as asked", "id", group)
	}
	groupPath := fmt.Sprintf("/api/v1/conversations/%d", group)
	a, err = r.c.do(ctx, "PATCH", groupPath, map[string]any{"name": name + "-renamed"}, 200)
	if err != nil {
		return err
	}
	if a.field("name") != name+"-renamed" {
		return errors.New(tagAnswerUnexpected, "the group was not renamed")
	}
	if _, err = r.c.do(ctx, "POST", groupPath+"/messages", map[string]any{"body": "Welcome to the group."}, 201); err != nil {
		return err
	}
	// Nobody leaves a direct conversation.
	if _, err = r.c.do(ctx, "POST", convPath+"/leave", nil, 422); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", groupPath+"/leave", nil, 204); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "GET", groupPath, nil, 404); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}
