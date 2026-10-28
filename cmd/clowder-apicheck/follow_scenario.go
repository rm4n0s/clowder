package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/rm4n0s/errors"
)

// followScenarioName is the name of the follow scenario.
const followScenarioName = "follow"

// followScenario is the scenario of step 18.4 for following posts, group
// mentions, keywords and presence. It logs in, makes a private room and posts
// in it (the author follows the post), unfollows and follows it again, follows
// through a comment, counts the people of an @room, reads who is online, sets
// and clears a mention keyword and the group mentions, and deletes the room.
func followScenario(ctx context.Context, r *runner) error {
	a, err := r.login(ctx)
	if err != nil {
		return err
	}
	me := a.number("user", "id")

	// A room of my own with a post.
	name := fmt.Sprintf("apicheck-follow-%d", time.Now().UnixNano())
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
	a, err = r.c.do(ctx, "POST", room+"/posts", map[string]any{"format": "text", "body": "A post to follow."}, 201)
	if err != nil {
		return err
	}
	post := fmt.Sprintf("/api/v1/posts/%d", a.number("id"))
	if a.field("following") != true {
		return errors.New(tagAnswerUnexpected, "the author does not follow their post")
	}

	// Unfollow, twice; follow, twice; each read back.
	steps := []struct {
		method string
		want   bool
	}{{"DELETE", false}, {"DELETE", false}, {"PUT", true}, {"PUT", true}}
	for _, step := range steps {
		if _, err = r.c.do(ctx, step.method, post+"/follow", nil, 204); err != nil {
			return err
		}
		a, err = r.c.do(ctx, "GET", post, nil, 200)
		if err != nil {
			return err
		}
		if a.field("following") != step.want {
			return errors.New(tagAnswerUnexpected, "the post is not as followed as asked", "method", step.method, "following", a.field("following"))
		}
	}

	// A comment acts on its root post: unfollowing through it, then the
	// comment of the user does not bring the follow back.
	a, err = r.c.do(ctx, "POST", post+"/comments", map[string]any{"format": "text", "body": "A comment."}, 201)
	if err != nil {
		return err
	}
	comment := fmt.Sprintf("/api/v1/posts/%d", a.number("id"))
	if _, err = r.c.do(ctx, "DELETE", comment+"/follow", nil, 204); err != nil {
		return err
	}
	for _, path := range []string{post, comment} {
		a, err = r.c.do(ctx, "GET", path, nil, 200)
		if err != nil {
			return err
		}
		if a.field("following") != false {
			return errors.New(tagAnswerUnexpected, "unfollowing through a comment left the post followed", "path", path)
		}
	}
	if _, err = r.c.do(ctx, "POST", post+"/comments", map[string]any{"format": "text", "body": "Another comment."}, 201); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", post, nil, 200)
	if err != nil {
		return err
	}
	if a.field("following") != false {
		return errors.New(tagAnswerUnexpected, "a comment brought the follow back")
	}
	if _, err = r.c.do(ctx, "PUT", "/api/v1/posts/999999999/follow", nil, 404); err != nil {
		return err
	}

	// Group mentions: nobody else is in the room, so @room counts 0; a body
	// with none counts 0 as well.
	for _, body := range []string{"@room hello", "no mention"} {
		a, err = r.c.do(ctx, "GET", room+"/group-mention-count?format=text&body="+url.QueryEscape(body), nil, 200)
		if err != nil {
			return err
		}
		if a.field("count") != float64(0) {
			return errors.New(tagAnswerUnexpected, "the count of an empty room is not 0", "body", body, "count", a.field("count"))
		}
	}
	if _, err = r.c.do(ctx, "GET", room+"/group-mention-count?format=html", nil, 422); err != nil {
		return err
	}

	// Presence: the client has no page or WebSocket open, so nobody is online.
	a, err = r.c.do(ctx, "GET", "/api/v1/me/presence?user_ids="+strconv.FormatUint(me, 10), nil, 200)
	if err != nil {
		return err
	}
	if online, _ := a.field("online").(map[string]any); len(online) != 0 {
		return errors.New(tagAnswerUnexpected, "somebody is online without a connection", "online", online)
	}
	if _, err = r.c.do(ctx, "GET", "/api/v1/me/presence?user_ids=abc", nil, 422); err != nil {
		return err
	}

	// A keyword and the group mentions, set and put back.
	a, err = r.c.do(ctx, "GET", "/api/v1/me/settings", nil, 200)
	if err != nil {
		return err
	}
	wasKeywords, _ := a.field("keywords").([]any)
	wasGroups, _ := a.field("group_mentions").(bool)
	a, err = r.c.do(ctx, "PATCH", "/api/v1/me/settings", map[string]any{
		"keywords": []string{"apicheck keyword"}, "group_mentions": !wasGroups,
	}, 200)
	if err != nil {
		return err
	}
	if kw, _ := a.field("keywords").([]any); len(kw) != 1 || kw[0] != "apicheck keyword" || a.field("group_mentions") != !wasGroups {
		return errors.New(tagAnswerUnexpected, "the keyword or the group mentions were not set", "keywords", a.field("keywords"))
	}
	if _, err = r.c.do(ctx, "PATCH", "/api/v1/me/settings", map[string]any{"keywords": []string{"twice", "TWICE"}}, 422); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "PATCH", "/api/v1/me/settings", map[string]any{"keywords": wasKeywords, "group_mentions": wasGroups}, 200)
	if err != nil {
		return err
	}
	if kw, _ := a.field("keywords").([]any); len(kw) != len(wasKeywords) || a.field("group_mentions") != wasGroups {
		return errors.New(tagAnswerUnexpected, "the keywords were not put back")
	}

	// The room goes, with the follow.
	if _, err = r.c.do(ctx, "DELETE", room, nil, 204); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "GET", post, nil, 404); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}
