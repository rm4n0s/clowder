package web

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// avatarFormSlack is what the body of the avatar form may hold besides the
// picture: the multipart framing.
const avatarFormSlack = 256 << 10

// The sections of the settings page that save on their own, as the ids of the
// page name them and as the query "saved" does.
const (
	sectionProfile    = "profile"
	sectionPassword   = "password"
	sectionAppearance = "appearance"
)

// Messages of the forms of the settings page. They are fixed text.
const (
	messageChoosePicture   = "Choose a picture."
	messagePasswordsDiffer = "The two passwords are not the same."
)

// settingsPage answers GET /me/settings: the settings page with its sections
// Profile, Password, Appearance, Notifications, Sign-in and Sessions. The query "saved" (profile,
// password or appearance) shows "Saved" under that section, which is where a
// form posted without JavaScript comes back to.
func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.settingsData(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch r.URL.Query().Get("saved") {
	case sectionProfile:
		d.Profile.Saved = true
	case sectionPassword:
		d.Password.Saved = true
	case sectionAppearance:
		d.Appearance.Saved = true
	case sectionNotifications:
		d.Notifications.Saved = true
	case sectionSignIn:
		d.SignIn.Saved = true
	}
	s.render(w, r, http.StatusOK, views.SettingsPage(d))
}

// settingsData reads everything the settings page shows for the person of ctx.
func (s *Server) settingsData(ctx context.Context) (views.SettingsData, error) {
	actor, _ := actorFrom(ctx)
	user, _ := userFrom(ctx)
	sb, err := s.pageSidebar(ctx, actor, user, 0)
	if err != nil {
		return views.SettingsData{}, err
	}
	settings, err := s.svc.Accounts.Settings(ctx, actor)
	if err != nil {
		return views.SettingsData{}, err
	}
	sessions, err := s.svc.Accounts.Sessions(ctx, actor, sessionIDFrom(ctx))
	if err != nil {
		return views.SettingsData{}, err
	}
	notifications, err := s.notifyPrefs(ctx, actor, settings)
	if err != nil {
		return views.SettingsData{}, err
	}
	signIn, err := s.signInData(ctx, actor)
	if err != nil {
		return views.SettingsData{}, err
	}
	return views.SettingsData{
		Sidebar:       sb,
		Profile:       views.ProfileData{User: user, DisplayName: user.DisplayName},
		Appearance:    views.AppearanceData{Theme: settings.Theme},
		Notifications: notifications,
		SignIn:        signIn,
		Sessions:      sessions,
	}, nil
}

// settingsFailed answers a submit of a section of the settings page that
// failed with err. A Datastar request gets the section alone, with the
// problems; a plain request gets the whole page. change puts the problems and
// the values to keep into the data of the page; section makes the fragment of
// the section from that data.
func (s *Server) settingsFailed(w http.ResponseWriter, r *http.Request, err error, change func(d *views.SettingsData, problems views.FormErrors), section func(d views.SettingsData) templ.Component) {
	s.formFailed(w, r, err, func(problems views.FormErrors) formView {
		page := templ.ComponentFunc(func(ctx context.Context, out io.Writer) error {
			d, err := s.settingsData(ctx)
			if err != nil {
				return err
			}
			change(&d, problems)
			return views.SettingsPage(d).Render(ctx, out)
		})
		fragment := templ.ComponentFunc(func(ctx context.Context, out io.Writer) error {
			// The sections that a failed submit shows need only the user.
			user, _ := userFrom(ctx)
			d := views.SettingsData{Profile: views.ProfileData{User: user, DisplayName: user.DisplayName}}
			if settings, err := s.svc.Accounts.Settings(ctx, actorOf(ctx)); err == nil {
				d.Appearance.Theme = settings.Theme
				if n, err := s.notifyPrefs(ctx, actorOf(ctx), settings); err == nil {
					d.Notifications = n
				}
			}
			if signIn, err := s.signInData(ctx, actorOf(ctx)); err == nil {
				d.SignIn = signIn
			}
			change(&d, problems)
			return section(d).Render(ctx, out)
		})
		return formView{fragment: fragment, page: page}
	})
}

// actorOf returns the actor of the request of ctx, the zero actor when nobody
// is logged in.
func actorOf(ctx context.Context) app.Actor {
	actor, _ := actorFrom(ctx)
	return actor
}

// settingsSaved answers a submit that worked. A Datastar request gets the
// section alone with "Saved", and runs js in the page when it is not empty; a
// plain request is sent back to the page, which says "Saved" under the
// section.
func (s *Server) settingsSaved(w http.ResponseWriter, r *http.Request, section string, fragment templ.Component, js string) {
	if !isDatastar(r) {
		s.redirect(w, r, views.SettingsPath+"?saved="+section+"#"+section, http.StatusSeeOther)
		return
	}
	sse := datastar.NewSSE(w, r)
	if err := sse.PatchElementTempl(fragment); err != nil {
		s.logger.Debug("sse write failed", "err", errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "patch-elements"))
		return
	}
	if js == "" {
		return
	}
	if err := runInBrowser(sse, js); err != nil {
		s.logger.Debug("sse write failed", "err", errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "run-in-browser"))
	}
}

// settingsProfile answers POST /me/settings/profile: it saves the display
// name.
func (s *Server) settingsProfile(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	name := r.PostFormValue("displayName")
	user, err := s.svc.Accounts.UpdateProfile(ctx, actor, name)
	if err != nil {
		s.settingsFailed(w, r, err, func(d *views.SettingsData, problems views.FormErrors) {
			d.Profile.DisplayName, d.Profile.Errors = name, problems
		}, func(d views.SettingsData) templ.Component { return views.ProfileSection(d.Profile) })
		return
	}
	s.settingsSaved(w, r, sectionProfile, views.ProfileSection(views.ProfileData{User: user, DisplayName: user.DisplayName, Saved: true}), "")
}

// settingsAvatar answers POST /me/settings/avatar, a multipart form with the
// picture in the file field "avatar".
func (s *Server) settingsAvatar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, app.AvatarMaxBytes+avatarFormSlack)
	mr, err := r.MultipartReader()
	if err != nil {
		s.rejectForm(w, r, errors.NewErr(tagFormInvalid, err))
		return
	}
	var picture []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.rejectForm(w, r, errors.NewErr(tagFormInvalid, err))
			return
		}
		if part.FormName() != "avatar" || part.FileName() == "" {
			continue
		}
		picture, err = io.ReadAll(io.LimitReader(part, app.AvatarMaxBytes+1))
		if err != nil {
			s.rejectForm(w, r, errors.NewErr(tagFormInvalid, err))
			return
		}
		break
	}
	var user app.User
	if len(picture) == 0 {
		err = errors.New(app.TagInvalidInput, "no picture was sent",
			"fields", map[string]string{app.FieldAvatar: messageChoosePicture})
	} else {
		user, err = s.svc.Accounts.SetAvatar(ctx, actor, bytes.NewReader(picture))
	}
	if err != nil {
		s.settingsFailed(w, r, err, func(d *views.SettingsData, problems views.FormErrors) {
			d.Profile.Errors = problems
		}, func(d views.SettingsData) templ.Component { return views.ProfileSection(d.Profile) })
		return
	}
	s.settingsSaved(w, r, sectionProfile, views.ProfileSection(views.ProfileData{User: user, DisplayName: user.DisplayName, Saved: true}), "")
}

// settingsAvatarRemove answers POST /me/settings/avatar/remove.
func (s *Server) settingsAvatarRemove(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	user, err := s.svc.Accounts.RemoveAvatar(ctx, actor)
	if err != nil {
		s.settingsFailed(w, r, err, func(d *views.SettingsData, problems views.FormErrors) {
			d.Profile.Errors = problems
		}, func(d views.SettingsData) templ.Component { return views.ProfileSection(d.Profile) })
		return
	}
	s.settingsSaved(w, r, sectionProfile, views.ProfileSection(views.ProfileData{User: user, DisplayName: user.DisplayName, Saved: true}), "")
}

// settingsPassword answers POST /me/settings/password: the current password
// and the new one twice. The two new passwords are compared here; the rest is
// the service's.
func (s *Server) settingsPassword(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	var err error
	newPassword := r.PostFormValue("newPassword")
	if newPassword != r.PostFormValue("newPasswordConfirm") {
		err = errors.New(app.TagInvalidInput, "the two new passwords differ",
			"fields", map[string]string{app.FieldNewPasswordConfirm: messagePasswordsDiffer})
	} else {
		err = s.svc.Accounts.ChangePassword(ctx, actor, sessionIDFrom(ctx), app.PasswordChange{
			CurrentPassword: r.PostFormValue("currentPassword"),
			NewPassword:     newPassword,
			ClientIP:        clientIP(r, s.cfg.TrustProxyHeaders),
		})
	}
	if err != nil {
		s.settingsFailed(w, r, err, func(d *views.SettingsData, problems views.FormErrors) {
			d.Password.Errors = problems
		}, func(d views.SettingsData) templ.Component { return views.PasswordSection(d.Password) })
		return
	}
	s.settingsSaved(w, r, sectionPassword, views.PasswordSection(views.PasswordData{Saved: true}), resetFormJS("password-form"))
}

// settingsAppearance answers POST /me/settings/appearance: it saves the theme
// and, for a Datastar request, sets it on the page at once. The form of the
// user menu, on every page, posts to the route with the query from=menu and gets only the menu
// patched; the Appearance section of the settings page gets itself and the
// menu patched.
func (s *Server) settingsAppearance(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	theme := app.Theme(r.PostFormValue("theme"))
	settings, err := s.svc.Accounts.UpdateSettings(ctx, actor, app.SettingsUpdate{Theme: &theme})
	if err != nil {
		s.settingsFailed(w, r, err, func(d *views.SettingsData, problems views.FormErrors) {
			d.Appearance.Errors = problems
		}, func(d views.SettingsData) templ.Component { return views.AppearanceSection(d.Appearance) })
		return
	}
	menu := views.ThemeMenu(settings.Theme)
	if r.URL.Query().Get("from") == "menu" {
		// The theme form of the user menu, on any page: only the menu is
		// patched, and the page changes its theme. On the settings page the
		// radio buttons of the Appearance section follow the choice.
		s.settingsSaved(w, r, sectionAppearance, menu, themeJS(settings.Theme)+", "+themeRadioJS(settings.Theme))
		return
	}
	// The Appearance section, and the user menu, which marks the same choice.
	section := views.AppearanceSection(views.AppearanceData{Theme: settings.Theme, Saved: true})
	s.settingsSaved(w, r, sectionAppearance, templ.Join(section, menu), themeJS(settings.Theme))
}

// resetFormJS is the JavaScript that empties the form id after it was saved:
// the morph of Datastar keeps what the user typed in a field whose new markup
// has no value, so a saved password or key would stay on the page.
func resetFormJS(id string) string {
	return "document.getElementById(" + jsString(id) + ").reset()"
}

// themeJS is the JavaScript that sets the theme on the page: data-theme on the
// html element for Light and Dark, and none for System, which leaves the
// choice to the browser; and the theme-color elements of the head, as Layout
// writes them (one for the chosen theme, or one for each palette for System).
func themeJS(theme app.Theme) string {
	attr := "document.documentElement.removeAttribute('data-theme')"
	colours := [][2]string{{views.ThemeColorLight, "(prefers-color-scheme: light)"}, {views.ThemeColorDark, "(prefers-color-scheme: dark)"}}
	switch theme {
	case app.ThemeLight:
		attr = "document.documentElement.setAttribute('data-theme', " + jsString(string(theme)) + ")"
		colours = [][2]string{{views.ThemeColorLight, ""}}
	case app.ThemeDark:
		attr = "document.documentElement.setAttribute('data-theme', " + jsString(string(theme)) + ")"
		colours = [][2]string{{views.ThemeColorDark, ""}}
	}
	var list []string
	for _, c := range colours {
		list = append(list, "["+jsString(c[0])+", "+jsString(c[1])+"]")
	}
	metas := "((list) => { document.querySelectorAll('meta[name=\"theme-color\"]').forEach((m) => m.remove()); " +
		"list.forEach(([content, media]) => { const m = document.createElement('meta'); m.name = 'theme-color'; m.content = content; " +
		"if (media) { m.media = media; } document.head.append(m); }); })([" + strings.Join(list, ", ") + "])"
	return attr + ", " + metas
}

// themeRadioJS is the JavaScript that checks the radio button of the theme in
// the Appearance section of the settings page, so that the section agrees with
// the user menu after a choice made there; on a page without the section it
// does nothing.
func themeRadioJS(theme app.Theme) string {
	return "(document.getElementById(" + jsString("theme-"+string(theme)) + ") || {}).checked = true"
}

// sessionsRedirect answers GET /me/sessions: the page of the sessions is a
// section of the settings page now.
func (s *Server) sessionsRedirect(w http.ResponseWriter, r *http.Request) {
	s.redirect(w, r, views.SettingsSessionsPath, http.StatusSeeOther)
}

// avatarFile answers GET /avatars/{userID} with the avatar of the user, a PNG,
// for any logged-in person. A user without an avatar and an unknown user get
// the 404 page. The address carries the key of the avatar (?v=), so the
// picture is cached for a day.
func (s *Server) avatarFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("userID"), 10, 63)
	if err != nil || id == 0 {
		s.notFound(w, r)
		return
	}
	actor, _ := actorFrom(r.Context())
	f, key, err := s.svc.Accounts.OpenAvatar(r.Context(), actor, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			s.logger.Debug("closing an avatar failed", "err", closeErr)
		}
	}()
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", files.DownloadCSP)
	h.Set("ETag", `"`+key+`"`)
	// The key in the ETag and in the address says when the picture changed, so
	// no modification time is needed.
	http.ServeContent(w, r, "", time.Time{}, f)
}
