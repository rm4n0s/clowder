package api

import "github.com/rm4n0s/clowder/internal/app"

// userJSON is a user in the answers of the API.
type userJSON struct {
	ID          uint64 `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	CreatedAt   string `json:"created_at"`
}

// userOf makes the JSON of a user.
func userOf(u app.User) userJSON {
	return userJSON{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        string(u.Role),
		CreatedAt:   rfc3339(u.CreatedAt),
	}
}

// sessionJSON is a session of the user in the answers of the API: never its
// token.
type sessionJSON struct {
	ID         uint64 `json:"id"`
	Kind       string `json:"kind"`
	ClientName string `json:"client_name"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
	Current    bool   `json:"current"`
}

// sessionOf makes the JSON of a session.
func sessionOf(s app.SessionInfo) sessionJSON {
	return sessionJSON{
		ID:         s.ID,
		Kind:       string(s.Kind),
		ClientName: s.ClientName,
		CreatedAt:  rfc3339(s.CreatedAt),
		LastSeenAt: rfc3339(s.LastSeenAt),
		ExpiresAt:  rfc3339(s.ExpiresAt),
		Current:    s.Current,
	}
}
