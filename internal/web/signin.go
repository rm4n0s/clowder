package web

import (
	"context"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// sectionSignIn is the id of the Sign-in section of the settings page, and the
// value of the query "saved" that returns to it.
const sectionSignIn = "signin"

// signInData reads what the Sign-in section shows for the actor: their SSH
// keys and the state of the SSH server.
func (s *Server) signInData(ctx context.Context, actor app.Actor) (views.SignInData, error) {
	keys, err := s.svc.SSHKeys.List(ctx, actor)
	if err != nil {
		return views.SignInData{}, err
	}
	settings, err := s.svc.Accounts.Settings(ctx, actor)
	if err != nil {
		return views.SignInData{}, err
	}
	return views.SignInData{Keys: keys, Server: s.svc.SSHKeys.Server(actor.Username), CodeRequired: settings.SSHCodeRequired}, nil
}

// settingsSSHKeyAdd answers POST /me/settings/ssh-keys: it registers the public
// key of the form under the name, after it checks the current password.
func (s *Server) settingsSSHKeyAdd(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	name, line := r.PostFormValue("name"), r.PostFormValue("publicKey")
	_, err := s.svc.SSHKeys.Add(ctx, actor, name, line, r.PostFormValue("currentPassword"), clientIP(r, s.cfg.TrustProxyHeaders))
	if err != nil {
		s.settingsFailed(w, r, err, func(d *views.SettingsData, problems views.FormErrors) {
			d.SignIn.Add = views.SSHKeyAddData{Name: name, PublicKey: line, Errors: problems}
		}, func(d views.SettingsData) templ.Component { return views.SignInSection(d.SignIn) })
		return
	}
	d, err := s.signInData(ctx, actor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = true
	s.settingsSaved(w, r, sectionSignIn, views.SignInSection(d), resetFormJS("ssh-key-add-form"))
}

// settingsSSHKeyRemove answers POST /me/settings/ssh-keys/{id}/remove: it
// removes the key of the user, after it checks the current password. A key
// that is gone, or that is somebody else's, gives the section as it is: the
// answer is the same for both.
func (s *Server) settingsSSHKeyRemove(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	id, perr := strconv.ParseUint(r.PathValue("id"), 10, 63)
	if perr != nil || id == 0 {
		s.notFound(w, r)
		return
	}
	err := s.svc.SSHKeys.Remove(ctx, actor, id, r.PostFormValue("currentPassword"), clientIP(r, s.cfg.TrustProxyHeaders))
	if err != nil && !isTag(err, app.TagNotFound) {
		s.settingsFailed(w, r, err, func(d *views.SettingsData, problems views.FormErrors) {
			d.SignIn.RemoveKeyID, d.SignIn.RemoveErrors = id, problems
		}, func(d views.SettingsData) templ.Component { return views.SignInSection(d.SignIn) })
		return
	}
	d, derr := s.signInData(ctx, actor)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	d.Saved = err == nil
	s.settingsSaved(w, r, sectionSignIn, views.SignInSection(d), "")
}

// settingsSSHCode answers POST /me/settings/ssh-code: it turns "Ask for an SSH
// code when I log in" on ("enabled" is "true") or off ("false"), after it
// checks the current password.
func (s *Server) settingsSSHCode(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	password, ip := r.PostFormValue("currentPassword"), clientIP(r, s.cfg.TrustProxyHeaders)
	var err error
	switch r.PostFormValue("enabled") {
	case "true":
		err = s.svc.SSHKeys.EnableSSHCode(ctx, actor, password, ip)
	case "false":
		err = s.svc.SSHKeys.DisableSSHCode(ctx, actor, password, ip)
	default:
		err = errors.New(app.TagInvalidInput, "enabled is not true or false",
			"fields", map[string]string{app.FieldForm: messageActionInvalid})
	}
	if err != nil {
		s.settingsFailed(w, r, err, func(d *views.SettingsData, problems views.FormErrors) {
			d.SignIn.Code = problems
		}, func(d views.SettingsData) templ.Component { return views.SignInSection(d.SignIn) })
		return
	}
	d, err := s.signInData(ctx, actor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = true
	s.settingsSaved(w, r, sectionSignIn, views.SignInSection(d), resetFormJS("ssh-code-form"))
}
