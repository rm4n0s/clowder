package api

import "github.com/rm4n0s/clowder/internal/app"

// adminUserJSON is a user in the answers of the admin routes: a user and
// whether an admin disabled the account.
type adminUserJSON struct {
	ID          uint64 `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	// AvatarURL is the address of the avatar (see avatarURL), null for none.
	AvatarURL *string `json:"avatar_url"`
	CreatedAt string  `json:"created_at"`
}

// adminUserOf makes the JSON of a user for an admin.
func adminUserOf(u app.User) adminUserJSON {
	return adminUserJSON{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        string(u.Role),
		Disabled:    u.Disabled,
		AvatarURL:   avatarURL(u.ID, u.AvatarKey),
		CreatedAt:   rfc3339(u.CreatedAt),
	}
}

// adminUserListResponse is the answer of GET /admin/users.
type adminUserListResponse struct {
	Users []adminUserJSON `json:"users"`
}

// adminInvitationJSON is a new invitation: the link, shown once.
type adminInvitationJSON struct {
	URL       string `json:"url"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expires_at"`
}

// adminResetLinkJSON is a new password reset link: the link, shown once.
type adminResetLinkJSON struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// auditEventJSON is an event of the audit log in the answers of the API.
type auditEventJSON struct {
	ID         uint64         `json:"id"`
	Actor      *userRefJSON   `json:"actor"`
	Action     string         `json:"action"`
	RoomID     uint64         `json:"room_id"`
	EntityType string         `json:"entity_type"`
	EntityID   uint64         `json:"entity_id"`
	Summary    string         `json:"summary"`
	Details    map[string]any `json:"details"`
	CreatedAt  string         `json:"created_at"`
}

// auditEventOf makes the JSON of an audit event.
func auditEventOf(ev app.AuditView) auditEventJSON {
	out := auditEventJSON{
		ID:         ev.ID,
		Action:     string(ev.Action),
		RoomID:     ev.RoomID,
		EntityType: string(ev.EntityType),
		EntityID:   ev.EntityID,
		Summary:    ev.Summary,
		Details:    ev.Details,
		CreatedAt:  rfc3339(ev.CreatedAt),
	}
	if out.Details == nil {
		out.Details = map[string]any{}
	}
	if ev.ActorID != 0 {
		out.Actor = &userRefJSON{ID: ev.ActorID, Username: ev.ActorUsername, DisplayName: ev.ActorDisplayName}
	}
	return out
}

// auditResponse is the answer of GET /admin/audit.
type auditResponse struct {
	Events  []auditEventJSON `json:"events"`
	Page    int              `json:"page"`
	HasMore bool             `json:"has_more"`
}
