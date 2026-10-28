package api

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/errors"
)

const (
	// avatarField is the name of the field of the form that holds the picture.
	avatarField = "file"
	// avatarBodySlack is what the cap of an avatar request allows besides the
	// picture: the framing of the form.
	avatarBodySlack = 256 << 10
	// messageAvatarOneFile is the problem with a form that is not one file in
	// the field "file".
	messageAvatarOneFile = "Send exactly one picture in the field file."
	// messageProfileEmpty and messageSettingsEmpty are the problems with a
	// change that changes nothing.
	messageProfileEmpty  = "Send the new display name."
	messageSettingsEmpty = "Send at least one setting."
)

// profilePatch is the body of PATCH /me.
type profilePatch struct {
	DisplayName *string `json:"display_name"`
}

// settingsPatch is the body of PATCH /me/settings: a field that is not there
// (or null) stays as it is.
type settingsPatch struct {
	Theme                *string `json:"theme"`
	Sound                *bool   `json:"sound"`
	BrowserNotifications *bool   `json:"browser_notifications"`
	Notify               *string `json:"notify"`
	SoundName            *string `json:"sound_name"`
	Replies              *string `json:"replies"`
}

// passwordRequest is the body of POST /me/password.
type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// settingsRoutes are the routes of the profile, the settings, the avatar and
// the password of the logged-in user (SPEC §7.14).
func (s *Server) settingsRoutes() []Route {
	return []Route{
		s.route(http.MethodPatch, "/me", false, s.patchMe),
		s.route(http.MethodGet, "/me/settings", false, s.getSettings),
		s.route(http.MethodPatch, "/me/settings", false, s.patchSettings),
		s.route(http.MethodPut, "/me/avatar", false, s.putAvatar),
		s.route(http.MethodDelete, "/me/avatar", false, s.deleteAvatar),
		s.route(http.MethodPost, "/me/password", false, s.changePassword),
		s.route(http.MethodGet, "/avatars/{id}", false, s.avatarFile),
	}
}

// patchMe answers PATCH /me: it sets the display name (1 to 64 characters,
// trimmed) and returns the user. A body without display_name is 422.
func (s *Server) patchMe(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	var body profilePatch
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := checkProfilePatch(body); err != nil {
		s.writeError(w, r, err)
		return
	}
	user, err := s.svc.Accounts.UpdateProfile(r.Context(), c.actor, *body.DisplayName)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, userOf(user))
}

// checkProfilePatch checks that a change of the profile changes something: a
// body without display_name fails with tagProfileEmpty.
func checkProfilePatch(body profilePatch) error {
	if body.DisplayName == nil {
		return errors.New(tagProfileEmpty, "the change has no display name",
			"fields", map[string]string{"display_name": messageProfileEmpty})
	}
	return nil
}

// checkSettingsPatch checks that a change of the settings changes something: a
// body with none of them fails with tagSettingsEmpty.
func checkSettingsPatch(body settingsPatch) error {
	if body.Theme == nil && body.Sound == nil && body.BrowserNotifications == nil &&
		body.Notify == nil && body.SoundName == nil && body.Replies == nil {
		return errors.New(tagSettingsEmpty, "the change has no setting",
			"fields", map[string]string{
				"theme": messageSettingsEmpty, "sound": messageSettingsEmpty, "browser_notifications": messageSettingsEmpty,
				"notify": messageSettingsEmpty, "sound_name": messageSettingsEmpty, "replies": messageSettingsEmpty,
			})
	}
	return nil
}

// getSettings answers GET /me/settings: the theme, the sound, the browser
// notifications and what notifies, with the defaults for a user who saved none.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	settings, err := s.svc.Accounts.Settings(r.Context(), c.actor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, settingsOf(settings))
}

// patchSettings answers PATCH /me/settings: the settings that are in the body
// change, the others stay, and the settings come back. A body with none of
// them is 422.
func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	var body settingsPatch
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := checkSettingsPatch(body); err != nil {
		s.writeError(w, r, err)
		return
	}
	update := app.SettingsUpdate{Sound: body.Sound, BrowserNotifications: body.BrowserNotifications}
	if body.Theme != nil {
		theme := app.Theme(*body.Theme)
		update.Theme = &theme
	}
	if body.Notify != nil {
		level := app.NotifyLevel(*body.Notify)
		update.Notify = &level
	}
	if body.SoundName != nil {
		name := app.SoundName(*body.SoundName)
		update.SoundName = &name
	}
	if body.Replies != nil {
		mode := app.RepliesMode(*body.Replies)
		update.Replies = &mode
	}
	settings, err := s.svc.Accounts.UpdateSettings(r.Context(), c.actor, update)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, settingsOf(settings))
}

// putAvatar answers PUT /me/avatar: the picture in the field "file" of a
// multipart/form-data body becomes the avatar, and the user comes back. The
// Content-Type must be multipart/form-data (415 otherwise), the body is cut at
// the size of a picture and a quarter of a megabyte at most (413), the form
// holds nothing but the file (422 on file), and a file that breaks the rules of
// an avatar is 422 on file too.
func (s *Server) putAvatar(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	picture, err := s.readAvatar(w, r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	user, err := s.svc.Accounts.SetAvatar(r.Context(), c.actor, bytes.NewReader(picture))
	if err != nil {
		s.writeError(w, r, renameField(err, app.FieldAvatar, avatarField))
		return
	}
	s.writeJSON(w, r, http.StatusOK, userOf(user))
}

// readAvatar reads the form of an avatar request and returns the bytes of its
// file, at most one byte more than an avatar may have (the service refuses a
// picture of that size). A body that is not multipart/form-data fails with
// tagUnsupportedMediaType, one that is cut at the cap with tagBodyTooLarge,
// and a form that is not exactly one file in the field "file" with
// tagUploadInvalid.
func (s *Server) readAvatar(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return nil, errors.New(tagUnsupportedMediaType, "the body is not multipart/form-data",
			"contentType", r.Header.Get("Content-Type"))
	}
	limit := int64(app.AvatarMaxBytes + avatarBodySlack)
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, errors.NewErr(tagUploadInvalid, err).
			SetMetadata("fields", map[string]string{avatarField: messageAvatarOneFile})
	}
	part, err := mr.NextPart()
	if err != nil {
		if isBodyTooLarge(err) {
			return nil, errors.NewErr(tagBodyTooLarge, err).SetMetadata("limit", limit)
		}
		return nil, errors.NewErr(tagUploadInvalid, err).
			SetMetadata("fields", map[string]string{avatarField: messageAvatarOneFile})
	}
	if part.FormName() != avatarField || part.FileName() == "" {
		return nil, errors.New(tagUploadInvalid, "the first part of the form is not the file",
			"fields", map[string]string{avatarField: messageAvatarOneFile})
	}
	picture, err := io.ReadAll(io.LimitReader(part, app.AvatarMaxBytes+1))
	if err != nil {
		if isBodyTooLarge(err) {
			return nil, errors.NewErr(tagBodyTooLarge, err).SetMetadata("limit", limit)
		}
		return nil, errors.NewErr(tagUploadInvalid, err).
			SetMetadata("fields", map[string]string{avatarField: messageAvatarOneFile})
	}
	if len(picture) > app.AvatarMaxBytes {
		// Too large for an avatar: the service refuses it (422). Reading on
		// tells a request that is far larger than a picture, which is 413.
		if _, err := io.Copy(io.Discard, part); err != nil && isBodyTooLarge(err) {
			return nil, errors.NewErr(tagBodyTooLarge, err).SetMetadata("limit", limit)
		}
		return picture, nil
	}
	// Nothing may follow the file.
	if _, err := mr.NextPart(); err != io.EOF {
		return nil, errors.New(tagUploadInvalid, "the form holds more than the file",
			"fields", map[string]string{avatarField: messageAvatarOneFile})
	}
	return picture, nil
}

// deleteAvatar answers DELETE /me/avatar: the avatar is removed, which brings
// back the initials. A user without an avatar is not an error.
func (s *Server) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	if _, err := s.svc.Accounts.RemoveAvatar(r.Context(), c.actor); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// changePassword answers POST /me/password: the current password and the new
// one. It ends every other session of the user, API tokens included, and keeps
// the one of this request. A wrong current password is 422 on current_password
// and counts as a failed login in the limiter (429 once the limiter blocks).
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	var body passwordRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	err := s.svc.Accounts.ChangePassword(r.Context(), c.actor, c.sessionID, app.PasswordChange{
		CurrentPassword: body.CurrentPassword,
		NewPassword:     body.NewPassword,
		ClientIP:        s.clientIP(r),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}

// avatarFile answers GET /avatars/{id} with the avatar of the user, a PNG
// of 256 by 256 pixels, for any logged-in user: the pages' address
// /avatars/{id} needs the cookie of the pages, which a client does not have.
// A user without an avatar and an unknown user are 404. The address carries
// the key of the avatar (?v=), so the picture is cached for a day.
func (s *Server) avatarFile(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	f, key, err := s.svc.Accounts.OpenAvatar(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			s.logger.Debug("closing an avatar failed", "pattern", r.Pattern, "err", closeErr)
		}
	}()
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", files.DownloadCacheControl)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", files.DownloadCSP)
	h.Set("ETag", `"`+key+`"`)
	http.ServeContent(w, r, "", time.Time{}, f)
}

const (
	tagProfileEmpty  = "ProfileChangeEmpty"
	tagSettingsEmpty = "SettingsChangeEmpty"
)
