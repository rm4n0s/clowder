package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/rm4n0s/errors"
)

// adminScenarioName is the name of the admin scenario, which -admin-username
// can leave out.
const adminScenarioName = "admin"

// adminScenario is the scenario of step 16.4 for the admin routes. It logs in
// as the admin, lists the users, makes an invitation for a member, checks
// that the link works, lists the rooms (the private ones too) and the members
// of the first, and reads the audit log, filtered by an action and by a room.
// Then it logs in as the runner's user, who is a member, and sees the admin
// routes refused with 403.
func adminScenario(ctx context.Context, r *runner) error {
	r.c.redac.add(r.adminPassword)
	if _, err := r.loginAs(ctx, r.adminUsername, r.adminPassword); err != nil {
		return err
	}
	a, err := r.c.do(ctx, "GET", "/api/v1/me", nil, 200)
	if err != nil {
		return err
	}
	if a.field("user", "role") != "admin" {
		return errors.New(tagAnswerUnexpected, "the admin user is not an admin", "username", r.adminUsername)
	}

	a, err = r.c.do(ctx, "GET", "/api/v1/admin/users", nil, 200)
	if err != nil {
		return err
	}
	var sawAdmin bool
	for _, item := range a.items("users") {
		u, _ := item.(map[string]any)
		if u["username"] == r.adminUsername && u["role"] == "admin" && u["disabled"] == false {
			sawAdmin = true
		}
	}
	if !sawAdmin {
		return errors.New(tagAnswerUnexpected, "the users do not include the admin", "username", r.adminUsername)
	}

	a, err = r.c.do(ctx, "POST", "/api/v1/admin/invitations", map[string]any{"role": "member"}, 201)
	if err != nil {
		return err
	}
	link, _ := a.field("url").(string)
	_, token, found := strings.Cut(link, "/invite/")
	if !found || token == "" || a.field("role") != "member" || a.field("expires_at") == nil {
		return errors.New(tagAnswerUnexpected, "the invitation has no link, role and expiry")
	}
	r.c.redac.add(token)
	// The link is good, and is for a member.
	a, err = r.c.do(ctx, "GET", "/api/v1/auth/invitations/"+token, nil, 200)
	if err != nil {
		return err
	}
	if a.field("role") != "member" {
		return errors.New(tagAnswerUnexpected, "the invitation is not for a member")
	}

	a, err = r.c.do(ctx, "GET", "/api/v1/admin/rooms", nil, 200)
	if err != nil {
		return err
	}
	rooms := a.items("rooms")
	if len(rooms) == 0 {
		return errors.New(tagAnswerUnexpected, "there are no rooms to list")
	}
	first, _ := rooms[0].(map[string]any)
	firstID, _ := first["id"].(float64)
	a, err = r.c.do(ctx, "GET", fmt.Sprintf("/api/v1/admin/rooms/%d/members", uint64(firstID)), nil, 200)
	if err != nil {
		return err
	}
	if a.field("members") == nil {
		return errors.New(tagAnswerUnexpected, "the room has no list of members")
	}

	a, err = r.c.do(ctx, "GET", "/api/v1/admin/audit?action=room.created&page=1", nil, 200)
	if err != nil {
		return err
	}
	events := a.items("events")
	if len(events) == 0 {
		return errors.New(tagAnswerUnexpected, "the audit log has no room.created event")
	}
	for _, item := range events {
		ev, _ := item.(map[string]any)
		if ev["action"] != "room.created" || ev["entity_type"] != "room" {
			return errors.New(tagAnswerUnexpected, "the audit log answers with another action than asked", "action", ev["action"])
		}
	}
	a, err = r.c.do(ctx, "GET", fmt.Sprintf("/api/v1/admin/audit?room_id=%d", uint64(firstID)), nil, 200)
	if err != nil {
		return err
	}
	if len(a.items("events")) == 0 {
		return errors.New(tagAnswerUnexpected, "the audit log has no event of the room", "room", uint64(firstID))
	}
	// A filter that is not valid is refused.
	if _, err = r.c.do(ctx, "GET", "/api/v1/admin/audit?from=yesterday", nil, 422); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}

	// A member is refused. (A runner whose user is an admin has no member to
	// try.)
	r.c.token = ""
	if a, err = r.login(ctx); err != nil {
		return err
	}
	if a.field("user", "role") == "admin" {
		if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
			return err
		}
		r.c.token = ""
		return nil
	}
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/rooms", "/api/v1/admin/audit"} {
		if _, err = r.c.do(ctx, "GET", path, nil, 403); err != nil {
			return err
		}
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/admin/invitations", map[string]any{"role": "member"}, 403); err != nil {
		return err
	}
	if _, err = r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}
