package api

import "github.com/rm4n0s/clowder/internal/app"

// userRefJSON names a user on a room, a post or a reaction: the three fields a
// client needs to show a person.
type userRefJSON struct {
	ID          uint64 `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	// AvatarURL is the address of the avatar (see avatarURL), null for none.
	AvatarURL *string `json:"avatar_url"`
}

// userRefOf makes the JSON of a reference to a user.
func userRefOf(u app.UserRef) userRefJSON {
	return userRefJSON{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, AvatarURL: avatarURL(u.ID, u.AvatarKey)}
}

// userRefsOf makes the JSON of references to users; never nil.
func userRefsOf(users []app.UserRef) []userRefJSON {
	out := make([]userRefJSON, len(users))
	for i, u := range users {
		out[i] = userRefOf(u)
	}
	return out
}

// roomJSON is a room in the answers of the API.
type roomJSON struct {
	ID          uint64      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Visibility  string      `json:"visibility"`
	CreatedBy   userRefJSON `json:"created_by"`
	CreatedAt   string      `json:"created_at"`
	MemberCount int         `json:"member_count"`
	IsMember    bool        `json:"is_member"`
	CanManage   bool        `json:"can_manage"`
	// UnreadPosts and UnreadMentions are the unread counts of the caller in the
	// room (SPEC §7.17); 0 for a room the caller is not a member of. They are
	// the raw numbers, whatever the notification preferences of the room say.
	UnreadPosts    int `json:"unread_posts"`
	UnreadMentions int `json:"unread_mentions"`
}

// roomOf makes the JSON of a room.
func roomOf(r app.Room) roomJSON {
	return roomJSON{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		Visibility:  string(r.Visibility),
		CreatedBy:   userRefOf(r.Creator),
		CreatedAt:   rfc3339(r.CreatedAt),
		MemberCount: r.MemberCount,
		IsMember:    r.IsMember,
		CanManage:   r.CanManage,

		UnreadPosts:    r.UnreadPosts,
		UnreadMentions: r.UnreadMentions,
	}
}

// memberJSON is a member of a room in the answers of the API.
type memberJSON struct {
	User     userRefJSON  `json:"user"`
	AddedBy  *userRefJSON `json:"added_by"`
	JoinedAt string       `json:"joined_at"`
}

// memberOf makes the JSON of a member.
func memberOf(m app.Member) memberJSON {
	out := memberJSON{
		User:     userRefJSON{ID: m.UserID, Username: m.Username, DisplayName: m.DisplayName, AvatarURL: avatarURL(m.UserID, m.AvatarKey)},
		JoinedAt: rfc3339(m.JoinedAt),
	}
	if m.AddedBy != nil {
		out.AddedBy = &userRefJSON{ID: *m.AddedBy, Username: m.AddedByUsername, DisplayName: m.AddedByDisplayName}
	}
	return out
}
