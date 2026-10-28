package main

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/rm4n0s/errors"
)

// msgSearchScenarioName is the name of the scenario of the search of private
// messages.
const msgSearchScenarioName = "msgsearch"

// foundMessage returns the result with the message ID in the answer of
// GET /messages/search, nil when it is not there.
func foundMessage(a answer, id uint64) map[string]any {
	for _, item := range a.items("results") {
		res, _ := item.(map[string]any)
		if n, _ := res["message_id"].(float64); uint64(n) == id {
			return res
		}
	}
	return nil
}

// msgSearchScenario is the scenario of step 20.3. As the runner's user it
// opens the direct conversation with the other user (or finds the one there
// is), sends a message with a word nobody wrote before and finds it with the
// search of messages, with the word marked in the snippet. Queries the search
// refuses (no word, a filter) are 422 on "q". It then edits the message so
// that the word goes away and finds nothing, and deletes the message.
func msgSearchScenario(ctx context.Context, r *runner) error {
	if _, err := r.login(ctx); err != nil {
		return err
	}
	a, err := r.c.do(ctx, "POST", "/api/v1/conversations", map[string]any{"usernames": []string{r.otherUsername}}, 201, 200)
	if err != nil {
		return err
	}
	conv := a.number("id")
	if conv == 0 {
		return errors.New(tagAnswerUnexpected, "the conversation has no ID")
	}
	word := fmt.Sprintf("apicheck%d", time.Now().UnixNano())
	a, err = r.c.do(ctx, "POST", fmt.Sprintf("/api/v1/conversations/%d/messages", conv),
		map[string]any{"body": "Search check: " + word + " is the word"}, 201)
	if err != nil {
		return err
	}
	msg := a.number("id")
	if msg == 0 {
		return errors.New(tagAnswerUnexpected, "the new message has no ID")
	}
	search := "/api/v1/messages/search?q="

	// The word finds the message, with the match in the snippet.
	a, err = r.c.do(ctx, "GET", search+url.QueryEscape(word), nil, 200)
	if err != nil {
		return err
	}
	res := foundMessage(a, msg)
	if res == nil {
		return errors.New(tagAnswerUnexpected, "the search does not find the message", "id", msg)
	}
	conversation, _ := res["conversation"].(map[string]any)
	if n, _ := conversation["id"].(float64); uint64(n) != conv || conversation["title"] == "" {
		return errors.New(tagAnswerUnexpected, "the result names another conversation", "conversation", conversation)
	}
	marked := false
	segments, _ := res["snippet"].([]any)
	for _, s := range segments {
		seg, _ := s.(map[string]any)
		if seg["match"] == true && seg["text"] == word {
			marked = true
		}
	}
	if !marked {
		return errors.New(tagAnswerUnexpected, "the snippet does not mark the word", "snippet", res["snippet"])
	}

	// Queries the search of messages refuses.
	for _, q := range []string{"", "#" + word, word + " room:lobby"} {
		if _, err = r.c.do(ctx, "GET", search+url.QueryEscape(q), nil, 422); err != nil {
			return err
		}
	}

	// An edit takes the word away: nothing is found.
	if _, err = r.c.do(ctx, "PATCH", fmt.Sprintf("/api/v1/messages/%d", msg), map[string]any{"body": "Search check: edited"}, 200); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", search+url.QueryEscape(word), nil, 200)
	if err != nil {
		return err
	}
	if len(a.items("results")) != 0 || a.field("next_before") != nil {
		return errors.New(tagAnswerUnexpected, "the search still finds the word after the edit", "results", a.field("results"))
	}

	// A deleted message is not found by any of its words.
	if _, err = r.c.do(ctx, "DELETE", fmt.Sprintf("/api/v1/messages/%d", msg), nil, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", search+"edited", nil, 200)
	if err != nil {
		return err
	}
	if foundMessage(a, msg) != nil {
		return errors.New(tagAnswerUnexpected, "the search finds a deleted message", "id", msg)
	}
	return nil
}
