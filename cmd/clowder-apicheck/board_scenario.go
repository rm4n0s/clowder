package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strings"
	"time"

	"github.com/rm4n0s/errors"
)

// boardNote is the content of the text file the board scenario uploads.
const boardNote = "Board check: a small text file made by clowder-apicheck.\n"

// boardWord is the word the post of the board scenario has and the search
// looks for.
const boardWord = "boardcheck"

// tinyPNG makes the PNG the board scenario uploads: 4 by 4 pixels, one color.
func tinyPNG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 0x33, G: 0x99, B: 0xcc, A: 0xff})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, errors.NewErr(tagRequestEncodeFailed, err)
	}
	return b.Bytes(), nil
}

// columnNames returns the names of the columns of a board answer, left to
// right, and the titles of the cards of each.
func columnNames(a answer) (names []string, cards [][]string) {
	for _, item := range a.items("columns") {
		col, _ := item.(map[string]any)
		name, _ := col["name"].(string)
		names = append(names, name)
		titles := []string{}
		tasks, _ := col["tasks"].([]any)
		for _, t := range tasks {
			task, _ := t.(map[string]any)
			title, _ := task["title"].(string)
			titles = append(titles, title)
		}
		cards = append(cards, titles)
	}
	return names, cards
}

// columnID returns the ID of the i-th column of a board answer, 0 if none.
func columnID(a answer, i int) uint64 {
	cols := a.items("columns")
	if i >= len(cols) {
		return 0
	}
	col, _ := cols[i].(map[string]any)
	id, _ := col["id"].(float64)
	return uint64(id)
}

// boardScenario is the scenario of step 16.3. As the runner's user it makes a
// private room with the other user, uploads a text file and a PNG, writes a
// post with both and a task assigned to the other user, reads the board,
// moves the card to "In progress", sets its due date, adds a column, renames
// it, moves it and deletes it, downloads the two attachments, and searches for
// the post by a word and its tag. Then it logs in as the other user, reads the
// notifications (the assignment is there) and marks them read, and the
// room goes.
func boardScenario(ctx context.Context, r *runner) error {
	if _, err := r.login(ctx); err != nil {
		return err
	}
	a, err := r.c.do(ctx, "GET", "/api/v1/users?q="+url.QueryEscape(r.otherUsername), nil, 200)
	if err != nil {
		return err
	}
	var other uint64
	for _, item := range a.items("users") {
		u, _ := item.(map[string]any)
		if name, _ := u["username"].(string); strings.EqualFold(name, r.otherUsername) {
			id, _ := u["id"].(float64)
			other = uint64(id)
		}
	}
	if other == 0 {
		return errors.New(tagAnswerUnexpected, "the other user is not among the users", "username", r.otherUsername)
	}

	name := fmt.Sprintf("apicheck-board-%d", time.Now().UnixNano())
	a, err = r.c.do(ctx, "POST", "/api/v1/rooms", map[string]any{
		"name": name, "description": "Made by clowder-apicheck", "visibility": "private",
	}, 201)
	if err != nil {
		return err
	}
	roomID := a.number("id")
	if roomID == 0 {
		return errors.New(tagAnswerUnexpected, "the new room has no ID")
	}
	room := fmt.Sprintf("/api/v1/rooms/%d", roomID)
	if _, err = r.c.do(ctx, "POST", room+"/members", map[string]any{"user_id": other}, 204); err != nil {
		return err
	}

	// The files.
	a, err = r.c.upload(ctx, "/api/v1/uploads", "notes.txt", []byte(boardNote), 201)
	if err != nil {
		return err
	}
	textUpload, _ := a.field("id").(string)
	if textUpload == "" || a.number("size") != uint64(len(boardNote)) || a.field("width") != nil {
		return errors.New(tagAnswerUnexpected, "the upload of the text file is not as sent")
	}
	pixel, err := tinyPNG()
	if err != nil {
		return err
	}
	a, err = r.c.upload(ctx, "/api/v1/uploads", "pixel.png", pixel, 201)
	if err != nil {
		return err
	}
	pngUpload, _ := a.field("id").(string)
	if pngUpload == "" || a.field("content_type") != "image/png" || a.number("width") != 4 || a.number("height") != 4 {
		return errors.New(tagAnswerUnexpected, "the upload of the image is not as sent")
	}

	// The post with both files and a task for the other user.
	a, err = r.c.do(ctx, "POST", room+"/posts", map[string]any{
		"format":     "markdown",
		"body":       "# " + boardWord + "\n\nThe **first** card of the board, with a note and a picture.",
		"tags":       []string{"apicheck", "board"},
		"tasks":      []map[string]any{{"title": "Move this card", "assignee_ids": []uint64{other}}},
		"upload_ids": []string{textUpload, pngUpload},
	}, 201)
	if err != nil {
		return err
	}
	postID := a.number("id")
	attachments, tasks := a.items("attachments"), a.items("tasks")
	if postID == 0 || len(attachments) != 2 || len(tasks) != 1 {
		return errors.New(tagAnswerUnexpected, "the new post does not have two files and a task", "id", postID)
	}
	first, _ := tasks[0].(map[string]any)
	taskNumber, _ := first["id"].(float64)
	taskID := uint64(taskNumber)
	var attachmentURLs []string
	for _, item := range attachments {
		att, _ := item.(map[string]any)
		u, _ := att["url"].(string)
		attachmentURLs = append(attachmentURLs, u)
	}

	// The board: the card is in the first column; move it to "In progress".
	a, err = r.c.do(ctx, "GET", room+"/board", nil, 200)
	if err != nil {
		return err
	}
	names, cards := columnNames(a)
	if len(names) != 3 || names[1] != "In progress" || len(cards[0]) != 1 || cards[0][0] != "Move this card" {
		return errors.New(tagAnswerUnexpected, "the board does not show the card in the first column",
			"columns", fmt.Sprint(names), "cards", fmt.Sprint(cards))
	}
	inProgress := columnID(a, 1)
	task := fmt.Sprintf("/api/v1/tasks/%d", taskID)
	if _, err = r.c.do(ctx, "POST", task+"/move", map[string]any{"column_id": inProgress, "position": 0}, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", room+"/board", nil, 200)
	if err != nil {
		return err
	}
	if _, cards = columnNames(a); len(cards[0]) != 0 || len(cards[1]) != 1 {
		return errors.New(tagAnswerUnexpected, "the card did not move to In progress", "cards", fmt.Sprint(cards))
	}
	a, err = r.c.do(ctx, "PATCH", task, map[string]any{"due_date": "2030-02-15"}, 200)
	if err != nil {
		return err
	}
	if a.field("due_date") != "2030-02-15" || len(a.items("assignees")) != 1 || a.number("column_id") != inProgress {
		return errors.New(tagAnswerUnexpected, "the card did not get the due date or lost its assignee")
	}

	// A column: add, rename, move, delete.
	a, err = r.c.do(ctx, "POST", room+"/columns", map[string]any{"name": "Review"}, 201)
	if err != nil {
		return err
	}
	columnPath := fmt.Sprintf("/api/v1/columns/%d", a.number("id"))
	if a.field("name") != "Review" || a.number("position") != 3 {
		return errors.New(tagAnswerUnexpected, "the new column is not the fourth")
	}
	if a, err = r.c.do(ctx, "PATCH", columnPath, map[string]any{"name": "In review"}, 200); err != nil {
		return err
	}
	if a.field("name") != "In review" {
		return errors.New(tagAnswerUnexpected, "the column was not renamed")
	}
	if _, err = r.c.do(ctx, "POST", columnPath+"/move", map[string]any{"position": 1}, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", room+"/board", nil, 200)
	if err != nil {
		return err
	}
	if names, _ = columnNames(a); fmt.Sprint(names) != "[To do In review In progress Done]" {
		return errors.New(tagAnswerUnexpected, "the columns are not in the order asked", "columns", fmt.Sprint(names))
	}
	if _, err = r.c.do(ctx, "DELETE", columnPath, nil, 204); err != nil {
		return err
	}

	// The files come back as they went.
	for i, want := range []struct {
		contentType string
		size        int
	}{{"application/octet-stream", len(boardNote)}, {"image/png", len(pixel)}} {
		a, err = r.c.download(ctx, attachmentURLs[i], 200)
		if err != nil {
			return err
		}
		if got := a.header.Get("Content-Type"); got != want.contentType || len(a.body) != want.size ||
			a.header.Get("X-Content-Type-Options") != "nosniff" {
			return errors.New(tagAnswerUnexpected, "the download is not as uploaded", "path", attachmentURLs[i], "contentType", got, "bytes", len(a.body))
		}
	}

	// Search by the word and the tag.
	a, err = r.c.do(ctx, "GET", "/api/v1/search?q="+url.QueryEscape(boardWord+" #apicheck"), nil, 200)
	if err != nil {
		return err
	}
	if !searchFinds(a, postID) {
		return errors.New(tagAnswerUnexpected, "the search does not find the post", "post", postID)
	}

	// The other user: the assignment is in the notifications; read them.
	alice := r.c.token
	r.c.token = ""
	if _, err = r.loginAs(ctx, r.otherUsername, r.otherPassword); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", "/api/v1/notifications", nil, 200)
	if err != nil {
		return err
	}
	var newest uint64
	assigned := false
	for i, item := range a.items("notifications") {
		n, _ := item.(map[string]any)
		id, _ := n["id"].(float64)
		if i == 0 {
			newest = uint64(id)
		}
		if n["kind"] == "assigned" && n["task_id"] == float64(taskID) && n["read"] == false {
			assigned = true
		}
	}
	if !assigned {
		return errors.New(tagAnswerUnexpected, "the other user has no notification of the assignment", "task", taskID)
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/notifications/read", map[string]any{"up_to_id": newest}, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", "/api/v1/me", nil, 200)
	if err != nil {
		return err
	}
	if a.number("unread_notifications") != 0 {
		return errors.New(tagAnswerUnexpected, "notifications are still unread", "unread", a.number("unread_notifications"))
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}

	// The room goes, as its creator may, so that a run leaves nothing behind.
	r.c.token = alice
	if _, err = r.c.do(ctx, "DELETE", room, nil, 204); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}

// searchFinds reports whether a search answer has a result for the post with
// a matching segment.
func searchFinds(a answer, postID uint64) bool {
	for _, item := range a.items("results") {
		res, _ := item.(map[string]any)
		if id, _ := res["post_id"].(float64); uint64(id) != postID {
			continue
		}
		segs, _ := res["snippet"].([]any)
		for _, s := range segs {
			seg, _ := s.(map[string]any)
			if seg["match"] == true {
				return true
			}
		}
	}
	return false
}
