package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// sectionNotifications is the id of the Notifications section of the settings
// page, and the value of the query "saved" that shows "Saved" under it.
const sectionNotifications = "notifications"

// notifyPrefs reads what the Notifications section of the settings shows for
// the actor: the settings, and the rooms and conversations that have
// preferences of their own with the link to the dialog of each.
func (s *Server) notifyPrefs(ctx context.Context, actor app.Actor, settings app.Settings) (views.NotifyPrefsData, error) {
	d := views.NotifyPrefsData{Settings: settings}
	places, err := s.svc.Prefs.Places(ctx, actor)
	if err != nil || len(places) == 0 {
		return d, err
	}
	rooms, err := s.svc.Rooms.Mine(ctx, actor)
	if err != nil {
		return views.NotifyPrefsData{}, err
	}
	conversations, err := s.svc.Messages.Conversations(ctx, actor)
	if err != nil {
		return views.NotifyPrefsData{}, err
	}
	roomNames := make(map[uint64]string, len(rooms))
	for _, r := range rooms {
		roomNames[r.ID] = r.Name
	}
	conversationNames := make(map[uint64]string, len(conversations))
	for _, c := range conversations {
		conversationNames[c.ID] = c.DisplayName
	}
	for _, p := range places {
		item := views.PlaceListItem{Room: p.Place.IsRoom(), Summary: views.PlacePrefsSummary(p)}
		var ok bool
		if p.Place.IsRoom() {
			item.Name, ok = roomNames[p.Place.RoomID]
			item.Path = views.RoomPath(p.Place.RoomID) + "?" + views.PlacePrefsQuery
		} else {
			item.Name, ok = conversationNames[p.Place.ConversationID]
			item.Path = views.ConversationPath(p.Place.ConversationID) + "?" + views.PlacePrefsQuery
		}
		if ok {
			d.Places = append(d.Places, item)
		}
	}
	return d, nil
}

// settingsNotifications answers POST /me/settings/notifications: it saves what
// notifies the person, the sound, the browser notifications and the replies. A
// checkbox that is not in the form is off.
func (s *Server) settingsNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	sound, browser := r.PostFormValue("sound") != "", r.PostFormValue("browserNotifications") != ""
	update := app.SettingsUpdate{Sound: &sound, BrowserNotifications: &browser}
	if v := r.PostFormValue("notify"); v != "" {
		level := app.NotifyLevel(v)
		update.Notify = &level
	}
	if v := r.PostFormValue("soundName"); v != "" {
		name := app.SoundName(v)
		update.SoundName = &name
	}
	if v := r.PostFormValue("replies"); v != "" {
		mode := app.RepliesMode(v)
		update.Replies = &mode
	}
	groups := r.PostFormValue("groupMentions") != ""
	keywords := keywordsOf(r.PostFormValue("keywords"))
	update.GroupMentions, update.Keywords = &groups, &keywords
	settings, err := s.svc.Accounts.UpdateSettings(ctx, actor, update)
	if err != nil {
		typed := r.PostFormValue("keywords")
		s.settingsFailed(w, r, err, func(d *views.SettingsData, problems views.FormErrors) {
			d.Notifications.Errors = problems
			d.Notifications.KeywordsTyped = &typed
		}, func(d views.SettingsData) templ.Component { return views.NotifyPrefsSection(d.Notifications) })
		return
	}
	d, err := s.notifyPrefs(ctx, actor, settings)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = true
	s.settingsSaved(w, r, sectionNotifications, views.NotifyPrefsSection(d), "")
}

// keywordsOf splits the text of the keywords field at the commas. Segments
// that are only white space are dropped; the service checks the rest.
func keywordsOf(text string) []string {
	out := []string{}
	for _, k := range strings.Split(text, ",") {
		if strings.TrimSpace(k) != "" {
			out = append(out, k)
		}
	}
	return out
}

// placePrefsData reads the dialog "Notification preferences" of the actor for
// a room or a conversation. A viewer who may not have preferences in the place
// (not a member, no login) gets the data without an Action, which draws no
// dialog; any other failure is returned.
func (s *Server) placePrefsData(ctx context.Context, actor app.Actor, ref app.PlaceRef, title string) (views.PlacePrefsData, error) {
	prefs, err := s.svc.Prefs.Place(ctx, actor, ref)
	if err != nil {
		if isTag(err, app.TagForbidden) || isTag(err, app.TagNotFound) {
			return views.PlacePrefsData{}, nil
		}
		return views.PlacePrefsData{}, err
	}
	return views.PlacePrefsData{Prefs: prefs, Title: title, Action: placePrefsAction(ref)}, nil
}

// placePrefsAction is where the dialog of a place posts to.
func placePrefsAction(ref app.PlaceRef) string {
	if ref.IsRoom() {
		return views.RoomPrefsPath(ref.RoomID)
	}
	return views.ConversationPrefsPath(ref.ConversationID)
}

// placeUpdateOf reads the form of the dialog: every choice, and the checkboxes
// ("ignoreGroupMentions", "muted"), which are off when they are not in the form.
// Mark as unread is left out for a conversation, whose form does not offer it.
func placeUpdateOf(r *http.Request, ref app.PlaceRef) app.PlaceUpdate {
	ignore, muted := r.PostFormValue("ignoreGroupMentions") != "", r.PostFormValue("muted") != ""
	update := app.PlaceUpdate{IgnoreGroupMentions: &ignore, Muted: &muted}
	if v := r.PostFormValue("notify"); v != "" {
		notify := app.PlaceNotify(v)
		update.Notify = &notify
	}
	if v := r.PostFormValue("sound"); v != "" {
		sound := app.PlaceSound(v)
		update.Sound = &sound
	}
	if v := r.PostFormValue("markUnread"); v != "" && ref.IsRoom() {
		mark := app.MarkUnread(v)
		update.MarkUnread = &mark
	}
	return update
}

// roomPrefs answers POST /rooms/{id}/notifications: the dialog of the
// notification preferences of a room.
func (s *Server) roomPrefs(w http.ResponseWriter, r *http.Request) {
	id, ok := s.roomID(w, r)
	if !ok {
		return
	}
	s.placePrefs(w, r, app.RoomRef(id), views.RoomPath(id))
}

// conversationPrefs answers POST /messages/{id}/notifications: the dialog of
// the notification preferences of a conversation.
func (s *Server) conversationPrefs(w http.ResponseWriter, r *http.Request) {
	id, ok := s.conversationID(w, r)
	if !ok {
		return
	}
	s.placePrefs(w, r, app.ConversationRef(id), views.ConversationPath(id))
}

// placePrefs saves the dialog of a place. A Datastar request gets the form
// patched, with "Saved" or with the problems, so that the dialog stays open; a
// plain post is sent back to the page of the place. A place that the person
// may not read is the 404 page.
func (s *Server) placePrefs(w http.ResponseWriter, r *http.Request, ref app.PlaceRef, back string) {
	if !s.parseForm(w, r) {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	update := placeUpdateOf(r, ref)
	prefs, err := s.svc.Prefs.UpdatePlace(ctx, actor, ref, update)
	if err != nil {
		if !isDatastar(r) || isTag(err, app.TagNotFound) || isTag(err, app.TagUnauthenticated) {
			s.fail(w, r, err)
			return
		}
		s.formFailed(w, r, err, func(problems views.FormErrors) formView {
			d, _ := s.placePrefsData(ctx, actor, ref, "")
			return formView{fragment: views.PlacePrefsForm(views.PlacePrefsData{
				Prefs: d.Prefs, Title: d.Title, Action: placePrefsAction(ref), Errors: problems,
			})}
		})
		return
	}
	if !isDatastar(r) {
		s.redirect(w, r, back, http.StatusSeeOther)
		return
	}
	s.patch(w, r, views.PlacePrefsForm(views.PlacePrefsData{Prefs: prefs, Action: placePrefsAction(ref), Saved: true}))
}
