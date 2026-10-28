package api

import (
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
)

// userJSON is a user in the answers of the API.
type userJSON struct {
	ID          uint64 `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	// AvatarURL is the address of the avatar for GET with the token, or null
	// when the user has none.
	AvatarURL *string `json:"avatar_url"`
	CreatedAt string  `json:"created_at"`
}

// userOf makes the JSON of a user.
func userOf(u app.User) userJSON {
	return userJSON{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        string(u.Role),
		AvatarURL:   avatarURL(u.ID, u.AvatarKey),
		CreatedAt:   rfc3339(u.CreatedAt),
	}
}

// avatarURL is the address of the avatar of the user for a client of the API,
// /api/v1/avatars/{id}?v=<key>, or nil when the key is empty. The pages' own
// address, /avatars/{id}, reads the session cookie, which a client of the API
// does not have; the key changes with the picture, so the address does too.
func avatarURL(userID uint64, key string) *string {
	if key == "" {
		return nil
	}
	u := Prefix + "/avatars/" + strconv.FormatUint(userID, 10) + "?v=" + key
	return &u
}

// settingsJSON are the settings of the user in the answers of the API.
type settingsJSON struct {
	Theme                string `json:"theme"`
	Sound                bool   `json:"sound"`
	BrowserNotifications bool   `json:"browser_notifications"`
	Notify               string `json:"notify"`
	SoundName            string `json:"sound_name"`
	Replies              string `json:"replies"`
	// Keywords are the mention keywords; [] for none.
	Keywords []string `json:"keywords"`
	// GroupMentions is true when @room, @all and @here reach the user.
	GroupMentions bool `json:"group_mentions"`
}

// settingsOf makes the JSON of the settings.
func settingsOf(s app.Settings) settingsJSON {
	keywords := s.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	return settingsJSON{
		Theme: string(s.Theme), Sound: s.Sound, BrowserNotifications: s.BrowserNotifications,
		Notify: string(s.Notify), SoundName: string(s.SoundName), Replies: string(s.Replies),
		Keywords: keywords, GroupMentions: s.GroupMentions,
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
