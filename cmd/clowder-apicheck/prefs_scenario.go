package main

import (
	"context"
	"fmt"
	"time"

	"github.com/rm4n0s/errors"
)

// prefsScenarioName is the name of the notification preferences scenario.
const prefsScenarioName = "prefs"

// prefsScenario is the scenario of step 18.3 for the notification
// preferences. It logs in, reads the global choices, makes a group with the
// other two users and sets it to "mentions", mutes the first room the user is
// a member of, reads both back, and puts everything as it was: the group is
// left, the room is unmuted and its choice is back to "default".
func prefsScenario(ctx context.Context, r *runner) error {
	if _, err := r.login(ctx); err != nil {
		return err
	}
	a, err := r.c.do(ctx, "GET", "/api/v1/me/settings", nil, 200)
	if err != nil {
		return err
	}
	notify, _ := a.field("notify").(string)
	soundName, _ := a.field("sound_name").(string)
	replies, _ := a.field("replies").(string)
	if notify == "" || soundName == "" || replies == "" {
		return errors.New(tagAnswerUnexpected, "the settings lack notify, sound_name or replies")
	}

	// A group, set to mentions.
	name := fmt.Sprintf("apicheck-prefs-%d", time.Now().UnixNano())
	a, err = r.c.do(ctx, "POST", "/api/v1/conversations", map[string]any{
		"usernames": []string{r.otherUsername, r.thirdUsername}, "name": name,
	}, 201)
	if err != nil {
		return err
	}
	group := a.number("id")
	if group == 0 || a.field("kind") != "group" {
		return errors.New(tagAnswerUnexpected, "the group is not as asked", "id", group)
	}
	groupPrefs := fmt.Sprintf("/api/v1/conversations/%d/notification-prefs", group)
	a, err = r.c.do(ctx, "GET", groupPrefs, nil, 200)
	if err != nil {
		return err
	}
	if a.field("notify") != "default" || a.field("muted") != false {
		return errors.New(tagAnswerUnexpected, "a new group has preferences of its own", "notify", a.field("notify"))
	}
	a, err = r.c.do(ctx, "PUT", groupPrefs, map[string]any{"notify": "mentions"}, 200)
	if err != nil {
		return err
	}
	if a.field("notify") != "mentions" {
		return errors.New(tagAnswerUnexpected, "the group did not change to mentions", "got", a.field("notify"))
	}
	a, err = r.c.do(ctx, "GET", groupPrefs, nil, 200)
	if err != nil {
		return err
	}
	if a.field("notify") != "mentions" {
		return errors.New(tagAnswerUnexpected, "the group is not at mentions when read back", "got", a.field("notify"))
	}
	// An empty body and a wrong choice are refused.
	if _, err = r.c.do(ctx, "PUT", groupPrefs, map[string]any{}, 422); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "PUT", groupPrefs, map[string]any{"notify": "loudly"}, 422); err != nil {
		return err
	}

	// A room of the user, muted.
	a, err = r.c.do(ctx, "GET", "/api/v1/rooms", nil, 200)
	if err != nil {
		return err
	}
	// One the user is in; when there is none, a visible room is joined for the
	// check and left again (leaving ends the preferences of the room).
	var roomID uint64
	var joined bool
	for _, item := range a.items("rooms") {
		room, _ := item.(map[string]any)
		id, _ := room["id"].(float64)
		if room["is_member"] == true {
			roomID, joined = uint64(id), false
			break
		}
		if room["visibility"] == "visible" && roomID == 0 {
			roomID, joined = uint64(id), true
		}
	}
	if roomID != 0 {
		roomPath := fmt.Sprintf("/api/v1/rooms/%d", roomID)
		if joined {
			if _, err = r.c.do(ctx, "POST", roomPath+"/join", nil, 200, 201, 204); err != nil {
				return err
			}
		}
		roomPrefs := roomPath + "/notification-prefs"
		a, err = r.c.do(ctx, "GET", roomPrefs, nil, 200)
		if err != nil {
			return err
		}
		wasMuted := a.field("muted") == true
		a, err = r.c.do(ctx, "PUT", roomPrefs, map[string]any{"muted": true}, 200)
		if err != nil {
			return err
		}
		if a.field("muted") != true {
			return errors.New(tagAnswerUnexpected, "the room was not muted")
		}
		a, err = r.c.do(ctx, "GET", roomPrefs, nil, 200)
		if err != nil {
			return err
		}
		if a.field("muted") != true {
			return errors.New(tagAnswerUnexpected, "the room is not muted when read back")
		}
		if _, err = r.c.do(ctx, "PUT", roomPrefs, map[string]any{"muted": wasMuted}, 200); err != nil {
			return err
		}
		if joined {
			if _, err = r.c.do(ctx, "POST", roomPath+"/leave", nil, 204); err != nil {
				return err
			}
		}
	}

	// Put the group back and leave it.
	if _, err = r.c.do(ctx, "PUT", groupPrefs, map[string]any{"notify": "default"}, 200); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", fmt.Sprintf("/api/v1/conversations/%d/leave", group), nil, 204); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "GET", groupPrefs, nil, 404); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}
